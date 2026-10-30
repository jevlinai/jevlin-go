// Package config resolves the client's configuration exactly once at
// startup — defaults, then a TOML file (JEVLIN_CONFIG names it), then flags
// — into an immutable struct. Nothing re-reads configuration at request
// time. Unsafe states are rejected here rather than warned about: a
// routable plain-http router or platform host, which would put the
// participant's key on the wire in the clear, and a credential-carrying
// URL.
//
// The file has four tables: [[provider]], [search], [platform] and
// [update]. Unknown keys and unknown tables are refused, because a typo in
// a key this version reads is a silent misconfiguration. A file an earlier,
// mining-capable version wrote names tables this version does not know and
// is refused the same way; that version's line continues as its own binary.
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
	"strings"

	"github.com/BurntSushi/toml"
)

// defaultUpstream is the [[provider]] upstream when the file names none:
// the router. [[provider]] is the table setup writes the router into;
// search.router_url, when set, is read first.
const defaultUpstream = DefaultRouterURL

// Platform is the search platform connect talks to (agent onboarding
// design, search-platform-agent-onboarding-design.md). Both URLs are always
// resolved and validated, and nothing dials either unless connect is
// actually invoked.
//
// Two URLs, not one: search-router runs the human-facing portal and
// claim pages on one host and the machine-facing /v1/agents/* API on
// another. BaseURL is the portal — the origin a printed claim_url is
// checked against (invariant 12). AgentsAPIURL is where the actual
// register/status requests go. A single shared origin was this package's
// original assumption; live testing found the real deployment splits it
// in two, and pkg/platform.Client now takes both rather than deriving one
// from the other.
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

// Search is the [search] block: where searches go and where this
// installation keeps what the hooks and the search pass between
// themselves.
type Search struct {
	// RouterURL is where searches go. Empty means the [[provider]]
	// upstream, the table setup names the router in.
	RouterURL *url.URL
	// RouterConfigured is true when search.router_url was written in the
	// config, false when RouterURL is the provider upstream standing in for
	// it. Searches go to either. Anything that must only ever reach the
	// search router — the turn end — requires this.
	RouterConfigured bool
	// StateDir holds the installation's state: the trace key, the agent
	// registration, the connect lock and the hooks' window state. The
	// installation's own directory is its parent: credentials.json, the
	// registration journal and the claim record live there, outside every
	// directory a sandboxed agent may write. Defaults to
	// os.UserConfigDir()/jevlin/state.
	StateDir string
	// SessionsDir holds the per-workspace lineage sidecars the hooks
	// write and `search` reads. Defaults beside StateDir.
	SessionsDir string
	// TurnEnd opts this installation in to reporting, when a turn that
	// searched ends, the assistant's final message to the router. Off by
	// default: it is model-written text leaving the machine.
	TurnEnd bool
	// Tier is the search tier a request carries when neither the caller
	// (the -tier flag, the stdin request's "tier") nor the participant's
	// `jevlin agents tier` file named one: [search] tier, written by hand.
	// Empty means the request carries no tier and the router applies the
	// tenant's default. One of SearchTiers when set, lower-cased and
	// trimmed at load.
	Tier string
	// UnknownTier is a [search] tier that is not one of SearchTiers even
	// after that: kept aside so status can say why the router's default
	// applies, and never sent. A word this client cannot use does not stop
	// the config from loading, because that would stop search over a value
	// that only chooses a band.
	UnknownTier string
}

// SearchTiers is the router's tier vocabulary this client knows — the
// cost bands of search-router internal/tenant. A config naming another
// word is refused at load rather than sent, so a typo is caught here and
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

// fileConfig mirrors the TOML document.
type fileConfig struct {
	// Provider is the table setup names the router in. Its upstream is the
	// router when search.router_url is unset; name is accepted and unread.
	Provider []struct {
		Name     string `toml:"name"`
		Upstream string `toml:"upstream"`
	} `toml:"provider"`
	Search struct {
		RouterURL   string `toml:"router_url"`
		StateDir    string `toml:"state_dir"`
		SessionsDir string `toml:"sessions_dir"`
		TurnEnd     bool   `toml:"turn_end"`
		Tier        string `toml:"tier"`
	} `toml:"search"`
	Platform struct {
		BaseURL      string `toml:"base_url"`
		AgentsAPIURL string `toml:"agents_api_url"`
	} `toml:"platform"`
	Update struct {
		Mode string `toml:"mode"`
	} `toml:"update"`
}

