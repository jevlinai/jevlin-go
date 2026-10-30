// Package config resolves the client's configuration exactly once, into an
// immutable struct: compiled defaults (defaults.go), then the TOML file a
// command names, then the JEVLIN_* endpoint variables. Nothing re-reads it
// later.
//
// The file is configuration format 1. It begins `schema = 1` and names every
// endpoint in its own section:
//
//	schema = 1
//
//	[router]
//	url = "https://api.jevlin.ai"
//
//	[platform]
//	url = "https://platform.jevlin.ai"
//
//	[agents]
//	url = "https://agents-v1.jevlin.ai"
//
//	[mining]
//	as_url   = "https://rewards-testnet.jevlin.ai"
//	chain_id = "twilight-testnet-1"
//	slot_id  = 3
//
// Every section and key is optional; a missing one takes its compiled
// default. A file without `schema = 1`, or with a key format 1 does not
// define, is refused with one message and never reinterpreted: an older
// file's endpoints were chosen under rules this client no longer has, and
// guessing what they meant now is how a key reaches a host it was not issued
// for.
//
// Unsafe states are rejected here rather than warned about later: every URL
// that a credential is sent to must be https, or plain http on a loopback
// host, and must carry no userinfo.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Schema is the configuration format this client reads and setup writes.
const Schema = 1

// ErrLegacyConfig marks a file from before configuration format 1: one with
// no schema line. Only such a file is one setup may set aside and replace; a
// file that names a schema is the participant's current configuration, and a
// mistake in it is refused, never replaced.
var ErrLegacyConfig = errors.New("this configuration is from an older jevlin; run `jevlin setup`")

// The runtime endpoint variables. Each replaces its URL after the file, under
// the file's own URL rules; JEVLIN_CONFIG still chooses the file.
const (
	EnvRouterURL    = "JEVLIN_ROUTER_URL"
	EnvPlatformURL  = "JEVLIN_PLATFORM_URL"
	EnvAgentsAPIURL = "JEVLIN_AGENTS_API_URL"
	EnvASURL        = "JEVLIN_AS_URL"
)

// Mining is the AS-facing mining-plane configuration (contract §18–§19).
// Enabled is only the scripted first answer consumed by onboarding; the
// persisted mining decision is runtime authority, while a non-empty ASBaseURL
// says AS work is configured.
type Mining struct {
	Enabled     bool
	ASBaseURL   string
	ChainID     string
	SlotID      uint64
	MetadataTTL time.Duration
	// StateDir holds the installation's secret material (ADR-0008:
	// owner-only files — DPoP key, refresh authorization,
	// participation_secret). Defaults to os.UserConfigDir()/jevlin/state.
	StateDir string
	// SpoolDir holds durably-written observations awaiting delivery.
	// Separate from StateDir because its contents are evidence, not
	// secrets. Defaults to StateDir/spool.
	SpoolDir string
	// TargetEpoch pins the reward epoch this installation participates in,
	// overriding the AS's current-target endpoint. A pointer because absent
	// and zero are different answers: nil means "ask the AS".
	TargetEpoch *uint64
	// Collector tuning. Zero means "use the collector's own defaults".
	CollectorInterval    time.Duration
	CollectorBaseBackoff time.Duration
	CollectorMaxBackoff  time.Duration
	CollectorMaxAttempts int
	// PayoutAddress is the scripted-install answer to connect/mining
	// enable's terminal question (agent onboarding design §5.5).
	PayoutAddress string
	// PlatformSlot names which platform-advertised mining slot connect
	// should enroll into, required only when the platform offers more
	// than one.
	PlatformSlot string
}

// Platform is the search platform connect and mining enable talk to. BaseURL
// is the human-facing portal, the origin a printed claim_url is checked
// against (invariant 12), and is never dialed; AgentsAPIURL is where
// register/status/enroll requests go, carrying the platform-issued key.
type Platform struct {
	BaseURL      string
	AgentsAPIURL string
}

// The words [update] mode takes (cmd/jevlin/update_check.go).
const (
	UpdateApply  = "apply"  // a native copy replaces itself with a newer verified release
	UpdateNotify = "notify" // the check runs; status and doctor say when a newer release exists
	UpdateOff    = "off"    // no check at all
)

// Update is the [update] block: what the daily release check the agent hooks
// start may do.
type Update struct {
	// Mode is apply (the default), notify or off.
	Mode string
	// UnknownMode is a word the file set that this version cannot name; Mode
	// is then notify — never off, which would silence the check by a typo,
	// and never apply, which would replace a binary on a word nobody chose.
	UnknownMode string
}

