package config

// Configuration format 1. The file names every endpoint in its own section,
// begins `schema = 1`, and anything else — no schema line, an older file's
// sections, a key format 1 does not define — is refused with one message,
// never reinterpreted.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

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

// The compiled defaults are the whole configuration when no file is named,
// and a file holding only the schema line means the same thing: every
// endpoint is Jevlin's, and no AS is configured.
func TestNoFileAndAMinimalFileAreTheCompiledDefaults(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"no file":      load(t, nil, noEnv),
		"schema only":  mustLoadFile(t, "schema = 1\n", noEnv),
		"empty tables": mustLoadFile(t, "schema = 1\n[router]\n[platform]\n[agents]\n[mining]\n", noEnv),
	} {
		if got := cfg.Miner.RouterURL.String(); got != DefaultRouterURL {
			t.Errorf("%s: router %q, want %q", name, got, DefaultRouterURL)
		}
		if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
			t.Errorf("%s: platform %q agents %q", name, cfg.Platform.BaseURL, cfg.Platform.AgentsAPIURL)
		}
		if cfg.Mining.ASBaseURL != "" || cfg.Miner.Enabled {
			t.Errorf("%s: an AS (%q) or intake (%v) with nothing naming one", name, cfg.Mining.ASBaseURL, cfg.Miner.Enabled)
		}
		if cfg.Update.Mode != UpdateApply {
			t.Errorf("%s: update mode %q", name, cfg.Update.Mode)
		}
	}
}

