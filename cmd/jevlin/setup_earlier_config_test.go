package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// earlierInstallationElsewhere lays out a 0.6.1 installation whose config
// named a state directory outside the installation directory and a router,
// portal and agents API other than the compiled defaults (the stub's), the
// way that version's setup wrote them, with the registration and its key
// beside that state directory. The person then runs setup with nothing in
// the environment but JEVLIN_HOME, as after an unattended update.
func earlierInstallationElsewhere(t *testing.T, s *setupSandbox, router string, withKey bool) (stateDir string) {
	t.Helper()
	elsewhere := filepath.Join(s.root, "elsewhere")
	stateDir = filepath.Join(elsewhere, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := filepath.ToSlash(s.home)
	writeFileT(t, s.cfgPath(), `[[provider]]
name     = "search-router"
upstream = "`+router+`"

[platform]
base_url       = "`+s.platform.srv.URL+`"
agents_api_url = "`+s.platform.srv.URL+`"

[mining]
as_url    = "https://as.invalid.test"
chain_id  = "twilight-testnet-1"
slot_id   = 3
state_dir = "`+filepath.ToSlash(stateDir)+`"
spool_dir = "`+h+`/spool"

[miner]
enabled      = true
intake_dir   = "`+h+`/intake"
sessions_dir = "`+h+`/sessions"
`)
	if !withKey {
		return stateDir
	}
	writeFileT(t, filepath.Join(stateDir, "agent.json"), `{"agent_id":"agent-old","status":"claimed","scopes":["search"]}`+"\n")
	if err := writeCredentials(filepath.Join(elsewhere, credentialsFile), credentials{
		APIKey: "sr-old", Router: router, AgentsAPI: s.platform.srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	s.platform.mu.Lock()
	s.platform.agentByKey["sr-old"] = "agent-old"
	s.platform.statusByAgent["agent-old"] = "claimed"
	s.platform.scopesByAgent["agent-old"] = []string{"search"}
	s.platform.mu.Unlock()
	return stateDir
}

// Setup over that installation writes the new file with the earlier file's
// directories and endpoints: connect finds the registration and registers
// nothing, and the kept key is one a search may send to the router it
// names.
func TestSetupOverAnEarlierConfigKeepsItsDirectoriesAndEndpoints(t *testing.T) {
	s := newSetupSandbox(t)
	router := "https://router.devnet.example"
	stateDir := earlierInstallationElsewhere(t, s, router, true)
	for _, k := range []string{config.EnvRouterURL, config.EnvPlatformURL, config.EnvAgentsAPIURL} {
		delete(s.env, k)
	}
	code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup: exit %d\n%s\n%s", code, out, errOut)
	}
	if register, _, _ := s.platform.counts(); register != 0 {
		t.Errorf("setup registered a new agent (%d) over an installation whose registration it kept\n%s", register, out)
	}
	if !strings.Contains(out, "Your registration and key are kept") {
		t.Errorf("setup did not say the registration was kept:\n%s", out)
	}
	cfg := mustLoadConfig(t, s.cfgPath())
	if filepath.Clean(cfg.Search.StateDir) != filepath.Clean(stateDir) ||
		filepath.Clean(cfg.Search.SessionsDir) != filepath.Join(s.home, "sessions") ||
		cfg.Search.RouterURL.String() != router ||
		cfg.Platform.BaseURL != s.platform.srv.URL || cfg.Platform.AgentsAPIURL != s.platform.srv.URL {
		t.Fatalf("the new config does not carry the earlier one's directories and endpoints:\n%s", s.readFile(s.cfgPath()))
	}
	if _, _, err := resolveAPIKey(noEnv, cfg.Search); err != nil {
		t.Errorf("the kept key is refused for every search: %v", err)
	}
}

// A variable set for the run still chooses its endpoint over the earlier
// file's, as it does for a fresh installation.
func TestAVariableStillChoosesTheEndpointOverAnEarlierConfig(t *testing.T) {
	v := resolveSetupValues("/h", envOf(map[string]string{config.EnvRouterURL: "https://chosen.example"})).
		carrying(earlierConfig{router: "https://earlier.example", agentsAPIURL: "https://agents.earlier.example"})
	if v.router != "https://chosen.example" || v.agentsAPIURL != "https://agents.earlier.example" {
		t.Errorf("router %q, agents API %q", v.router, v.agentsAPIURL)
	}
}

// Only the router setup wrote is carried: an upstream an earlier proxy
// configuration named under another provider is not one.
func TestOnlyTheSearchRouterProviderIsCarriedAsTheRouter(t *testing.T) {
	for body, want := range map[string]string{
		"[[provider]]\nname = \"search-router\"\nupstream = \"https://r.example\"\n":                                              "https://r.example",
		"[[provider]]\nname = \"openrouter\"\nupstream = \"https://openrouter.ai/api\"\n":                                         "",
		"[miner]\nrouter_url = \"https://m.example\"\n[[provider]]\nname = \"search-router\"\nupstream = \"https://r.example\"\n": "https://m.example",
		"not = [toml": "",
	} {
		if got := readEarlierConfig([]byte(body)).router; got != want {
			t.Errorf("%q: router %q, want %q", body, got, want)
		}
	}
}

// With no key beside the state directory there is nothing kept, and setup
// does not say there is.
func TestSetupSaysKeptOnlyOfAKeyThatIsThere(t *testing.T) {
	s := newSetupSandbox(t)
	earlierInstallationElsewhere(t, s, s.env[config.EnvRouterURL], false)
	code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup: exit %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, "kept") {
		t.Errorf("setup said a registration was kept where there was none:\n%s", out)
	}
}
