package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

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

func loadErr(t *testing.T, args []string, getenv func(string) string) error {
	t.Helper()
	_, _, err := Load(args, getenv)
	if err == nil {
		t.Fatal("expected error, got none")
	}
	return err
}

func TestDefaults(t *testing.T) {
	cfg := load(t, nil, noEnv)
	if cfg.Miner.RouterURL == nil || cfg.Miner.RouterURL.String() != DefaultRouterURL {
		t.Errorf("router default: %v", cfg.Miner.RouterURL)
	}
	if cfg.Miner.RouterConfigured {
		t.Error("no config file at all, yet RouterConfigured")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir here: %v", err)
	}
	if want := filepath.Join(base, "jevlin", "state"); cfg.Miner.StateDir != want {
		t.Errorf("state dir default: %q, want %q", cfg.Miner.StateDir, want)
	}
	if want := filepath.Join(base, "jevlin", "sessions"); cfg.Miner.SessionsDir != want {
		t.Errorf("sessions dir default: %q, want %q", cfg.Miner.SessionsDir, want)
	}
	if cfg.Miner.TurnEnd || cfg.Miner.Tier != "" {
		t.Errorf("miner defaults: %+v", cfg.Miner)
	}
	if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
		t.Errorf("platform defaults: %+v", cfg.Platform)
	}
	if cfg.Update.Mode != UpdateApply {
		t.Errorf("update default: %+v", cfg.Update)
	}
}

// The config file names the state directory; the sessions directory
// defaults beside it, and an explicit one wins.
func TestStateAndSessionsDirectories(t *testing.T) {
	home := filepath.ToSlash(t.TempDir())
	cfg := load(t, []string{"-config", writeTOML(t, "[miner]\nstate_dir = \""+home+"/state\"\n")}, noEnv)
	if cfg.Miner.StateDir != filepath.FromSlash(home+"/state") {
		t.Errorf("state dir: %q", cfg.Miner.StateDir)
	}
	if cfg.Miner.SessionsDir != filepath.Join(filepath.FromSlash(home), "sessions") {
		t.Errorf("sessions dir: %q, want beside the state dir", cfg.Miner.SessionsDir)
	}
	cfg = load(t, []string{"-config", writeTOML(t, "[miner]\nstate_dir = \""+home+"/state\"\nsessions_dir = \""+home+"/elsewhere\"\n")}, noEnv)
	if cfg.Miner.SessionsDir != filepath.FromSlash(home+"/elsewhere") {
		t.Errorf("an explicit sessions dir lost: %q", cfg.Miner.SessionsDir)
	}
}

// A config an earlier version wrote names the state directory under
// [mining], and setup never rewrites an existing config: it is still read,
// and [miner] state_dir wins where both are named.
func TestLegacyMiningStateDirIsRead(t *testing.T) {
	home := filepath.ToSlash(t.TempDir())
	legacy := "[mining]\nas_url = \"https://as.example.com\"\nchain_id = \"twilight-1\"\nslot_id = 7\n" +
		"state_dir = \"" + home + "/state\"\nspool_dir = \"" + home + "/spool\"\n\n" +
		"[miner]\nenabled = true\nintake_dir = \"" + home + "/intake\"\nsessions_dir = \"" + home + "/sessions\"\n"
	cfg := load(t, []string{"-config", writeTOML(t, legacy)}, noEnv)
	if cfg.Miner.StateDir != filepath.FromSlash(home+"/state") {
		t.Errorf("state dir from [mining]: %q", cfg.Miner.StateDir)
	}
	both := legacy + "state_dir = \"" + home + "/newer\"\n"
	cfg = load(t, []string{"-config", writeTOML(t, both)}, noEnv)
	if cfg.Miner.StateDir != filepath.FromSlash(home+"/newer") {
		t.Errorf("[miner] state_dir did not win: %q", cfg.Miner.StateDir)
	}
}

