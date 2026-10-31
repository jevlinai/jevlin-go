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
//	[search]
//	state_dir    = "/home/u/.jevlin/state"
//	sessions_dir = "/home/u/.jevlin/sessions"
//
// Every section and key is optional; a missing one takes its compiled
// default. A file without `schema = 1`, or with a key format 1 does not
// define, is refused with one message and never reinterpreted: an older
// file's endpoints were chosen under rules this client no longer has, and
// guessing what they meant now is how a key reaches a host it was not issued
// for. `jevlin setup` sets such a file aside and writes format 1.
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
)

// Platform is the search platform connect talks to (agent onboarding
// design, search-platform-agent-onboarding-design.md). Both URLs are always
// resolved and validated, and nothing dials either unless connect is
// actually invoked.
//
// Two URLs, not one: search-router runs the human-facing portal and
// claim pages on one host and the machine-facing /v1/agents/* API on
// another. BaseURL is the portal — [platform] url, the origin a printed
// claim_url is checked against (invariant 12). AgentsAPIURL is [agents] url,
// where the actual register/status requests go. A single shared origin was
// this package's original assumption; live testing found the real
// deployment splits it in two, and pkg/platform.Client now takes both rather
// than deriving one from the other.
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
// start may do. The check contacts only the compiled-in GitHub release
// origin, with no credential (invariant 5's one exception); a copy npm
// installed is never replaced, whatever Mode says, and is only noted.
type Update struct {
	// Mode is apply (the default), notify or off. Apply is the default
	// because a release is installed only under the project's signature over
	// its checksums.txt, checked against a key compiled into the binary doing
	// the installing (internal/selfupdate's SignedVerifier).
	Mode string
	// UnknownMode is a word the file set that this version cannot name; Mode
	// is then notify — never off, which would silence the check by a typo,
	// and never apply, which would replace a binary on a word nobody chose.
	UnknownMode string
}

// Search is the search side of the client: where searches go and where this
// installation keeps what the hooks and the search pass between themselves.
type Search struct {
	// RouterURL is where searches, fetches, limits reads and turn ends go:
	// [router] url, JEVLIN_ROUTER_URL, or the compiled default. Never nil
	// in a loaded config, and there is no fallback to anything else.
	RouterURL *url.URL
	// StateDir holds the installation's state: the trace key, the agent
	// registration, the connect lock and the hooks' window state. The
	// installation's own directory is its parent: credentials.json, the
	// registration journal and the claim record live there, outside every
	// directory a sandboxed agent may write. [search] state_dir; defaults to
	// os.UserConfigDir()/jevlin/state.
	StateDir string
	// SessionsDir holds the per-workspace lineage sidecars the hooks
	// write and `search` reads. [search] sessions_dir; defaults beside
	// StateDir.
	SessionsDir string
	// TurnEnd opts this installation in to reporting, when a turn that
	// searched ends, the assistant's final message to the router. [search]
	// turn_end; off by default: it is model-written text leaving the
	// machine.
	TurnEnd bool
	// Tier is the search tier a request carries when neither the caller
	// (the -tier flag, the stdin request's "tier") nor the participant's
	// `jevlin agents tier` file named one: [router] tier, written by hand.
	// Empty means the request carries no tier and the router applies the
	// tenant's default. One of SearchTiers when set, lower-cased and
	// trimmed at load.
	Tier string
	// UnknownTier is a [router] tier that is not one of SearchTiers even
	// after that: kept aside so status can say why the router's default
	// applies, and never sent. A word this client cannot use does not stop
	// the config from loading, because that would stop search over a value
	// that only chooses a band.
	UnknownTier string
}

// SearchTiers is the router's tier vocabulary this client knows — the
// cost bands of search-router internal/tenant. A config naming another
// word is kept aside at load rather than sent, so a typo is caught here and
// not as a 400 on every search.
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
	Search   Search
	Platform Platform
	Update   Update
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
	Search struct {
		StateDir    string `toml:"state_dir"`
		SessionsDir string `toml:"sessions_dir"`
		TurnEnd     bool   `toml:"turn_end"`
	} `toml:"search"`
	Update struct {
		Mode string `toml:"mode"`
	} `toml:"update"`
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
	// whoever wrote that directory (a cloned repo, an archive) would choose
	// where the participant's keys are sent.
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