// Miner is the search side of the client: where searches go and what a
// served search leaves for the flush.
type Miner struct {
	// Enabled says intake is configured: a served search records its
	// request id for the flush. It is true exactly when an AS is
	// configured ([mining] as_url), because intake exists only to be
	// delivered there.
	Enabled bool
	// RouterURL is where searches, fetches, limits reads and turn ends go:
	// [router] url, JEVLIN_ROUTER_URL, or the compiled default. Never nil
	// in a loaded config.
	RouterURL *url.URL
	// IntakeDir holds one small JSON file per served search until a flush
	// promotes it into the spool under a joined (slot, epoch).
	IntakeDir string
	// SessionsDir holds the per-workspace lineage sidecars the hooks write
	// and `search` reads.
	SessionsDir string
	// FlushInterval is how often a flush repeats the AS round trip.
	FlushInterval time.Duration
	// TurnEnd opts this installation in to reporting, when a turn that
	// searched ends, the assistant's final message to the router. Off by
	// default: it is model-written text leaving the machine.
	TurnEnd bool
	// Tier is the search tier a request carries when neither the caller nor
	// the participant's `jevlin agents tier` file named one. Empty means the
	// router's default. One of SearchTiers when set.
	Tier string
	// UnknownTier is a [router] tier that is not one of SearchTiers: kept
	// aside so status can say why the router's default applies, and never
	// sent.
	UnknownTier string
}

// SearchTiers is the router's tier vocabulary this client knows.
var SearchTiers = []string{"fast", "balanced", "max"}

// KnownSearchTier reports whether tier is one this client can name.
func KnownSearchTier(tier string) bool {
	for _, t := range SearchTiers {
		if t == tier {
			return true
		}
	}
	return false
}

// Config is the resolved, immutable configuration.
type Config struct {
	Mining   Mining
	Miner    Miner
	Platform Platform
	Update   Update

	// MiningEnabledExplicit is true when the config file itself wrote
	// `[mining] enabled` (true or false), as distinct from saying nothing.
	// connect and mining enable ask their interactive question only when
	// this is false.
	MiningEnabledExplicit bool
}

// fileConfig mirrors configuration format 1. Any key outside it is refused.
type fileConfig struct {
	Schema *int `toml:"schema"`
	Router struct {
		URL  string `toml:"url"`
		Tier string `toml:"tier"`
	} `toml:"router"`
	Platform struct {
		URL string `toml:"url"`
	} `toml:"platform"`
	Agents struct {
		URL string `toml:"url"`
	} `toml:"agents"`
	Mining struct {
		Enabled              bool     `toml:"enabled"`
		ASURL                string   `toml:"as_url"`
		ChainID              string   `toml:"chain_id"`
		SlotID               *uint64  `toml:"slot_id"`
		TargetEpoch          *uint64  `toml:"target_epoch"`
		MetadataTTL          duration `toml:"metadata_ttl"`
		StateDir             string   `toml:"state_dir"`
		SpoolDir             string   `toml:"spool_dir"`
		IntakeDir            string   `toml:"intake_dir"`
		SessionsDir          string   `toml:"sessions_dir"`
		FlushInterval        duration `toml:"flush_interval"`
		TurnEnd              bool     `toml:"turn_end"`
		CollectorInterval    duration `toml:"collector_interval"`
		CollectorBaseBackoff duration `toml:"collector_base_backoff"`
		CollectorMaxBackoff  duration `toml:"collector_max_backoff"`
		CollectorMaxAttempts int      `toml:"collector_max_attempts"`
		PayoutAddress        string   `toml:"payout_address"`
		PlatformSlot         string   `toml:"platform_slot"`
	} `toml:"mining"`
	Update struct {
		Mode string `toml:"mode"`
	} `toml:"update"`
}