// Every file that is not format 1 is refused with the one message, whatever
// it says: an empty file, an older file's provider block or miner table, a
// proxy-era section, a key format 1 does not define, or format-1 sections
// with no schema line.
func TestAFileThatIsNotFormatOneIsRefusedWithOneMessage(t *testing.T) {
	for name, body := range map[string]string{
		"empty":                   "",
		"comments only":           "# nothing here\n",
		"an older provider block": "[[provider]]\nname = \"search-router\"\nupstream = \"https://api.jevlin.ai\"\n",
		"an older miner table":    "[miner]\nenabled = true\nrouter_url = \"https://api.jevlin.ai\"\n",
		"an older platform key":   "schema = 1\n[platform]\nbase_url = \"https://platform.jevlin.ai\"\n",
		"a proxy listener":        "schema = 1\n[proxy]\nlisten = \"127.0.0.1:8787\"\n",
		"privacy.log_content":     "schema = 1\n[privacy]\nlog_content = false\n",
		"an unknown key":          "schema = 1\n[router]\nurl = \"https://api.jevlin.ai\"\nshade = \"blue\"\n",
		"no schema line":          "[router]\nurl = \"https://api.jevlin.ai\"\n",
	} {
		_, err := loadFile(t, body, noEnv)
		if !errors.Is(err, ErrLegacyConfig) {
			t.Errorf("%s: err = %v, want ErrLegacyConfig", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "this configuration is from an older jevlin; run `jevlin setup`") {
			t.Errorf("%s: the message does not say what to do: %v", name, err)
		}
	}
	_, err := loadFile(t, "schema = 1\n[router]\nshade = \"blue\"\n", noEnv)
	if err == nil || !strings.Contains(err.Error(), "router.shade") {
		t.Errorf("an unknown key is not named: %v", err)
	}
}

// A value of the wrong type in a format-1 key is this file's mistake, not an
// older file.
func TestAWrongValueIsNotCalledAnOlderFile(t *testing.T) {
	_, err := loadFile(t, "schema = 1\n[mining]\nslot_id = \"three\"\n", noEnv)
	if err == nil || errors.Is(err, ErrLegacyConfig) {
		t.Fatalf("a string slot_id: %v", err)
	}
}

// A schema this client does not read is not called older: it may be newer.
func TestAnotherSchemaNumberIsRefusedAsNotThisFormat(t *testing.T) {
	_, err := loadFile(t, "schema = 2\n", noEnv)
	if err == nil || errors.Is(err, ErrLegacyConfig) || !strings.Contains(err.Error(), "schema = 2") {
		t.Fatalf("schema = 2: %v", err)
	}
}

func TestAnExplicitConfigMustExist(t *testing.T) {
	if _, _, err := Load([]string{"-config", filepath.Join(t.TempDir(), "missing.toml")}, noEnv); err == nil {
		t.Fatal("a missing explicit config loaded")
	}
	if _, _, err := Load(nil, envOf(map[string]string{"JEVLIN_CONFIG": filepath.Join(t.TempDir(), "missing.toml")})); err == nil {
		t.Fatal("a missing JEVLIN_CONFIG file loaded")
	}
}

// JEVLIN_CONFIG names the file when -config does not, and -config wins.
func TestJEVLINCONFIGSelectsTheFile(t *testing.T) {
	a := writeTOML(t, "schema = 1\n[router]\nurl = \"https://a.example\"\n")
	b := writeTOML(t, "schema = 1\n[router]\nurl = \"https://b.example\"\n")
	env := envOf(map[string]string{"JEVLIN_CONFIG": a})
	if got := load(t, nil, env).Miner.RouterURL.Host; got != "a.example" {
		t.Errorf("JEVLIN_CONFIG: router %q", got)
	}
	if got := load(t, []string{"-config", b}, env).Miner.RouterURL.Host; got != "b.example" {
		t.Errorf("-config over JEVLIN_CONFIG: router %q", got)
	}
}

// The four endpoint variables apply at runtime, after the file, held to the
// file's own URL rules.
func TestTheEndpointVariablesApplyAtRuntimeUnderTheSameRules(t *testing.T) {
	body := `schema = 1
[router]
url = "https://router.file.example"
[platform]
url = "https://portal.file.example"
[agents]
url = "https://agents.file.example"
[mining]
as_url = "https://as.file.example"
`
	cfg := mustLoadFile(t, body, envOf(map[string]string{
		EnvRouterURL:    "https://router.env.example",
		EnvPlatformURL:  "https://portal.env.example",
		EnvAgentsAPIURL: "https://agents.env.example",
		EnvASURL:        "https://as.env.example",
	}))
	if cfg.Miner.RouterURL.Host != "router.env.example" || cfg.Platform.BaseURL != "https://portal.env.example" ||
		cfg.Platform.AgentsAPIURL != "https://agents.env.example" || cfg.Mining.ASBaseURL != "https://as.env.example" {
		t.Fatalf("the variables did not win: %+v %+v %q", cfg.Miner.RouterURL, cfg.Platform, cfg.Mining.ASBaseURL)
	}
	// With no file at all as well.
	if got := load(t, nil, envOf(map[string]string{EnvRouterURL: "http://127.0.0.1:9"})).Miner.RouterURL.String(); got != "http://127.0.0.1:9" {
		t.Errorf("JEVLIN_ROUTER_URL with no file: %q", got)
	}
	for _, name := range []string{EnvRouterURL, EnvPlatformURL, EnvAgentsAPIURL, EnvASURL} {
		for _, bad := range []string{"http://router.example", "https://user:pw@router.example", "ftp://router.example", "https://router.example/?q=1"} {
			env := map[string]string{name: bad}
			if name == EnvPlatformURL {
				env[EnvAgentsAPIURL] = "https://agents.example"
			}
			if _, _, err := Load(nil, envOf(env)); err == nil {
				t.Errorf("%s=%q loaded", name, bad)
			}
		}
	}
}

// The proxy-era variables are gone: setting them changes nothing.
func TestTheProxyEraVariablesHaveNoEffect(t *testing.T) {
	name := func(parts ...string) string { return strings.Join(parts, "_") }
	env := envOf(map[string]string{
		name("TOKENDROP", "UPSTREAM"):         "https://legacy-upstream.example/api",
		name("TOKENDROP", "UPSTREAM_CA_FILE"): "/nonexistent.pem",
		name("TOKENDROP", "LISTEN"):           "0.0.0.0:1",
		name("TOKENDROP", "ADMIN_LISTEN"):     "0.0.0.0:2",
		name("TOKENDROP", "LOG_LEVEL"):        "nonsense",
	})
	for name, cfg := range map[string]*Config{
		"no file":     load(t, nil, env),
		"schema only": mustLoadFile(t, "schema = 1\n", env),
	} {
		if got := cfg.Miner.RouterURL.String(); got != DefaultRouterURL {
			t.Errorf("%s: router %q", name, got)
		}
	}
}

func TestEveryEndpointIsHeldToTheURLRules(t *testing.T) {
	for section, key := range map[string]string{"router": "url", "platform": "url", "agents": "url", "mining": "as_url"} {
		for _, bad := range []string{"http://host.example", "https://u:p@host.example", "not a url", "https://host.example/#x"} {
			body := "schema = 1\n[" + section + "]\n" + key + " = \"" + bad + "\"\n"
			if section == "platform" {
				body += "[agents]\nurl = \"https://agents.example\"\n"
			}
			if _, err := loadFile(t, body, noEnv); err == nil {
				t.Errorf("%s.%s = %q loaded", section, key, bad)
			}
		}
		for _, good := range []string{"https://host.example", "http://127.0.0.1:8080", "http://localhost:1", "http://[::1]:2"} {
			body := "schema = 1\n[" + section + "]\n" + key + " = \"" + good + "\"\n"
			if section == "platform" {
				body += "[agents]\nurl = \"https://agents.example\"\n"
			}
			if _, err := loadFile(t, body, noEnv); err != nil {
				t.Errorf("%s.%s = %q refused: %v", section, key, good, err)
			}
		}
	}
}

// A custom portal names its agents API, unless the portal is loopback, where
// one local stub serves both.
func TestTheAgentsAPIPairsWithThePortal(t *testing.T) {
	if _, err := loadFile(t, "schema = 1\n[platform]\nurl = \"https://portal.staging.example\"\n", noEnv); err == nil {
		t.Error("a custom portal with no agents API loaded")
	}
	cfg := mustLoadFile(t, "schema = 1\n[platform]\nurl = \"http://127.0.0.1:9000\"\n", noEnv)
	if cfg.Platform.AgentsAPIURL != "http://127.0.0.1:9000" {
		t.Errorf("a loopback portal implies itself as the agents API: %q", cfg.Platform.AgentsAPIURL)
	}
	cfg = mustLoadFile(t, "schema = 1\n[platform]\nurl = \"http://127.0.0.1:9000\"\n[agents]\nurl = \"http://127.0.0.1:9001\"\n", noEnv)
	if cfg.Platform.AgentsAPIURL != "http://127.0.0.1:9001" {
		t.Errorf("a named agents API is used: %q", cfg.Platform.AgentsAPIURL)
	}
	cfg = mustLoadFile(t, "schema = 1\n[agents]\nurl = \"https://agents.staging.example\"\n", noEnv)
	if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != "https://agents.staging.example" {
		t.Errorf("the agents API alone: %+v", cfg.Platform)
	}
}

// An AS, named by the file or JEVLIN_AS_URL, takes the compiled chain and slot
// unless the file names its own, turns intake on, and derives its directories.
func TestAnASTakesTheCompiledIdentityAndTurnsIntakeOn(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg := mustLoadFile(t, "schema = 1\n[mining]\nas_url = \"https://as.example\"\nstate_dir = \""+filepath.ToSlash(stateDir)+"\"\n", noEnv)
	if cfg.Mining.ChainID != DefaultChainID || cfg.Mining.SlotID != DefaultSlotID {
		t.Errorf("identity %q/%d", cfg.Mining.ChainID, cfg.Mining.SlotID)
	}
	if !cfg.Miner.Enabled {
		t.Error("an AS did not turn intake on")
	}
	state := filepath.FromSlash(filepath.ToSlash(stateDir))
	base := filepath.Dir(state)
	if cfg.Miner.IntakeDir != filepath.Join(base, "intake") || cfg.Miner.SessionsDir != filepath.Join(base, "sessions") ||
		cfg.Mining.SpoolDir != filepath.Join(state, "spool") {
		t.Errorf("derived dirs: intake %q sessions %q spool %q", cfg.Miner.IntakeDir, cfg.Miner.SessionsDir, cfg.Mining.SpoolDir)
	}
	cfg = mustLoadFile(t, "schema = 1\n[mining]\nas_url = \"https://as.example\"\nchain_id = \"other-1\"\nslot_id = 0\n", noEnv)
	if cfg.Mining.ChainID != "other-1" || cfg.Mining.SlotID != 0 {
		t.Errorf("a named identity, slot 0 included: %q/%d", cfg.Mining.ChainID, cfg.Mining.SlotID)
	}
	cfg = load(t, nil, envOf(map[string]string{EnvASURL: "https://as.example"}))
	if cfg.Mining.ASBaseURL != "https://as.example" || !cfg.Miner.Enabled || cfg.Mining.SlotID != DefaultSlotID {
		t.Errorf("JEVLIN_AS_URL: %+v intake %v", cfg.Mining, cfg.Miner.Enabled)
	}
}

func TestMiningValuesAreValidated(t *testing.T) {
	for name, body := range map[string]string{
		"a negative metadata_ttl":     "metadata_ttl = \"-1s\"",
		"a negative backoff":          "collector_base_backoff = \"-1s\"",
		"negative attempts":           "collector_max_attempts = -1",
		"a negative flush":            "flush_interval = \"-1s\"",
		"a duration that is not":      "metadata_ttl = \"soon\"",
		"a slot that is not a number": "slot_id = \"three\"",
	} {
		if _, err := loadFile(t, "schema = 1\n[mining]\nas_url = \"https://as.example\"\n"+body+"\n", noEnv); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
	cfg := mustLoadFile(t, "schema = 1\n[mining]\nas_url = \"https://as.example\"\ntarget_epoch = 0\nflush_interval = \"2m\"\nturn_end = true\n", noEnv)
	if cfg.Mining.TargetEpoch == nil || *cfg.Mining.TargetEpoch != 0 {
		t.Errorf("target_epoch = 0 is a pin: %v", cfg.Mining.TargetEpoch)
	}
	if cfg.Miner.FlushInterval != 2*time.Minute || !cfg.Miner.TurnEnd {
		t.Errorf("flush_interval %v turn_end %v", cfg.Miner.FlushInterval, cfg.Miner.TurnEnd)
	}
	if mustLoadFile(t, "schema = 1\n", noEnv).Miner.TurnEnd {
		t.Error("the turn end is on by default")
	}
}

// connect and mining enable ask their question only when the file never
// answered it; an explicit false is an answer.
func TestMiningEnabledExplicitDistinguishesAbsentFromFalse(t *testing.T) {
	if load(t, nil, noEnv).MiningEnabledExplicit {
		t.Fatal("no file: explicit")
	}
	cfg := mustLoadFile(t, "schema = 1\n[mining]\nenabled = false\n", noEnv)
	if !cfg.MiningEnabledExplicit || cfg.Mining.Enabled {
		t.Fatalf("enabled = false: explicit %v enabled %v", cfg.MiningEnabledExplicit, cfg.Mining.Enabled)
	}
	cfg = mustLoadFile(t, "schema = 1\n[mining]\nenabled = true\npayout_address = \"twilight1abc\"\n", noEnv)
	if !cfg.MiningEnabledExplicit || !cfg.Mining.Enabled || cfg.Mining.PayoutAddress != "twilight1abc" {
		t.Fatalf("enabled = true: %+v explicit %v", cfg.Mining, cfg.MiningEnabledExplicit)
	}
	if mustLoadFile(t, "schema = 1\n[router]\nurl = \"https://api.jevlin.ai\"\n", noEnv).MiningEnabledExplicit {
		t.Fatal("a file that never mentions [mining] answered the question")
	}
}

// A jevlin.toml in the working directory is never loaded on its own: a
// cloned repo must not choose where the participant's keys are sent.
func TestLoadIgnoresJevlinTOMLInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	body := "schema = 1\n[router]\nurl = \"https://evil.example\"\n[agents]\nurl = \"https://evil.example\"\n"
	if err := os.WriteFile(filepath.Join(dir, "jevlin.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	cfg := load(t, nil, noEnv)
	if cfg.Miner.RouterURL.Host == "evil.example" || cfg.Platform.AgentsAPIURL == "https://evil.example" {
		t.Fatalf("cwd jevlin.toml was loaded: %v %v", cfg.Miner.RouterURL, cfg.Platform.AgentsAPIURL)
	}
}

// [update] mode: absent is apply, the three words are normalized, and a
// word this version cannot name falls to notify with the word kept aside.
func TestUpdateModeDefaultsToApplyAndAnUnknownWordFallsToNotify(t *testing.T) {
	for raw, want := range map[string]string{"": UpdateApply, "apply": UpdateApply, "notify": UpdateNotify, "off": UpdateOff, "Off": UpdateOff, " apply ": UpdateApply} {
		body := "schema = 1\n"
		if raw != "" {
			body += "[update]\nmode = \"" + raw + "\"\n"
		}
		cfg := mustLoadFile(t, body, noEnv)
		if cfg.Update.Mode != want || cfg.Update.UnknownMode != "" {
			t.Errorf("mode %q: %+v, want %q", raw, cfg.Update, want)
		}
	}
	for _, unknown := range []string{"auto", "of", "apply; echo x"} {
		cfg := mustLoadFile(t, "schema = 1\n[update]\nmode = \""+unknown+"\"\n", noEnv)
		if cfg.Update.Mode != UpdateNotify || cfg.Update.UnknownMode != unknown {
			t.Errorf("mode %q: %+v", unknown, cfg.Update)
		}
	}
}

// [router] tier is the participant's own setting: case and space are
// forgiven, and a word that is still not a band loads as no tier, kept aside
// for status to name, and is never sent.
func TestRouterTierIsNormalizedAndAnUnknownWordIsIgnoredAtLoad(t *testing.T) {
	for raw, want := range map[string]string{"": "", "fast": "fast", "balanced": "balanced", "max": "max", "Fast": "fast", " max ": "max"} {
		body := "schema = 1\n[router]\n"
		if raw != "" {
			body += "tier = \"" + raw + "\"\n"
		}
		cfg := mustLoadFile(t, body, noEnv)
		if cfg.Miner.Tier != want || cfg.Miner.UnknownTier != "" {
			t.Errorf("tier %q: Tier %q UnknownTier %q", raw, cfg.Miner.Tier, cfg.Miner.UnknownTier)
		}
	}
	for _, unknown := range []string{"turbo", "max; echo x"} {
		cfg := mustLoadFile(t, "schema = 1\n[router]\ntier = \""+unknown+"\"\n", noEnv)
		if cfg.Miner.Tier != "" || cfg.Miner.UnknownTier != unknown {
			t.Errorf("tier %q: Tier %q UnknownTier %q", unknown, cfg.Miner.Tier, cfg.Miner.UnknownTier)
		}
	}
}

func TestVersionFlagAndUnexpectedArguments(t *testing.T) {
	if _, show, err := Load([]string{"-version"}, noEnv); err != nil || !show {
		t.Errorf("-version: show %v err %v", show, err)
	}
	if _, _, err := Load([]string{"stray"}, noEnv); err == nil {
		t.Error("a stray argument loaded")
	}
	for _, gone := range []string{"-listen", "-admin-listen", "-log-level"} {
		if _, _, err := Load([]string{gone, "x"}, noEnv); err == nil {
			t.Errorf("the proxy-era flag %s is still accepted", gone)
		}
	}
}

// LoadBytes judges bytes as Load judges a file, environment included.
func TestLoadBytesIsLoadOnBytes(t *testing.T) {
	if _, err := LoadBytes([]byte("[miner]\nenabled = true\n"), noEnv); !errors.Is(err, ErrLegacyConfig) {
		t.Errorf("an older file's bytes: %v", err)
	}
	cfg, err := LoadBytes([]byte("schema = 1\n"), envOf(map[string]string{EnvRouterURL: "https://r.example"}))
	if err != nil || cfg.Miner.RouterURL.Host != "r.example" {
		t.Errorf("LoadBytes with the environment: %v %v", cfg, err)
	}
}