// LoadBytes is Load applied to config file bytes already in hand, for a
// caller that knows what a file would contain before anything writes it
// there — setup, which loads the exact bytes it is about to publish, and
// its dry run, which never publishes them. There is no -config flag to
// override it: that names a path on argv, and bytes already in memory have
// none.
func LoadBytes(data []byte, getenv func(string) string) (*Config, error) {
	return LoadBytesAs(data, "config", getenv)
}

// LoadBytesAs is LoadBytes with source naming the bytes in an error, for a
// caller that has the path they were read from or are about to be written
// to: setup's refusal of a file then names that file, rather than "config
// file config".
func LoadBytesAs(data []byte, source string, getenv func(string) string) (*Config, error) {
	var raw rawConfig
	if err := raw.applyBytes(source, data); err != nil {
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
}

// rawConfig holds pre-validation values from the file and the environment.
type rawConfig struct {
	routerURL string
	tier      string

	platformURL  string
	agentsAPIURL string

	stateDir    string
	sessionsDir string
	turnEnd     bool

	updateMode string
}

// formatOneSections are the top-level keys format 1 defines.
var formatOneSections = map[string]bool{"schema": true, "router": true, "platform": true, "agents": true, "search": true, "update": true}

// formatOneWithoutSchema says why a file with no schema line was written as
// format 1 after all, or "" when nothing in it says so. No version before
// format 1 wrote a schema key, a [router] or [agents] table, or a [platform]
// url (its portal key was base_url); format 1 writes all of them. So a key
// spelled like schema anywhere in the file (a case or two letters away, or
// under a table), or one of those, is a format-1 file whose schema line is
// mistyped or missing, not an older file to replace. [search] says nothing
// either way: this client used that table before format 1 as well.
func formatOneWithoutSchema(doc map[string]any) string {
	if k := schemaLikeKey(doc, ""); k != "" {
		return fmt.Sprintf("it holds %q, which reads as a mistyped schema line", k)
	}
	for _, table := range []string{"router", "agents"} {
		if _, ok := doc[table].(map[string]any); ok {
			return "it holds a [" + table + "] table, which only configuration format 1 has"
		}
	}
	if p, ok := doc["platform"].(map[string]any); ok {
		if _, ok := p["url"]; ok {
			return "its [platform] table names url, which only configuration format 1 does"
		}
	}
	return ""
}

// schemaLikeKey is the first key, at any depth, that is "schema" in another
// case or within two edits of it, as a dotted path; or "".
func schemaLikeKey(doc map[string]any, prefix string) string {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if editDistance(strings.ToLower(k), "schema") <= 2 {
			return prefix + k
		}
		if sub, ok := doc[k].(map[string]any); ok {
			if found := schemaLikeKey(sub, prefix+k+"."); found != "" {
				return found
			}
		}
	}
	return ""
}

// editDistance is the Levenshtein distance between a and b, by byte.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

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
// error message (a path for a real file, or a fixed label for bytes that
// only exist in memory — LoadBytes has no path to give it).
func (r *rawConfig) applyBytes(source string, data []byte) error {
	// Read as a plain document first: a file that is not TOML says so, and
	// one with no schema line is an older file whatever else it holds.
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return fmt.Errorf("config file %s: %w", source, err)
	}
	schema, ok := doc["schema"]
	if !ok {
		if why := formatOneWithoutSchema(doc); why != "" {
			// Not ErrLegacyConfig: setup sets an older file aside and
			// writes a new one, and this file is the participant's own
			// format-1 file with a mistake in it, which is left for them.
			return fmt.Errorf("config file %s: %s, but it has no `schema = 1` line at the top; "+
				"correct it (a line `schema = 1` before the first table)", source, why)
		}
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
		var unknown []string
		for key := range doc {
			if !formatOneSections[key] {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return unknownKeysError(source, unknown)
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
	r.stateDir = f.Search.StateDir
	r.sessionsDir = f.Search.SessionsDir
	r.turnEnd = f.Search.TurnEnd
	r.updateMode = f.Update.Mode
	return nil
}

func (r *rawConfig) finish() (*Config, error) {
	search, err := r.finishSearch()
	if err != nil {
		return nil, err
	}
	platform, err := r.finishPlatform()
	if err != nil {
		return nil, err
	}
	return &Config{
		Search:   search,
		Platform: platform,
		Update:   r.finishUpdate(),
	}, nil
}

// finishUpdate resolves the [update] block the way [router] tier is resolved:
// the word is lowercased and trimmed, and one this version cannot name does
// not stop the config from loading — a typo must not cost every search. It
// is kept aside for doctor to name, and the mode falls to notify, not to the
// default: a word the participant wrote is a choice this version cannot
// read, and a binary is not replaced on it.
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
	// Every search sends the participant's sr- key in Authorization, and
	// connect the platform-issued key, so a routable plain-http host is a
	// cleartext-credential exposure. The loopback carve-out is deliberate,
	// for local development without TLS; testing against a ROUTABLE devnet
	// needs TLS there, or the client co-located over loopback — that is
	// the intended consequence, not a gap.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%s: plain http is permitted only for loopback hosts, got %q — "+
			"use https, or a loopback address for local development", field, u.Host)
	}
	return u, nil
}