type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Load resolves the configuration. args may carry -config (the file) and
// -version; with no -config, JEVLIN_CONFIG names the file. With neither, the
// compiled defaults and the endpoint variables are the whole configuration.
func Load(args []string, getenv func(string) string) (cfg *Config, showVersion bool, err error) {
	fs := flag.NewFlagSet("jevlin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		flagConfig  = fs.String("config", "", "path to TOML config file")
		flagVersion = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}
	if fs.NArg() > 0 {
		return nil, false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *flagVersion {
		return nil, true, nil
	}

	var raw rawConfig
	// Only an explicit path (flag or env) is read, and it must exist. A
	// jevlin.toml in the working directory is never picked up on its own:
	// whoever wrote that directory would choose where the participant's keys
	// are sent.
	path := *flagConfig
	if path == "" {
		path = getenv("JEVLIN_CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path) // #nosec G304 -- the config path is operator-supplied by design
		if err != nil {
			return nil, false, fmt.Errorf("config file: %w", err)
		}
		if err := raw.applyBytes(path, data); err != nil {
			return nil, false, err
		}
	}
	applyEnv(&raw, getenv)
	cfg, err = raw.finish()
	if err != nil {
		return nil, false, err
	}
	return cfg, false, nil
}

// LoadBytes is Load applied to config file bytes already in hand, for setup's
// dry run, which computes the exact bytes it would publish but never does.
func LoadBytes(data []byte, getenv func(string) string) (*Config, error) {
	var raw rawConfig
	if err := raw.applyBytes("config", data); err != nil {
		return nil, err
	}
	applyEnv(&raw, getenv)
	return raw.finish()
}

// applyEnv is the runtime endpoint overlay. Each variable replaces its URL,
// and the URL is then held to the file's rules in finish.
func applyEnv(r *rawConfig, getenv func(string) string) {
	if v := getenv(EnvRouterURL); v != "" {
		r.routerURL = v
	}
	if v := getenv(EnvPlatformURL); v != "" {
		r.platformURL = v
	}
	if v := getenv(EnvAgentsAPIURL); v != "" {
		r.agentsAPIURL = v
	}
	if v := getenv(EnvASURL); v != "" {
		r.asURL = v
	}
}

// rawConfig holds pre-validation values from the file and the environment.
type rawConfig struct {
	routerURL string
	tier      string

	platformURL  string
	agentsAPIURL string

	miningEnabled         bool
	miningEnabledExplicit bool
	asURL                 string
	chainID               string
	slotID                *uint64
	targetEpoch           *uint64
	metadataTTL           time.Duration
	stateDir              string
	spoolDir              string
	intakeDir             string
	sessionsDir           string
	flushInterval         time.Duration
	turnEnd               bool
	collectorInterval     time.Duration
	collectorBaseBackoff  time.Duration
	collectorMaxBackoff   time.Duration
	collectorMaxAttempts  int
	payoutAddress         string
	platformSlot          string

	updateMode string
}

// formatOneSections are the top-level keys format 1 defines.
var formatOneSections = map[string]bool{"schema": true, "router": true, "platform": true, "agents": true, "mining": true, "update": true}

// unknownKeysError refuses a format-1 file holding keys the format does not
// define. It is not ErrLegacyConfig: the file says it is format 1, so a key
// it holds is a typo or a setting carried over by hand, and setup leaves the
// file for the participant to correct rather than replacing it.
func unknownKeysError(source string, keys []string) error {
	return fmt.Errorf("config file %s: format 1 does not define %s; correct or remove it", source, strings.Join(keys, ", "))
}

// legacyError is ErrLegacyConfig for one file, saying what made it so.
func legacyError(source, why string) error {
	return fmt.Errorf("config file %s: %w (%s)", source, ErrLegacyConfig, why)
}

// applyBytes parses one format-1 document. source names the bytes in an
// error message.
func (r *rawConfig) applyBytes(source string, data []byte) error {
	// Read as a plain document first: a file that is not TOML says so, and
	// one with no schema line is an older file whatever else it holds.
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return fmt.Errorf("config file %s: %w", source, err)
	}
	schema, ok := doc["schema"]
	if !ok {
		return legacyError(source, "no schema line")
	}
	// The schema is judged before anything else in the file: a file of
	// another schema may be newer, and its keys are not this format's to
	// call unknown.
	if n, isInt := schema.(int64); !isInt || n != Schema {
		return fmt.Errorf("config file %s: its schema is not one this jevlin reads (it reads schema = %d); "+
			"use the jevlin that wrote it, or write schema = %d", source, Schema, Schema)
	}
	var f fileConfig
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		// A section of another shape (an older file's [[provider]] array)
		// is a key format 1 does not define; a value of the wrong type in
		// a format-1 key is a mistake in this file, and says so.
		for key := range doc {
			if !formatOneSections[key] {
				return unknownKeysError(source, []string{key})
			}
		}
		return fmt.Errorf("config file %s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return unknownKeysError(source, keys)
	}

	r.routerURL = f.Router.URL
	r.tier = f.Router.Tier
	r.platformURL = f.Platform.URL
	r.agentsAPIURL = f.Agents.URL

	m := f.Mining
	r.miningEnabled = m.Enabled
	// IsDefined, not m.Enabled itself: connect and mining enable must tell
	// "the file wrote enabled = false" from "the file said nothing".
	r.miningEnabledExplicit = md.IsDefined("mining", "enabled")
	r.asURL = m.ASURL
	r.chainID = m.ChainID
	r.slotID = m.SlotID
	r.targetEpoch = m.TargetEpoch
	r.metadataTTL = m.MetadataTTL.Duration
	r.stateDir = m.StateDir
	r.spoolDir = m.SpoolDir
	r.intakeDir = m.IntakeDir
	r.sessionsDir = m.SessionsDir
	r.flushInterval = m.FlushInterval.Duration
	r.turnEnd = m.TurnEnd
	r.collectorInterval = m.CollectorInterval.Duration
	r.collectorBaseBackoff = m.CollectorBaseBackoff.Duration
	r.collectorMaxBackoff = m.CollectorMaxBackoff.Duration
	r.collectorMaxAttempts = m.CollectorMaxAttempts
	r.payoutAddress = m.PayoutAddress
	r.platformSlot = m.PlatformSlot
	r.updateMode = f.Update.Mode
	return nil
}

func (r *rawConfig) finish() (*Config, error) {
	mining, err := r.finishMining()
	if err != nil {
		return nil, err
	}
	miner, err := r.finishMiner(mining)
	if err != nil {
		return nil, err
	}
	platform, err := r.finishPlatform()
	if err != nil {
		return nil, err
	}
	return &Config{
		Mining:                mining,
		Miner:                 miner,
		Platform:              platform,
		Update:                r.finishUpdate(),
		MiningEnabledExplicit: r.miningEnabledExplicit,
	}, nil
}

// finishUpdate resolves the [update] block. A word this version cannot name
// does not stop the config from loading; it is kept aside for doctor, and the
// mode falls to notify.
func (r *rawConfig) finishUpdate() Update {
	switch mode := strings.ToLower(strings.TrimSpace(r.updateMode)); mode {
	case "":
		return Update{Mode: UpdateApply}
	case UpdateApply, UpdateNotify, UpdateOff:
		return Update{Mode: mode}
	default:
		return Update{Mode: UpdateNotify, UnknownMode: r.updateMode}
	}
}

// ParseEndpoint holds a URL a credential is sent to, or one a printed URL is
// checked against, to the rule every such URL shares (invariant 5): an
// absolute http(s) URL with a host, https unless the host is loopback, and
// no userinfo, query or fragment. field names the URL in the error.
func ParseEndpoint(raw, field string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("%s: must be an absolute http(s) URL", field)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s: must not carry userinfo", field)
	}
	// A host written in Unicode (or percent-encoded into it) is dialed as
	// its IDNA form, a name other than the one shown; only an ASCII host
	// reads as what is dialed. A participant's own IDN is written in its
	// xn-- form.
	for _, c := range []byte(u.Hostname()) {
		if c < 0x21 || c > 0x7e {
			return nil, fmt.Errorf("%s: the host must be plain ASCII (write an internationalized name in its xn-- form)", field)
		}
	}
	if u.ForceQuery || strings.ContainsAny(raw, "?#") {
		return nil, fmt.Errorf("%s: must not carry a query or fragment", field)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%s: plain http is permitted only for loopback hosts, got %q — "+
			"use https, or a loopback address for local development", field, u.Host)
	}
	return u, nil
}