// Every table and key an earlier version, or the proxy before it, wrote is
// accepted: an installation that upgrades keeps loading its config. Nothing
// of them is read, so a value that would once have been refused is not.
func TestLegacyTablesAreAcceptedAndUnread(t *testing.T) {
	body := `[proxy]
listen = "127.0.0.1:8787"
admin_listen = "127.0.0.1:8788"
shutdown_grace = "5s"
max_request_body_bytes = 1
max_inbound_conns = 1

[[provider]]
name = "openrouter"
tier = "A"
upstream = "https://openrouter.ai/api"

[transport]
response_header_timeout = "1s"
upstream_ca_file = "/nonexistent/ca.pem"

[privacy]
log_content = true

[log]
level = "debug"

[observe]
observation_memory_budget = 1
ring_bytes = 1
max_event_bytes = 1
max_events = 1
max_observed_body_bytes = 1
max_depth = 1
max_json_key_bytes = 1

[mining]
enabled = true
as_url = "http://as.example.com"
chain_id = "twilight-1"
slot_id = 7
target_epoch = 3
metadata_ttl = "1m"
collector_interval = "1s"
collector_base_backoff = "1s"
collector_max_backoff = "1s"
collector_max_attempts = 1
payout_address = "twilight1abc"
platform_slot = "slot"

[miner]
enabled = true
intake_dir = "/tmp/intake"
flush_interval = "3m"
`
	cfg := load(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if cfg.Miner.RouterURL.String() != "https://openrouter.ai/api" {
		t.Errorf("the provider upstream is the router when miner.router_url is unset: %v", cfg.Miner.RouterURL)
	}
	if cfg.Miner.RouterConfigured {
		t.Error("a provider upstream is not a configured router")
	}
}

func TestUnknownTOMLKeyRejected(t *testing.T) {
	err := loadErr(t, []string{"-config", writeTOML(t, "[miner]\nrouter_ur = \"https://r.example\"\n")}, noEnv)
	if !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "miner.router_ur") {
		t.Fatalf("got %v", err)
	}
	err = loadErr(t, []string{"-config", writeTOML(t, "[flush]\ninterval = \"3m\"\n")}, noEnv)
	if !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a table this version never wrote was accepted: %v", err)
	}
}

func TestExplicitConfigMustExist(t *testing.T) {
	_ = loadErr(t, []string{"-config", filepath.Join(t.TempDir(), "missing.toml")}, noEnv)
	if _, _, err := Load(nil, func(k string) string {
		if k == "JEVLIN_CONFIG" {
			return filepath.Join(t.TempDir(), "missing.toml")
		}
		return ""
	}); err == nil {
		t.Fatal("JEVLIN_CONFIG naming a missing file was accepted")
	}
}

func TestUpstreamValidation(t *testing.T) {
	for _, bad := range []string{
		"http://openrouter.ai/api",
		"https://user:pw@openrouter.ai/api",
		"https://openrouter.ai/api?x=1",
		"https://openrouter.ai/api#frag",
		"https://openrouter.ai/api/",
		"https:///api",
	} {
		if _, err := parseUpstream(bad); err == nil {
			t.Errorf("upstream %q accepted", bad)
		}
	}
	if _, err := parseUpstream("https://openrouter.ai/api"); err != nil {
		t.Errorf("valid upstream refused: %v", err)
	}
	err := loadErr(t, []string{"-config", writeTOML(t, "[[provider]]\nupstream = \"http://r.example\"\n")}, noEnv)
	if !strings.Contains(err.Error(), "provider.upstream") {
		t.Fatalf("got %v", err)
	}
}