// finishPlatform resolves the portal and the agents API. The agents API
// defaults to the compiled one only when the portal is the compiled one, and
// to the portal itself when the portal is loopback (a local stub serving
// both, which is every stub-backed test's own setup). A custom non-loopback
// portal with no agents API named is refused: pairing it with the production
// API would send a devnet's registrations, and the key they mint, to
// production — which is exactly what happened once, live, before this rule.
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
		case sameOrigin(base, DefaultPlatformBaseURL):
			agentsRaw = DefaultAgentsAPIURL
		case isLoopbackHost(base.Hostname()):
			agentsRaw = baseRaw
		default:
			return Platform{}, errors.New("agents.url: required when platform.url names anything other than the default " +
				"platform — a devnet or staging deployment must name both (JEVLIN_AGENTS_API_URL beside JEVLIN_PLATFORM_URL " +
				"when the portal came from the environment)")
		}
	}
	if _, err := ParseEndpoint(agentsRaw, "agents.url"); err != nil {
		return Platform{}, err
	}
	return Platform{BaseURL: baseRaw, AgentsAPIURL: agentsRaw}, nil
}

// finishSearch resolves the router and the search side's directories. The
// state directory defaults under the user's config directory, and the
// sessions directory beside it, so one tree holds everything the participant
// owns.
func (r *rawConfig) finishSearch() (Search, error) {
	routerRaw := r.routerURL
	if routerRaw == "" {
		routerRaw = DefaultRouterURL
	}
	router, err := ParseEndpoint(routerRaw, "router.url")
	if err != nil {
		return Search{}, err
	}
	m := Search{
		RouterURL:   router,
		StateDir:    cleanDir(r.stateDir),
		SessionsDir: cleanDir(r.sessionsDir),
		TurnEnd:     r.turnEnd,
	}
	if tier := strings.ToLower(strings.TrimSpace(r.tier)); KnownSearchTier(tier) {
		m.Tier = tier
	} else if tier != "" {
		m.UnknownTier = r.tier
	}
	if m.StateDir == "" {
		if base, err := os.UserConfigDir(); err == nil {
			m.StateDir = filepath.Join(base, "jevlin", "state")
		}
	}
	if m.SessionsDir == "" && m.StateDir != "" {
		m.SessionsDir = filepath.Join(filepath.Dir(m.StateDir), "sessions")
	}
	return m, nil
}

// sameOrigin reports whether u names the same origin as the compiled URL
// def: the scheme and host compared case-insensitively, a trailing slash or
// a spelled-out default port making no difference.
func sameOrigin(u *url.URL, def string) bool {
	d, err := url.Parse(def)
	if err != nil {
		return false
	}
	port := func(x *url.URL) string {
		if p := x.Port(); p != "" {
			return p
		}
		if strings.EqualFold(x.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(u.Scheme, d.Scheme) && strings.EqualFold(u.Hostname(), d.Hostname()) && port(u) == port(d) &&
		(u.Path == "" || u.Path == "/") && (d.Path == "" || d.Path == "/")
}

// cleanDir is a directory the file names, in this system's own spelling:
// TOML has no path type, and a Windows path is most often written with
// forward slashes, which every comparison and every message would otherwise
// carry beside the backslashed paths filepath builds. Empty stays empty, so
// the default still applies.
func cleanDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
