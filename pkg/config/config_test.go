package config

// Configuration format 1. The file begins `schema = 1` and names every
// endpoint in its own section; anything else — no schema line, an older
// file's sections, a key format 1 does not define — is refused with one
// message and never reinterpreted (config.go). Every test here drives Load
// or LoadBytes, never a decoder on its own, so what is held is what a
// command sees.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// legacyMessage is the one sentence every file from before format 1 gets.
const legacyMessage = "this configuration is from an older jevlin; run `jevlin setup`"

// earlierConfig is the shape every version before format 1 wrote: a
// [[provider]] block naming the router, the [platform] pair, and tables this
// client has no reading of.
const earlierConfig = `[[provider]]
name = "search-router"
upstream = "https://api.jevlin.ai"

[platform]
base_url = "https://platform.jevlin.ai"
agents_api_url = "https://agents-v1.jevlin.ai"

[other]
enabled = true
state_dir = "/tmp/state"
interval = "3m"
`

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, args []string, getenv func(string) string) *Config {
	t.Helper()
	cfg, showVersion, err := Load(args, getenv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if showVersion {
		t.Fatal("unexpected showVersion")
	}
	return cfg
}

func loadFile(t *testing.T, body string, getenv func(string) string) (*Config, error) {
	t.Helper()
	cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, getenv)
	return cfg, err
}

func mustLoadFile(t *testing.T, body string, getenv func(string) string) *Config {
	t.Helper()
	cfg, err := loadFile(t, body, getenv)
	if err != nil {
		t.Fatalf("Load(%q): %v", body, err)
	}
	return cfg
}

// defaultDirs is where [search] state_dir and sessions_dir land when the
// file names neither: under the user's config directory, side by side.
func defaultDirs(t *testing.T) (state, sessions string) {
	t.Helper()
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir here: %v", err)
	}
	return filepath.Join(base, "jevlin", "state"), filepath.Join(base, "jevlin", "sessions")
}

// assertDefaults holds cfg to the compiled defaults in every field.
func assertDefaults(t *testing.T, name string, cfg *Config) {
	t.Helper()
	if cfg.Search.RouterURL == nil || cfg.Search.RouterURL.String() != DefaultRouterURL {
		t.Errorf("%s: router %v, want %q", name, cfg.Search.RouterURL, DefaultRouterURL)
	}
	if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
		t.Errorf("%s: platform %q agents %q", name, cfg.Platform.BaseURL, cfg.Platform.AgentsAPIURL)
	}
	if cfg.Search.TurnEnd || cfg.Search.Tier != "" || cfg.Search.UnknownTier != "" {
		t.Errorf("%s: search %+v, want turn_end off and no tier", name, cfg.Search)
	}
	if cfg.Update.Mode != UpdateApply || cfg.Update.UnknownMode != "" {
		t.Errorf("%s: update %+v, want apply", name, cfg.Update)
	}
	state, sessions := defaultDirs(t)
	if cfg.Search.StateDir != state || cfg.Search.SessionsDir != sessions {
		t.Errorf("%s: dirs %q %q, want %q %q", name, cfg.Search.StateDir, cfg.Search.SessionsDir, state, sessions)
	}
}

// sameConfig reports whether two loaded configurations resolve to the same
// values. RouterURL is a pointer, so it is compared by its text.
func sameConfig(a, b *Config) bool {
	if (a.Search.RouterURL == nil) != (b.Search.RouterURL == nil) {
		return false
	}
	if a.Search.RouterURL != nil && a.Search.RouterURL.String() != b.Search.RouterURL.String() {
		return false
	}
	as, bs := a.Search, b.Search
	as.RouterURL, bs.RouterURL = nil, nil
	return as == bs && a.Platform == b.Platform && a.Update == b.Update
}

// The compiled defaults are the whole configuration when no file is named,
// and a file holding only the schema line, or the schema line and empty
// sections, means the same thing.
func TestNoFileAndAMinimalFileAreTheCompiledDefaults(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"no file":        load(t, nil, noEnv),
		"schema only":    mustLoadFile(t, "schema = 1\n", noEnv),
		"empty sections": mustLoadFile(t, "schema = 1\n[router]\n[platform]\n[agents]\n[search]\n[update]\n", noEnv),
		"comments too":   mustLoadFile(t, "# written by hand\nschema = 1 # format 1\n", noEnv),
	} {
		assertDefaults(t, name, cfg)
	}
}