func TestOneProviderOnly(t *testing.T) {
	body := "[[provider]]\nupstream = \"https://a.example\"\n[[provider]]\nupstream = \"https://b.example\"\n"
	err := loadErr(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if !strings.Contains(err.Error(), "exactly one [[provider]]") {
		t.Fatalf("got %v", err)
	}
}

func TestDurationParsing(t *testing.T) {
	var d duration
	if err := d.UnmarshalText([]byte("90s")); err != nil || d.Seconds() != 90 {
		t.Fatalf("got %v %v", d, err)
	}
	if err := d.UnmarshalText([]byte("soon")); err == nil {
		t.Fatal("bad duration accepted")
	}
}

func TestVersionFlag(t *testing.T) {
	_, show, err := Load([]string{"-version"}, noEnv)
	if err != nil || !show {
		t.Fatalf("got show=%v err=%v", show, err)
	}
}

func TestUnexpectedArgRejected(t *testing.T) {
	_ = loadErr(t, []string{"extra"}, noEnv)
}

func TestMinerRejectsRoutablePlainHTTPRouterURL(t *testing.T) {
	for _, host := range []string{"router.example.com", "203.0.113.10", "router.internal:8080"} {
		body := "[miner]\nrouter_url = \"http://" + host + "\"\n"
		err := loadErr(t, []string{"-config", writeTOML(t, body)}, noEnv)
		if err == nil {
			t.Errorf("plain http router_url %q was accepted at load", host)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("router_url %q: the refusal does not name the rule: %v", host, err)
		}
	}
	if _, err := parseUpstream("http://router.attacker.example"); err == nil {
		t.Fatal("parseUpstream unexpectedly accepted http://")
	}
}

// The carve-out that makes local development work without TLS — losing it
// would be as much a defect as losing the rule. Testing against a ROUTABLE
// devnet router still needs TLS on the devnet, or the client co-located
// over loopback: the intended consequence, not a gap.
func TestMinerAllowsPlainHTTPRouterURLOnLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		body := "[miner]\nrouter_url = \"http://" + host + "\"\n"
		if _, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv); err != nil {
			t.Errorf("loopback router_url %q was refused: %v", host, err)
		}
	}
}

func TestMinerRejectsUserinfoRouterURL(t *testing.T) {
	err := loadErr(t, []string{"-config", writeTOML(t, "[miner]\nrouter_url = \"https://u:p@router.example\"\n")}, noEnv)
	if !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("got %v", err)
	}
}

// [miner] turn_end is off unless the config says otherwise: it sends
// model-written text off the machine, and a default must not.
func TestMinerTurnEndIsOffByDefault(t *testing.T) {
	for body, want := range map[string]bool{
		"[miner]\nrouter_url = \"http://127.0.0.1:8080\"\n":                   false,
		"[miner]\nrouter_url = \"http://127.0.0.1:8080\"\nturn_end = false\n": false,
		"[miner]\nrouter_url = \"http://127.0.0.1:8080\"\nturn_end = true\n":  true,
	} {
		cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if cfg.Miner.TurnEnd != want {
			t.Errorf("%q: turn_end = %v, want %v", body, cfg.Miner.TurnEnd, want)
		}
	}
}