// finishPlatform resolves the portal and the agents API. The agents API
// defaults to the compiled one only when the portal is the compiled one, and
// to the portal itself when the portal is loopback (a local stub serving
// both). A custom non-loopback portal with no agents API named is refused:
// pairing it with the production API would send a devnet's registrations, and
// the key they mint, to production.
func (r *rawConfig) finishPlatform() (Platform, error) {
	baseRaw := r.platformURL
	if baseRaw == "" {
		baseRaw = DefaultPlatformBaseURL
	}
	base, err := ParseEndpoint(baseRaw, "platform.url")
	if err != nil {
		return Platform{}, err
	}
	agentsRaw := r.agentsAPIURL
	if agentsRaw == "" {
		switch {
		case baseRaw == DefaultPlatformBaseURL:
			agentsRaw = DefaultAgentsAPIURL
		case isLoopbackHost(base.Hostname()):
			agentsRaw = baseRaw
		default:
			return Platform{}, errors.New("agents.url: required when platform.url names anything other than the default " +
				"platform — a devnet or staging deployment must name both")
		}
	}
	if _, err := ParseEndpoint(agentsRaw, "agents.url"); err != nil {
		return Platform{}, err
	}
	return Platform{BaseURL: baseRaw, AgentsAPIURL: agentsRaw}, nil
}