// Every file with no schema line is refused as ErrLegacyConfig with the one
// message, whatever else it holds: a 0.6.1 file, a lone [[provider]] block,
// an empty file, a comment, and format 1's own sections written without the
// schema line. It is never called an unknown key: the sections an older
// file holds are not this format's to name, and this is the one refusal
// setup may answer by setting the file aside.
func TestAFileWithNoSchemaLineIsLegacyWhateverElseItHolds(t *testing.T) {
	for name, body := range map[string]string{
		"empty":                         "",
		"comments only":                 "# nothing here\n",
		"an earlier version's file":     earlierConfig,
		"a lone provider block":         "[[provider]]\nname = \"search-router\"\nupstream = \"https://api.jevlin.ai\"\n",
		"a table this client never had": "[other]\nenabled = true\nrouter_url = \"https://api.jevlin.ai\"\n",
		"the old [search] router_url":   "[search]\nrouter_url = \"https://api.jevlin.ai\"\n",
		"the old [platform] pair":       "[platform]\nbase_url = \"https://platform.jevlin.ai\"\nagents_api_url = \"https://agents-v1.jevlin.ai\"\n",
		"format 1 sections, no line":    "[router]\nurl = \"https://api.jevlin.ai\"\n\n[search]\nturn_end = true\n",
		"a wrong type, no line":         "[search]\nturn_end = \"yes\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadFile(t, body, noEnv)
			if !errors.Is(err, ErrLegacyConfig) {
				t.Fatalf("err = %v, want ErrLegacyConfig", err)
			}
			if !strings.Contains(err.Error(), legacyMessage) {
				t.Errorf("the message does not say what to do: %v", err)
			}
			if strings.Contains(err.Error(), "does not define") {
				t.Errorf("an older file was called a format-1 file with a typo: %v", err)
			}
		})
	}
}