// Load resolves the configuration from args (flags) and getenv. It returns
// showVersion=true when -version was passed. Precedence: flags > TOML file
// > defaults.
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

	raw := newRawConfigDefaults()

	// TOML file: only an explicit path (flag or env), which is required to
	// exist. A jevlin.toml in the working directory is never picked up on
	// its own: whoever wrote that directory (a cloned repo, an archive)
	// would choose where the participant's keys are sent.
	path := *flagConfig
	if path == "" {
		path = getenv("JEVLIN_CONFIG")
	}
	if path != "" {
		if err := raw.applyFile(path); err != nil {
			return nil, false, err
		}
	}

	cfg, err = raw.finish()
	if err != nil {
		return nil, false, err
	}
	return cfg, false, nil
}

// LoadBytes is finish() applied to config file bytes already in hand,
// instead of a path Load would read from disk. It exists for a caller that
// knows what a file would contain before anything writes it there —
// setup's dry run, which computes the exact bytes it would publish but (by
// design) never does. There is no -config flag to override it: that names
// a path on argv, and bytes already in memory have none.
func LoadBytes(data []byte, _ func(string) string) (*Config, error) {
	raw := newRawConfigDefaults()
	if err := raw.applyBytes("config", data); err != nil {
		return nil, err
	}
	return raw.finish()
}

// newRawConfigDefaults is rawConfig before any file or flag has been
// applied.
func newRawConfigDefaults() rawConfig {
	return rawConfig{
		upstream:        defaultUpstream,
		platformBaseURL: DefaultPlatformBaseURL,
	}
}

// rawConfig holds pre-validation values from all sources.
type rawConfig struct {
	upstream string

	routerURL   string
	stateDir    string
	sessionsDir string
	turnEnd     bool
	tier        string

	platformBaseURL string
	agentsAPIURL    string

	updateMode string
}

func (r *rawConfig) applyFile(path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the config path is operator-supplied by design
	if err != nil {
		return fmt.Errorf("config file: %w", err)
	}
	return r.applyBytes(path, data)
}

// applyBytes is applyFile's parse-and-apply step, taken alone: source names
// the bytes in an error message (a path for a real file, or a fixed label
// for bytes that only exist in memory — LoadBytes has no path to give it).
func (r *rawConfig) applyBytes(source string, data []byte) error {
	var f fileConfig
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return fmt.Errorf("config file %s: %w", source, err)
	}
	// Unknown keys are typos waiting to be silent; reject them.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return fmt.Errorf("config file %s: unknown key(s): %s", source, strings.Join(keys, ", "))
	}
	switch len(f.Provider) {
	case 0:
	case 1:
		if p := f.Provider[0]; p.Upstream != "" {
			r.upstream = p.Upstream
		}
	default:
		return errors.New("config: exactly one [[provider]] is supported")
	}
	r.turnEnd = f.Search.TurnEnd
	r.tier = f.Search.Tier
	if f.Search.RouterURL != "" {
		r.routerURL = f.Search.RouterURL
	}
	if f.Search.StateDir != "" {
		r.stateDir = f.Search.StateDir
	}
	if f.Search.SessionsDir != "" {
		r.sessionsDir = f.Search.SessionsDir
	}
	if f.Platform.BaseURL != "" {
		r.platformBaseURL = f.Platform.BaseURL
	}
	if f.Platform.AgentsAPIURL != "" {
		r.agentsAPIURL = f.Platform.AgentsAPIURL
	}
	if f.Update.Mode != "" {
		r.updateMode = f.Update.Mode
	}
	return nil
}