// finishMiner resolves the router and the search side's directories. The
// directories default beside the mining state so one tree holds everything
// the participant owns.
func (r *rawConfig) finishMiner(mining Mining) (Miner, error) {
	routerRaw := r.routerURL
	if routerRaw == "" {
		routerRaw = DefaultRouterURL
	}
	router, err := ParseEndpoint(routerRaw, "router.url")
	if err != nil {
		return Miner{}, err
	}
	m := Miner{
		Enabled:       mining.ASBaseURL != "",
		RouterURL:     router,
		IntakeDir:     r.intakeDir,
		SessionsDir:   r.sessionsDir,
		FlushInterval: r.flushInterval,
		TurnEnd:       r.turnEnd,
	}
	if tier := strings.ToLower(strings.TrimSpace(r.tier)); KnownSearchTier(tier) {
		m.Tier = tier
	} else if tier != "" {
		m.UnknownTier = r.tier
	}
	if m.FlushInterval == 0 {
		m.FlushInterval = 3 * time.Minute
	}
	if m.FlushInterval < 0 {
		return Miner{}, errors.New("mining.flush_interval: must not be negative")
	}
	if mining.StateDir != "" {
		base := filepath.Dir(mining.StateDir)
		if m.IntakeDir == "" {
			m.IntakeDir = filepath.Join(base, "intake")
		}
		if m.SessionsDir == "" {
			m.SessionsDir = filepath.Join(base, "sessions")
		}
	}
	if m.Enabled && (m.IntakeDir == "" || m.SessionsDir == "") {
		return Miner{}, errors.New("mining: intake_dir and sessions_dir are required when no mining.state_dir can be derived")
	}
	return m, nil
}

// finishMining resolves the mining plane. An absent AS is a valid
// search-only configuration; once an AS is named, by the file or
// JEVLIN_AS_URL, its participant identity defaults to the compiled chain and
// slot and every value must validate.
func (r *rawConfig) finishMining() (Mining, error) {
	m := Mining{
		Enabled:              r.miningEnabled,
		ASBaseURL:            r.asURL,
		ChainID:              r.chainID,
		MetadataTTL:          r.metadataTTL,
		CollectorInterval:    r.collectorInterval,
		CollectorBaseBackoff: r.collectorBaseBackoff,
		CollectorMaxBackoff:  r.collectorMaxBackoff,
		CollectorMaxAttempts: r.collectorMaxAttempts,
		PayoutAddress:        r.payoutAddress,
		PlatformSlot:         r.platformSlot,
		StateDir:             r.stateDir,
		SpoolDir:             r.spoolDir,
	}
	// Copied, not aliased: the resolved configuration is immutable.
	if r.targetEpoch != nil {
		pinned := *r.targetEpoch
		m.TargetEpoch = &pinned
	}
	if r.slotID != nil {
		m.SlotID = *r.slotID
	}
	if m.MetadataTTL == 0 {
		m.MetadataTTL = 15 * time.Minute
	}
	if m.StateDir == "" {
		if base, err := os.UserConfigDir(); err == nil {
			m.StateDir = filepath.Join(base, "jevlin", "state")
		}
	}
	if m.SpoolDir == "" && m.StateDir != "" {
		m.SpoolDir = filepath.Join(m.StateDir, "spool")
	}
	if m.ASBaseURL == "" {
		return m, nil
	}
	// §18, rejected here rather than at first use: every token, DPoP proof
	// and observation this client sends to the AS would otherwise cross the
	// wire in the clear on a routable plain-http host.
	if _, err := ParseEndpoint(m.ASBaseURL, "mining.as_url"); err != nil {
		return Mining{}, err
	}
	if m.ChainID == "" {
		m.ChainID = DefaultChainID
	}
	if r.slotID == nil {
		m.SlotID = DefaultSlotID
	}
	if m.MetadataTTL < 0 {
		return Mining{}, errors.New("mining.metadata_ttl: must not be negative")
	}
	if m.StateDir == "" {
		return Mining{}, errors.New("mining.state_dir: required when an authorization server is configured (no user config directory could be determined for the default)")
	}
	if m.CollectorInterval < 0 || m.CollectorBaseBackoff < 0 || m.CollectorMaxBackoff < 0 {
		return Mining{}, errors.New("mining: collector durations must not be negative")
	}
	if m.CollectorMaxAttempts < 0 {
		return Mining{}, errors.New("mining.collector_max_attempts: must not be negative (0 means unlimited)")
	}
	return m, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