// A file that is not TOML is neither older nor format 1: it is refused as
// what it is, so setup does not set aside a file it could not read.
func TestAFileThatIsNotTOMLIsNotCalledOlder(t *testing.T) {
	for _, body := range []string{"schema = \n", "[router\nurl = 1\n", "schema = 1\n[router]\nurl = \"unterminated\n"} {
		_, err := loadFile(t, body, noEnv)
		if err == nil || errors.Is(err, ErrLegacyConfig) {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// A file that says it is format 1 is the participant's current
// configuration: a key the format does not define is refused by name, with
// what to do, and is not called older — so setup never sets a file aside
// over a typo or a setting carried over by hand from an older file.
func TestAnUnknownKeyInAFormatOneFileIsNamedNotCalledOlder(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		keys []string
	}{
		"a typo in a section":           {"schema = 1\n[router]\nurl = \"https://api.jevlin.ai\"\nshade = \"blue\"\n", []string{"router.shade"}},
		"a typo at the top":             {"schema = 1\nschemas = 2\n", []string{"schemas"}},
		"the old router key":            {"schema = 1\n[search]\nrouter_url = \"https://api.jevlin.ai\"\n", []string{"search.router_url"}},
		"the old tier key":              {"schema = 1\n[search]\ntier = \"max\"\n", []string{"search.tier"}},
		"the old portal key":            {"schema = 1\n[platform]\nbase_url = \"https://platform.jevlin.ai\"\n", []string{"platform.base_url"}},
		"the old agents key":            {"schema = 1\n[platform]\nagents_api_url = \"https://agents-v1.jevlin.ai\"\n", []string{"platform.agents_api_url"}},
		"a proxy-era section":           {"schema = 1\n[proxy]\nlisten = \"127.0.0.1:8787\"\n", []string{"proxy"}},
		"a provider block":              {"schema = 1\n[[provider]]\nname = \"search-router\"\nupstream = \"https://api.jevlin.ai\"\n", []string{"provider"}},
		"a table this format never had": {"schema = 1\n[other]\nenabled = true\n", []string{"other"}},
		"every unknown key, named":      {"schema = 1\n[search]\nshade = \"blue\"\nenabled = true\ndepth = 3\n", []string{"search.depth", "search.enabled", "search.shade"}},
		"an unknown key beside format 1 keys": {
			"schema = 1\n[router]\nurl = \"https://api.jevlin.ai\"\n[search]\nstate_dir = \"/tmp/state\"\nextra = \"https://x.example\"\n",
			[]string{"search.extra"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadFile(t, tc.body, noEnv)
			if err == nil {
				t.Fatal("loaded")
			}
			if errors.Is(err, ErrLegacyConfig) {
				t.Fatalf("called older: %v", err)
			}
			if !strings.Contains(err.Error(), "format 1 does not define ") || !strings.Contains(err.Error(), "; correct or remove it") {
				t.Errorf("the refusal does not say what to do: %v", err)
			}
			for _, key := range tc.keys {
				if !strings.Contains(err.Error(), key) {
					t.Errorf("the refusal does not name %s: %v", key, err)
				}
			}
			// What it names is the unknown key, not a section format 1 has.
			if strings.Contains(err.Error(), "does not define router,") || strings.Contains(err.Error(), "does not define search,") {
				t.Errorf("a format-1 section was named as unknown: %v", err)
			}
		})
	}
}

// A value of the wrong type in a key format 1 defines is this file's
// mistake: refused, not called older and not called an unknown key.
func TestAWrongValueTypeIsNotCalledAnOlderFile(t *testing.T) {
	for _, body := range []string{
		"schema = 1\n[search]\nturn_end = \"yes\"\n",
		"schema = 1\n[router]\nurl = 3\n",
		"schema = 1\n[search]\nstate_dir = 1\n",
		"schema = 1\n[update]\nmode = true\n",
		"schema = 1\n[router]\ntier = [\"max\"]\n",
	} {
		_, err := loadFile(t, body, noEnv)
		if err == nil {
			t.Errorf("%q loaded", body)
			continue
		}
		if errors.Is(err, ErrLegacyConfig) || strings.Contains(err.Error(), "does not define") {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// A schema this client does not read is refused with its own message, and
// is not called older: it may be newer, and its keys are not this format's
// to call unknown. The schema is judged before anything else in the file.
func TestAnotherSchemaNumberIsRefusedAsNotThisFormat(t *testing.T) {
	for _, body := range []string{
		"schema = 2\n",
		"schema = 2\n[newer]\nkey = 1\n",
		"schema = 2\n[router]\nurl = \"https://api.jevlin.ai\"\n",
		"schema = 0\n",
		"schema = -1\n",
		"schema = \"1\"\n",
		"schema = 1.0\n",
		"schema = true\n",
	} {
		_, err := loadFile(t, body, noEnv)
		if err == nil {
			t.Errorf("%q loaded", body)
			continue
		}
		if errors.Is(err, ErrLegacyConfig) || strings.Contains(err.Error(), "does not define") {
			t.Errorf("%q: %v", body, err)
		}
		if !strings.Contains(err.Error(), "reads schema = 1") || !strings.Contains(err.Error(), "write schema = 1") {
			t.Errorf("%q: the refusal does not name the schema this client reads: %v", body, err)
		}
	}
}

func TestAnExplicitConfigMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	if _, _, err := Load([]string{"-config", missing}, noEnv); err == nil {
		t.Fatal("a missing -config file loaded")
	}
	if _, _, err := Load(nil, envOf(map[string]string{"JEVLIN_CONFIG": missing})); err == nil {
		t.Fatal("a missing JEVLIN_CONFIG file loaded")
	}
}

// JEVLIN_CONFIG names the file when -config does not, and -config wins.
func TestJEVLINCONFIGSelectsTheFile(t *testing.T) {
	a := writeTOML(t, "schema = 1\n[router]\nurl = \"https://a.example\"\n")
	b := writeTOML(t, "schema = 1\n[router]\nurl = \"https://b.example\"\n")
	env := envOf(map[string]string{"JEVLIN_CONFIG": a})
	if got := load(t, nil, env).Search.RouterURL.Host; got != "a.example" {
		t.Errorf("JEVLIN_CONFIG: router %q", got)
	}
	if got := load(t, []string{"-config", b}, env).Search.RouterURL.Host; got != "b.example" {
		t.Errorf("-config over JEVLIN_CONFIG: router %q", got)
	}
}

// The three endpoint variables apply at runtime, after the file, each
// replacing only its own URL, under the file's own URL rules — with a file
// and with none.
func TestTheEndpointVariablesApplyAtRuntimeUnderTheSameRules(t *testing.T) {
	body := `schema = 1
[router]
url = "https://router.file.example"
[platform]
url = "https://portal.file.example"
[agents]
url = "https://agents.file.example"
`
	cfg := mustLoadFile(t, body, envOf(map[string]string{
		EnvRouterURL:    "https://router.env.example",
		EnvPlatformURL:  "https://portal.env.example",
		EnvAgentsAPIURL: "https://agents.env.example",
	}))
	if cfg.Search.RouterURL.String() != "https://router.env.example" || cfg.Platform.BaseURL != "https://portal.env.example" ||
		cfg.Platform.AgentsAPIURL != "https://agents.env.example" {
		t.Fatalf("the variables did not win over the file: %v %+v", cfg.Search.RouterURL, cfg.Platform)
	}
	// One variable replaces one URL; the file keeps the other two.
	cfg = mustLoadFile(t, body, envOf(map[string]string{EnvRouterURL: "http://127.0.0.1:9"}))
	if cfg.Search.RouterURL.String() != "http://127.0.0.1:9" || cfg.Platform.BaseURL != "https://portal.file.example" ||
		cfg.Platform.AgentsAPIURL != "https://agents.file.example" {
		t.Fatalf("JEVLIN_ROUTER_URL alone: %v %+v", cfg.Search.RouterURL, cfg.Platform)
	}
	// With no file at all.
	cfg = load(t, nil, envOf(map[string]string{
		EnvRouterURL:    "http://127.0.0.1:9",
		EnvPlatformURL:  "http://localhost:10",
		EnvAgentsAPIURL: "http://[::1]:11",
	}))
	if cfg.Search.RouterURL.String() != "http://127.0.0.1:9" || cfg.Platform.BaseURL != "http://localhost:10" ||
		cfg.Platform.AgentsAPIURL != "http://[::1]:11" {
		t.Fatalf("the variables with no file: %v %+v", cfg.Search.RouterURL, cfg.Platform)
	}
	// An empty variable is not set: the file's value stays.
	cfg = mustLoadFile(t, body, envOf(map[string]string{EnvRouterURL: ""}))
	if cfg.Search.RouterURL.String() != "https://router.file.example" {
		t.Errorf("an empty JEVLIN_ROUTER_URL replaced the file's router: %v", cfg.Search.RouterURL)
	}
	// Each is held to ParseEndpoint's rules, and the refusal names the
	// key the variable stands for.
	for name, field := range map[string]string{EnvRouterURL: "router.url", EnvPlatformURL: "platform.url", EnvAgentsAPIURL: "agents.url"} {
		for _, bad := range []string{"http://router.example", "https://user:pw@router.example", "ftp://router.example", "https://router.example/?q=1", "https://router.example/#x", "https://apİ.example"} {
			env := map[string]string{name: bad}
			if name == EnvPlatformURL {
				env[EnvAgentsAPIURL] = "https://agents.example"
			}
			for _, args := range [][]string{nil, {"-config", writeTOML(t, body)}} {
				_, _, err := Load(args, envOf(env))
				if err == nil {
					t.Errorf("%s=%q loaded (args %q)", name, bad, args)
					continue
				}
				if !strings.Contains(err.Error(), field) {
					t.Errorf("%s=%q: the refusal does not name %s: %v", name, bad, field, err)
				}
			}
		}
	}
}

// Every endpoint — [router] url, [platform] url, [agents] url — is held to
// ParseEndpoint's rules (invariant 5): an absolute http(s) URL with a plain
// ASCII host, https unless the host is loopback, and no userinfo, query or
// fragment. The refusal names the key and the rule it broke.
func TestEveryEndpointIsHeldToParseEndpointsRules(t *testing.T) {
	bad := map[string]string{
		"http://host.example":         "loopback",
		"http://203.0.113.10":         "loopback",
		"http://router.internal:8080": "loopback",
		"https://u:p@host.example":    "userinfo",
		"https://host.example/?q=1":   "query or fragment",
		"https://host.example/?":      "query or fragment",
		"https://host.example/#x":     "query or fragment",
		"https://host.example/#":      "query or fragment",
		"https://apİ.example":         "plain ASCII",
		"https://ap%C4%B0.example":    "plain ASCII",
		"https://host example":        "absolute http(s) URL",
		"not a url":                   "absolute http(s) URL",
		"ftp://host.example":          "absolute http(s) URL",
		"https:///path":               "absolute http(s) URL",
		"//host.example":              "absolute http(s) URL",
	}
	good := []string{"https://host.example", "https://host.example/base/", "http://127.0.0.1:8080", "http://localhost:1", "http://[::1]:2", "https://xn--jevlin-tyd.example"}
	for _, section := range []string{"router", "platform", "agents"} {
		field := section + ".url"
		for raw, rule := range bad {
			body := "schema = 1\n[" + section + "]\nurl = \"" + raw + "\"\n"
			if section == "platform" {
				body += "[agents]\nurl = \"https://agents.example\"\n"
			}
			_, err := loadFile(t, body, noEnv)
			if err == nil {
				t.Errorf("%s = %q loaded", field, raw)
				continue
			}
			if !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), rule) {
				t.Errorf("%s = %q: the refusal names neither the key nor the rule (%q): %v", field, raw, rule, err)
			}
			if _, err := ParseEndpoint(raw, field); err == nil || !strings.Contains(err.Error(), field) {
				t.Errorf("ParseEndpoint(%q, %s): %v", raw, field, err)
			}
		}
		for _, raw := range good {
			body := "schema = 1\n[" + section + "]\nurl = \"" + raw + "\"\n"
			if section == "platform" {
				body += "[agents]\nurl = \"https://agents.example\"\n"
			}
			if _, err := loadFile(t, body, noEnv); err != nil {
				t.Errorf("%s = %q refused: %v", field, raw, err)
			}
			if u, err := ParseEndpoint(raw, field); err != nil || u.String() != raw {
				t.Errorf("ParseEndpoint(%q): %v %v", raw, u, err)
			}
		}
	}
}

// [agents] url pairs with [platform] url: the default portal implies the
// default agents API; a loopback portal implies itself, since one local
// stub serves both; and a custom routable portal with no agents API named
// is refused — pairing it with production would send a devnet's
// registrations, and the key they mint, to production, which happened once
// live before this rule. The retired portal is such a custom portal now:
// this client carries no successor for it.
func TestTheAgentsAPIPairsWithThePortal(t *testing.T) {
	for name, body := range map[string]string{
		"nothing named":            "schema = 1\n",
		"the default, by name":     "schema = 1\n[platform]\nurl = \"" + DefaultPlatformBaseURL + "\"\n",
		"the default, via the env": "",
	} {
		var cfg *Config
		if body == "" {
			cfg = load(t, nil, envOf(map[string]string{EnvPlatformURL: DefaultPlatformBaseURL}))
		} else {
			cfg = mustLoadFile(t, body, noEnv)
		}
		if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
			t.Errorf("%s: %+v", name, cfg.Platform)
		}
	}
	if DefaultPlatformBaseURL == DefaultAgentsAPIURL {
		t.Fatal("the portal and the agents API defaulted to one host; they are two")
	}

	cfg := mustLoadFile(t, "schema = 1\n[platform]\nurl = \"http://127.0.0.1:9000\"\n", noEnv)
	if cfg.Platform.AgentsAPIURL != "http://127.0.0.1:9000" {
		t.Errorf("a loopback portal implies itself as the agents API, not production: %q", cfg.Platform.AgentsAPIURL)
	}
	cfg = load(t, nil, envOf(map[string]string{EnvPlatformURL: "http://localhost:9000"}))
	if cfg.Platform.AgentsAPIURL != "http://localhost:9000" {
		t.Errorf("a loopback portal from the environment implies itself: %q", cfg.Platform.AgentsAPIURL)
	}
	cfg = mustLoadFile(t, "schema = 1\n[platform]\nurl = \"http://127.0.0.1:9000\"\n[agents]\nurl = \"http://127.0.0.1:9001\"\n", noEnv)
	if cfg.Platform.AgentsAPIURL != "http://127.0.0.1:9001" {
		t.Errorf("a named agents API wins over the loopback pairing: %q", cfg.Platform.AgentsAPIURL)
	}
	cfg = mustLoadFile(t, "schema = 1\n[platform]\nurl = \"https://portal.staging.example\"\n[agents]\nurl = \"https://agents.staging.example\"\n", noEnv)
	if cfg.Platform.BaseURL != "https://portal.staging.example" || cfg.Platform.AgentsAPIURL != "https://agents.staging.example" {
		t.Errorf("both named: %+v", cfg.Platform)
	}
	cfg = mustLoadFile(t, "schema = 1\n[agents]\nurl = \"https://agents.staging.example\"\n", noEnv)
	if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != "https://agents.staging.example" {
		t.Errorf("the agents API alone leaves the portal default: %+v", cfg.Platform)
	}

	for name, run := range map[string]func() error{
		"a custom portal in the file": func() error {
			_, err := loadFile(t, "schema = 1\n[platform]\nurl = \"https://portal.staging.example\"\n", noEnv)
			return err
		},
		"the retired portal in the file": func() error {
			_, err := loadFile(t, "schema = 1\n[platform]\nurl = \"https://platform.nyks.dev\"\n", noEnv)
			return err
		},
		"a custom portal from the environment": func() error {
			_, _, err := Load(nil, envOf(map[string]string{EnvPlatformURL: "https://portal.staging.example"}))
			return err
		},
		"a custom portal over a file naming neither": func() error {
			_, err := loadFile(t, "schema = 1\n[router]\nurl = \"https://api.jevlin.ai\"\n", envOf(map[string]string{EnvPlatformURL: "https://portal.staging.example"}))
			return err
		},
	} {
		err := run()
		if err == nil {
			t.Errorf("%s: loaded with no agents API named, silently paired with production", name)
			continue
		}
		if !strings.Contains(err.Error(), "agents.url") {
			t.Errorf("%s: the refusal does not name the missing key: %v", name, err)
		}
	}
}

// [search] state_dir defaults under the user's config directory and
// sessions_dir beside it; a named state_dir moves the sessions beside it;
// a named sessions_dir wins outright.
func TestSearchDirectoriesDefaultUnderTheUserConfigDirAndANamedOneWins(t *testing.T) {
	state, sessions := defaultDirs(t)
	for name, cfg := range map[string]*Config{
		"no file":           load(t, nil, noEnv),
		"schema only":       mustLoadFile(t, "schema = 1\n", noEnv),
		"an empty [search]": mustLoadFile(t, "schema = 1\n[search]\n", noEnv),
	} {
		if cfg.Search.StateDir != state || cfg.Search.SessionsDir != sessions {
			t.Errorf("%s: state %q sessions %q, want %q %q", name, cfg.Search.StateDir, cfg.Search.SessionsDir, state, sessions)
		}
	}
	home := filepath.ToSlash(t.TempDir())
	cfg := mustLoadFile(t, "schema = 1\n[search]\nstate_dir = \""+home+"/state\"\n", noEnv)
	if cfg.Search.StateDir != filepath.FromSlash(home+"/state") {
		t.Errorf("state dir: %q", cfg.Search.StateDir)
	}
	if cfg.Search.SessionsDir != filepath.Join(filepath.FromSlash(home), "sessions") {
		t.Errorf("sessions dir: %q, want beside the state dir", cfg.Search.SessionsDir)
	}
	cfg = mustLoadFile(t, "schema = 1\n[search]\nstate_dir = \""+home+"/state\"\nsessions_dir = \""+home+"/elsewhere\"\n", noEnv)
	if cfg.Search.StateDir != filepath.FromSlash(home+"/state") || cfg.Search.SessionsDir != filepath.FromSlash(home+"/elsewhere") {
		t.Errorf("both named: state %q sessions %q", cfg.Search.StateDir, cfg.Search.SessionsDir)
	}
	cfg = mustLoadFile(t, "schema = 1\n[search]\nsessions_dir = \""+home+"/elsewhere\"\n", noEnv)
	if cfg.Search.StateDir != state || cfg.Search.SessionsDir != filepath.FromSlash(home+"/elsewhere") {
		t.Errorf("sessions alone: state %q sessions %q", cfg.Search.StateDir, cfg.Search.SessionsDir)
	}
}

// [search] turn_end is off unless the file says otherwise: it sends
// model-written text off the machine, and a default must not.
func TestSearchTurnEndIsOffByDefault(t *testing.T) {
	if load(t, nil, noEnv).Search.TurnEnd {
		t.Error("no file: turn_end on")
	}
	for body, want := range map[string]bool{
		"schema = 1\n":                             false,
		"schema = 1\n[search]\n":                   false,
		"schema = 1\n[search]\nturn_end = false\n": false,
		"schema = 1\n[search]\nturn_end = true\n":  true,
		"schema = 1\n[router]\nurl = \"http://127.0.0.1:8080\"\n[search]\nturn_end = true\n": true,
	} {
		if got := mustLoadFile(t, body, noEnv).Search.TurnEnd; got != want {
			t.Errorf("%q: turn_end = %v, want %v", body, got, want)
		}
	}
}

// [router] tier is the participant's own setting, written by hand, and a
// word the client cannot use must not take the config down with it: a
// failed load stops search over a value that only chooses a band. Case
// and surrounding space are forgiven, and a word that is still not a band
// loads as no tier, kept aside for status to name, and is never sent.
func TestRouterTierIsNormalizedAndAnUnknownWordIsKeptAside(t *testing.T) {
	for raw, want := range map[string]string{"": "", "fast": "fast", "balanced": "balanced", "max": "max", "Fast": "fast", "BALANCED": "balanced", " max ": "max"} {
		body := "schema = 1\n[router]\nurl = \"http://127.0.0.1:8080\"\n"
		if raw != "" {
			body += "tier = \"" + raw + "\"\n"
		}
		cfg := mustLoadFile(t, body, noEnv)
		if cfg.Search.Tier != want || cfg.Search.UnknownTier != "" {
			t.Errorf("tier %q: Tier %q UnknownTier %q, want %q and none", raw, cfg.Search.Tier, cfg.Search.UnknownTier, want)
		}
		if want != "" && !KnownSearchTier(want) {
			t.Errorf("tier %q resolved to %q, which KnownSearchTier does not know", raw, want)
		}
	}
	for _, unknown := range []string{"turbo", "auto", "max; echo x", " "} {
		cfg, err := loadFile(t, "schema = 1\n[router]\ntier = \""+unknown+"\"\n", noEnv)
		if err != nil {
			t.Errorf("tier %q stopped the config from loading: %v", unknown, err)
			continue
		}
		wantAside := unknown
		if strings.TrimSpace(unknown) == "" {
			wantAside = "" // blank is no tier, not a word to keep
		}
		if cfg.Search.Tier != "" || cfg.Search.UnknownTier != wantAside {
			t.Errorf("tier %q: Tier %q UnknownTier %q, want none and %q kept aside", unknown, cfg.Search.Tier, cfg.Search.UnknownTier, wantAside)
		}
	}
}

// [update] mode: absent is apply, the three words are normalized, and a
// word this version cannot name falls to notify — never off, which would
// silence the check by a typo, and never apply, which would replace a
// binary on a word nobody chose — with the word kept aside for doctor.
func TestUpdateModeDefaultsToApplyAndAnUnknownWordFallsToNotify(t *testing.T) {
	if got := load(t, nil, noEnv).Update; got.Mode != UpdateApply || got.UnknownMode != "" {
		t.Errorf("no file: %+v", got)
	}
	for raw, want := range map[string]string{"": UpdateApply, "apply": UpdateApply, "notify": UpdateNotify, "off": UpdateOff, "Off": UpdateOff, "NOTIFY": UpdateNotify, " apply ": UpdateApply} {
		body := "schema = 1\n"
		if raw != "" {
			body += "[update]\nmode = \"" + raw + "\"\n"
		}
		cfg := mustLoadFile(t, body, noEnv)
		if cfg.Update.Mode != want || cfg.Update.UnknownMode != "" {
			t.Errorf("mode %q: %+v, want %q and none kept aside", raw, cfg.Update, want)
		}
	}
	for _, unknown := range []string{"auto", "of", "apply; echo x"} {
		cfg, err := loadFile(t, "schema = 1\n[update]\nmode = \""+unknown+"\"\n", noEnv)
		if err != nil {
			t.Errorf("mode %q stopped the config from loading: %v", unknown, err)
			continue
		}
		if cfg.Update.Mode != UpdateNotify || cfg.Update.UnknownMode != unknown {
			t.Errorf("mode %q: %+v, want notify and the word kept aside", unknown, cfg.Update)
		}
	}
}

func TestVersionFlagAndUnexpectedArguments(t *testing.T) {
	cfg, show, err := Load([]string{"-version"}, noEnv)
	if err != nil || !show || cfg != nil {
		t.Errorf("-version: show %v cfg %v err %v", show, cfg, err)
	}
	for _, args := range [][]string{{"stray"}, {"-config", writeTOML(t, "schema = 1\n"), "stray"}, {"-no-such-flag"}} {
		cfg, show, err := Load(args, noEnv)
		if err == nil || show || cfg != nil {
			t.Errorf("%q: show %v cfg %v err %v, want a refusal", args, show, cfg, err)
		}
	}
	if _, _, err := Load([]string{"-config", writeTOML(t, "schema = 1\n"), "-version"}, noEnv); err != nil {
		t.Errorf("-version after -config: %v", err)
	}
}

// A jevlin.toml in the working directory is never loaded on its own:
// whoever wrote that directory (a cloned repo, an archive) would choose
// where the participant's keys are sent. Neither a format-1 file naming
// another router nor an older file that would fail to load is read.
func TestLoadIgnoresJevlinTOMLInWorkingDirectory(t *testing.T) {
	for name, body := range map[string]string{
		"a format-1 file": "schema = 1\n[router]\nurl = \"https://evil.example\"\n[agents]\nurl = \"https://evil.example\"\n",
		"an older file":   earlierConfig,
		"not TOML":        "schema = \n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "jevlin.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		cfg, _, err := Load(nil, noEnv)
		if err != nil {
			t.Fatalf("%s: a cwd jevlin.toml was read: %v", name, err)
		}
		assertDefaults(t, name, cfg)
	}
}

// LoadBytes is Load applied to bytes already in hand: the same bytes
// resolve to the same configuration, the same environment applies after
// them, and what Load refuses LoadBytes refuses for the same reason.
func TestLoadBytesIsLoadOverBytes(t *testing.T) {
	full := `schema = 1
[router]
url = "http://127.0.0.1:8080"
tier = "Max"
[platform]
url = "http://127.0.0.1:9000"
[search]
state_dir = "/tmp/jevlin/state"
turn_end = true
[update]
mode = "notify"
`
	for name, body := range map[string]string{"schema only": "schema = 1\n", "every section": full} {
		for envName, env := range map[string]func(string) string{"no env": noEnv, "the router from the env": envOf(map[string]string{EnvRouterURL: "https://r.example"})} {
			fromFile := mustLoadFile(t, body, env)
			fromBytes, err := LoadBytes([]byte(body), env)
			if err != nil {
				t.Fatalf("%s, %s: LoadBytes: %v", name, envName, err)
			}
			if !sameConfig(fromFile, fromBytes) {
				t.Errorf("%s, %s: Load %+v %v, LoadBytes %+v %v", name, envName, *fromFile, fromFile.Search.RouterURL, *fromBytes, fromBytes.Search.RouterURL)
			}
		}
	}
	cfg, err := LoadBytes([]byte(full), envOf(map[string]string{EnvRouterURL: "https://r.example"}))
	if err != nil || cfg.Search.RouterURL.String() != "https://r.example" || cfg.Search.Tier != "max" || !cfg.Search.TurnEnd || cfg.Update.Mode != UpdateNotify {
		t.Errorf("LoadBytes with the environment: %+v %v %v", cfg, cfg.Search.RouterURL, err)
	}
	for name, body := range map[string]string{
		"an older file":   earlierConfig,
		"an unknown key":  "schema = 1\n[search]\nrouter_url = \"https://r.example\"\n",
		"another schema":  "schema = 2\n",
		"a routable http": "schema = 1\n[router]\nurl = \"http://routable.example\"\n",
		"not TOML":        "schema = \n",
	} {
		_, fileErr := loadFile(t, body, noEnv)
		_, bytesErr := LoadBytes([]byte(body), noEnv)
		if fileErr == nil || bytesErr == nil {
			t.Errorf("%s: Load %v, LoadBytes %v", name, fileErr, bytesErr)
			continue
		}
		if errors.Is(fileErr, ErrLegacyConfig) != errors.Is(bytesErr, ErrLegacyConfig) {
			t.Errorf("%s: Load and LoadBytes disagree on legacy: %v / %v", name, fileErr, bytesErr)
		}
		for _, marker := range []string{"does not define", "reads schema = 1", "loopback"} {
			if strings.Contains(fileErr.Error(), marker) != strings.Contains(bytesErr.Error(), marker) {
				t.Errorf("%s: Load and LoadBytes disagree on %q: %v / %v", name, marker, fileErr, bytesErr)
			}
		}
	}
}