// RouterConfigured tells a router the config names from the provider
// upstream standing in for one.
func TestMinerRouterConfigured(t *testing.T) {
	named, _, err := Load([]string{"-config", writeTOML(t, "[miner]\nrouter_url = \"http://127.0.0.1:8080\"\n")}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !named.Miner.RouterConfigured || named.Miner.RouterURL == nil {
		t.Errorf("a named router: configured=%v url=%v", named.Miner.RouterConfigured, named.Miner.RouterURL)
	}
	fallback, _, err := Load([]string{"-config", writeTOML(t, "[miner]\nturn_end = true\n")}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Miner.RouterConfigured {
		t.Errorf("no miner.router_url, yet RouterConfigured; RouterURL = %v", fallback.Miner.RouterURL)
	}
}

func TestPlatformBaseURLDefaults(t *testing.T) {
	cfg := load(t, nil, noEnv)
	if cfg.Platform.BaseURL != "https://platform.jevlin.ai" {
		t.Fatalf("got %q, want the default", cfg.Platform.BaseURL)
	}
}

// platform.base_url is the router_url rule again: connect sends the
// platform-issued sr- key in Authorization to the agents API, and the
// portal keeps the same posture.
func TestPlatformBaseURLRejectsRoutablePlainHTTP(t *testing.T) {
	for _, host := range []string{"platform.example.com", "203.0.113.10", "platform.internal:8080"} {
		body := "[platform]\nbase_url = \"http://" + host + "\"\n"
		err := loadErr(t, []string{"-config", writeTOML(t, body)}, noEnv)
		if err == nil {
			t.Errorf("plain http platform.base_url %q was accepted at load", host)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("platform.base_url %q: the refusal does not name the rule: %v", host, err)
		}
	}
}

func TestPlatformBaseURLAllowsPlainHTTPOnLoopback(t *testing.T) {
	body := "[platform]\nbase_url = \"http://127.0.0.1:9090\"\n"
	if _, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv); err != nil {
		t.Errorf("loopback platform.base_url was refused: %v", err)
	}
}

// platform.agents_api_url defaults to the real agents API, separate from
// platform.base_url (the human portal) — live testing found the two are
// different hosts in the real deployment, not one shared origin.
func TestAgentsAPIURLDefaults(t *testing.T) {
	cfg := load(t, nil, noEnv)
	if cfg.Platform.AgentsAPIURL != "https://agents-v1.jevlin.ai" {
		t.Fatalf("got %q, want the default", cfg.Platform.AgentsAPIURL)
	}
	if cfg.Platform.AgentsAPIURL == cfg.Platform.BaseURL {
		t.Fatalf("agents_api_url and base_url defaulted to the same value; they are different hosts")
	}
}

// Same https-or-loopback rule as base_url (invariant 5): connect sends the
// platform-issued sr- key in Authorization to this one.
func TestAgentsAPIURLRejectsRoutablePlainHTTP(t *testing.T) {
	body := "[platform]\nagents_api_url = \"http://agents.example.com\"\n"
	err := loadErr(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if err == nil {
		t.Fatal("plain http platform.agents_api_url was accepted at load")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("the refusal does not name the rule: %v", err)
	}
}

func TestAgentsAPIURLAllowsPlainHTTPOnLoopback(t *testing.T) {
	body := "[platform]\nagents_api_url = \"http://127.0.0.1:9091\"\n"
	if _, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv); err != nil {
		t.Errorf("loopback platform.agents_api_url was refused: %v", err)
	}
}

// A dev/test config naming only a loopback base_url (every existing
// stub-backed test does exactly this) must not silently default
// agents_api_url to the real platform — connect would then register
// against production while believing it was talking to a local stub,
// which is exactly what happened once, live, before this fix.
func TestAgentsAPIURLDefaultsToBaseURLWhenBaseURLIsLoopback(t *testing.T) {
	body := "[platform]\nbase_url = \"http://127.0.0.1:9999\"\n"
	cfg := load(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if cfg.Platform.AgentsAPIURL != "http://127.0.0.1:9999" {
		t.Fatalf("agents_api_url defaulted to %q, want the loopback base_url reused, not the real platform",
			cfg.Platform.AgentsAPIURL)
	}
}

// The loopback default is a convenience, not a lock-in: an explicit
// agents_api_url still wins, including pointing a loopback base_url at
// a real (or a second, differently-loopback) agents API.
func TestAgentsAPIURLExplicitValueOverridesTheLoopbackDefault(t *testing.T) {
	body := "[platform]\nbase_url = \"http://127.0.0.1:9999\"\nagents_api_url = \"https://agents-v1.jevlin.ai\"\n"
	cfg := load(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if cfg.Platform.AgentsAPIURL != "https://agents-v1.jevlin.ai" {
		t.Fatalf("got %q, want the explicit value", cfg.Platform.AgentsAPIURL)
	}
}

// The two [platform] URLs are independently configurable — setting one
// must not disturb the other's default.
func TestPlatformURLsAreIndependentlyConfigurable(t *testing.T) {
	body := "[platform]\nbase_url = \"https://portal.example.com\"\nagents_api_url = \"https://agents.example.com\"\n"
	cfg := load(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if cfg.Platform.BaseURL != "https://portal.example.com" {
		t.Fatalf("base_url: got %q", cfg.Platform.BaseURL)
	}
	if cfg.Platform.AgentsAPIURL != "https://agents.example.com" {
		t.Fatalf("agents_api_url: got %q", cfg.Platform.AgentsAPIURL)
	}
}

// A custom non-loopback base_url (a devnet, a staging portal) gives no safe
// signal about which API host pairs with it — defaulting to production here
// would be the same footgun the loopback fix above exists to close, just
// for a devnet instead of a laptop. Refused, not defaulted.
func TestNonDefaultNonLoopbackBaseURLRequiresExplicitAgentsAPIURL(t *testing.T) {
	body := "[platform]\nbase_url = \"https://portal.devnet.example.com\"\n"
	err := loadErr(t, []string{"-config", writeTOML(t, body)}, noEnv)
	if err == nil {
		t.Fatal("a devnet base_url with no agents_api_url was accepted, silently defaulting to production")
	}
	if !strings.Contains(err.Error(), "agents_api_url") {
		t.Errorf("the refusal does not name the missing key: %v", err)
	}
}

func TestLoadIgnoresJevlinTOMLInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	body := "[miner]\nrouter_url = \"https://evil.example\"\n\n[platform]\nagents_api_url = \"https://evil.example\"\n"
	if err := os.WriteFile(filepath.Join(dir, "jevlin.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg := load(t, nil, noEnv)
	if cfg.Miner.RouterConfigured {
		t.Fatalf("cwd jevlin.toml was loaded: router %v", cfg.Miner.RouterURL)
	}
	if u := cfg.Miner.RouterURL; u != nil && u.Host == "evil.example" {
		t.Fatalf("router taken from cwd jevlin.toml: %v", u)
	}
}

// LoadBytes is Load over bytes already in hand: setup's dry run checks the
// config it would write without writing it.
func TestLoadBytesIsLoadOverBytes(t *testing.T) {
	cfg, err := LoadBytes([]byte("[miner]\nrouter_url = \"http://127.0.0.1:8080\"\n"), noEnv)
	if err != nil || !cfg.Miner.RouterConfigured {
		t.Fatalf("LoadBytes: %+v %v", cfg, err)
	}
	if _, err := LoadBytes([]byte("[miner]\nrouter_url = \"http://routable.example\"\n"), noEnv); err == nil {
		t.Fatal("LoadBytes accepted what Load refuses")
	}
}

// [update] mode: absent is apply, the three words are normalized, and a
// word this version cannot name falls to notify — never off, which would
// silence the check by a typo, and never apply, the default, which would
// replace a binary on a word nobody chose — with the word kept aside.
func TestUpdateModeDefaultsToApplyAndAnUnknownWordFallsToNotify(t *testing.T) {
	for raw, want := range map[string]string{
		"":        UpdateApply,
		"apply":   UpdateApply,
		"notify":  UpdateNotify,
		"off":     UpdateOff,
		"Off":     UpdateOff,
		" apply ": UpdateApply,
	} {
		body := "[miner]\nrouter_url = \"http://127.0.0.1:8080\"\n"
		if raw != "" {
			body += "[update]\nmode = \"" + raw + "\"\n"
		}
		cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv)
		if err != nil {
			t.Fatalf("mode %q: %v", raw, err)
		}
		if cfg.Update.Mode != want || cfg.Update.UnknownMode != "" {
			t.Errorf("mode %q: Mode = %q UnknownMode = %q, want %q and none", raw, cfg.Update.Mode, cfg.Update.UnknownMode, want)
		}
	}
	for _, unknown := range []string{"auto", "of", "apply; echo x"} {
		body := "[update]\nmode = \"" + unknown + "\"\n"
		cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv)
		if err != nil {
			t.Errorf("mode %q stopped the config from loading: %v", unknown, err)
			continue
		}
		if cfg.Update.Mode != UpdateNotify || cfg.Update.UnknownMode != unknown {
			t.Errorf("mode %q: Mode = %q UnknownMode = %q, want notify and the word kept aside", unknown, cfg.Update.Mode, cfg.Update.UnknownMode)
		}
	}
}

// [miner] tier is the participant's own setting, written by hand, and a
// word the client cannot use must not take the config down with it: a
// failed load stops search for a value that changes nothing but which band
// a request asks for. So case and surrounding space are forgiven, and a
// word that is still not a band loads as no tier, kept aside for status to
// name, and is never sent, so the router does not answer every search with
// a 400 either.
func TestMinerTierIsNormalizedAndAnUnknownWordIsIgnoredAtLoad(t *testing.T) {
	for raw, want := range map[string]string{
		"":         "",
		"fast":     "fast",
		"balanced": "balanced",
		"max":      "max",
		"Fast":     "fast",
		" max ":    "max",
	} {
		body := "[miner]\nrouter_url = \"http://127.0.0.1:8080\"\n"
		if raw != "" {
			body += "tier = \"" + raw + "\"\n"
		}
		cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv)
		if err != nil {
			t.Fatalf("tier %q: %v", raw, err)
		}
		if cfg.Miner.Tier != want || cfg.Miner.UnknownTier != "" {
			t.Errorf("tier %q: Tier = %q UnknownTier = %q, want %q and none", raw, cfg.Miner.Tier, cfg.Miner.UnknownTier, want)
		}
	}
	for _, unknown := range []string{"turbo", "auto", "max; echo x"} {
		body := "[miner]\nrouter_url = \"http://127.0.0.1:8080\"\ntier = \"" + unknown + "\"\n"
		cfg, _, err := Load([]string{"-config", writeTOML(t, body)}, noEnv)
		if err != nil {
			t.Errorf("tier %q stopped the config from loading: %v", unknown, err)
			continue
		}
		if cfg.Miner.Tier != "" || cfg.Miner.UnknownTier != unknown {
			t.Errorf("tier %q: Tier = %q UnknownTier = %q, want none and the word kept aside", unknown, cfg.Miner.Tier, cfg.Miner.UnknownTier)
		}
	}
}

// A config written before the move to jevlin.ai may name only the old
// portal and rely on the agents API being implied from it. It still is,
// as the old pair: an upgrade must not turn a loading config into one
// refused as "a custom portal with no API named" (PR #87 review).
func TestLegacyPortalOnlyConfigStillImpliesItsAgentsAPI(t *testing.T) {
	cfg, _, err := Load([]string{"-config", writeTOML(t, "[platform]\nbase_url = \"https://platform.nyks.dev\"\n")}, noEnv)
	if err != nil {
		t.Fatalf("a portal-only config from before the move was refused: %v", err)
	}
	if cfg.Platform.BaseURL != "https://platform.nyks.dev" || cfg.Platform.AgentsAPIURL != "https://agents-v1.nyks.dev" {
		t.Fatalf("legacy pair: base_url %q, agents_api_url %q", cfg.Platform.BaseURL, cfg.Platform.AgentsAPIURL)
	}
	// Named explicitly, the old pair is simply two URLs; and a portal that
	// is neither default still needs its API named.
	if _, _, err := Load([]string{"-config", writeTOML(t, "[platform]\nbase_url = \"https://platform.staging.example\"\n")}, noEnv); err == nil {
		t.Fatal("a custom portal with no agents_api_url was accepted")
	}
}