func (r *rawConfig) finish() (*Config, error) {
	upstream, err := parseUpstream(r.upstream)
	if err != nil {
		return nil, fmt.Errorf("provider.upstream: %w", err)
	}
	search, err := r.finishSearch(upstream)
	if err != nil {
		return nil, err
	}
	platformBaseURL, err := parsePlatformURL(r.platformBaseURL, "platform.base_url")
	if err != nil {
		return nil, err
	}
	// agents_api_url used to default unconditionally to the real platform,
	// so a dev/test config naming only a loopback base_url (every existing
	// stub-backed test does exactly this) would silently register against
	// production the moment connect actually ran — which is exactly what
	// happened once, live, before this fix. Three cases, only two of which
	// have a safe implicit answer:
	//   - base_url is exactly the real platform's default: agents_api_url
	//     defaults to the real agents API. The ordinary case. The pair the
	//     platform had before jevlin.ai is implied the same way, so a
	//     config written against it keeps loading.
	//   - base_url is loopback: assume the same local stub serves both
	//     roles, matching every existing test's own setup.
	//   - base_url names anything else (a devnet, a staging portal) and
	//     agents_api_url is unset: refused. A custom non-loopback base_url
	//     gives no safe signal about which API host it pairs with —
	//     defaulting to production here is exactly the footgun this whole
	//     fix exists to close, just for a devnet instead of a laptop.
	agentsAPIRaw := r.agentsAPIURL
	if agentsAPIRaw == "" {
		switch platformBaseURL {
		case DefaultPlatformBaseURL:
			agentsAPIRaw = DefaultAgentsAPIURL
		case LegacyPlatformBaseURL:
			agentsAPIRaw = LegacyAgentsAPIURL
		default:
			pu, perr := url.Parse(platformBaseURL)
			if perr != nil || !isLoopbackHost(pu.Hostname()) {
				return nil, errors.New("platform.agents_api_url: required when platform.base_url names anything other " +
					"than the default platform — a devnet or staging deployment must name both")
			}
			agentsAPIRaw = platformBaseURL
		}
	}
	agentsAPIURL, err := parsePlatformURL(agentsAPIRaw, "platform.agents_api_url")
	if err != nil {
		return nil, err
	}

	return &Config{
		Search:   search,
		Platform: Platform{BaseURL: platformBaseURL, AgentsAPIURL: agentsAPIURL},
		Update:   r.finishUpdate(),
	}, nil
}

// finishUpdate resolves the [update] block the way [search] tier is resolved:
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

// parsePlatformURL validates a [platform] URL (base_url or agents_api_url)
// the same way search.router_url is (invariant 5): https, or http only for
// loopback — connect sends the platform-issued sr- key in Authorization to
// agents_api_url (base_url never carries a credential, only a printed
// claim_url is checked against it, but the same posture costs nothing to
// hold for both). Both always resolve, since nothing dials either unless
// connect actually runs, and the defaults are always valid https URLs.
func parsePlatformURL(raw, field string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%s: must be an absolute http(s) URL", field)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s: must not carry userinfo", field)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("%s: plain http is permitted only for loopback hosts, got %q — "+
			"use https, or a loopback address for local development", field, u.Host)
	}
	return raw, nil
}

// finishSearch resolves the [search] block. The state directory defaults
// under the user's config directory, and the sessions directory beside it,
// so one tree holds everything the participant owns; the router defaults
// to the provider upstream so a config that names the router there is a
// config here by adding nothing.
func (r *rawConfig) finishSearch(upstream *url.URL) (Search, error) {
	m := Search{
		StateDir:    r.stateDir,
		SessionsDir: r.sessionsDir,
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
	if r.routerURL != "" {
		m.RouterConfigured = true
		u, err := url.Parse(r.routerURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return Search{}, errors.New("search.router_url: must be an absolute http(s) URL")
		}
		if u.User != nil {
			return Search{}, errors.New("search.router_url: must not carry userinfo")
		}
		// Every search sends the participant's sr- key in Authorization,
		// so a routable plain-http router is a cleartext-credential
		// exposure. The loopback carve-out is deliberate, for local
		// development without TLS; testing against a ROUTABLE devnet
		// router needs TLS on the devnet, or the client co-located over
		// loopback — that is the intended consequence, not a gap.
		if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
			return Search{}, fmt.Errorf("search.router_url: plain http is permitted only for loopback hosts, got %q — "+
				"use https, or a loopback address for local development", u.Host)
		}
		m.RouterURL = u
	} else {
		m.RouterURL = upstream
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

// parseUpstream validates the upstream as an origin plus fixed base path — not
// a general URL. A credential smuggled in as https://user:pass@host would
// become an Authorization header the client generates; a query or fragment
// has no business in a base; and anything but https would put the bearer
// credential on the wire in the clear.
func parseUpstream(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", s, err)
	}
	switch {
	case u.Scheme != "https":
		return nil, fmt.Errorf("scheme must be https, got %q", u.Scheme)
	case u.User != nil:
		return nil, errors.New("must not carry userinfo")
	case u.RawQuery != "":
		return nil, errors.New("must not carry a query")
	case u.Fragment != "" || u.RawFragment != "":
		return nil, errors.New("must not carry a fragment")
	case u.Host == "":
		return nil, errors.New("missing host")
	case u.RawPath != "":
		return nil, errors.New("path must not be percent-encoded")
	case strings.HasSuffix(u.Path, "/"):
		return nil, errors.New("base path must not end with a slash")
	case u.Opaque != "":
		return nil, errors.New("must be an absolute URL with a host")
	}
	return u, nil
}
