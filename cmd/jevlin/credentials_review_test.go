package main

import (
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// An origin whose host is not plain ASCII is not one the stored key goes
// to, recorded or configured: Go's case folding maps U+0130 onto ASCII "i",
// so a lower-cased Unicode host could read as the bound origin while the
// connection went to the IDNA name.
func TestAnOriginWithANonASCIIHostIsNotOneTheKeyGoesTo(t *testing.T) {
	for _, raw := range []string{
		"https://api.jevl\u0130n.ai",
		"https://api.jevlin.ai\u200b",
		"https://caf\u00e9.example",
		"https://api.jevlin.ai:4\u0661",
		// Percent-encoded into the host: Hostname returns it decoded.
		"https://api.jevl%C4%B0n.ai",
	} {
		if o, err := urlOrigin(raw); err == nil {
			t.Errorf("urlOrigin(%q) = %q, want a refusal", raw, o)
		}
	}
	// Both keys' services refuse a lookalike destination for an ASCII
	// binding, the case the folding let through.
	for _, c := range []struct {
		s     keyService
		creds credentials
		dest  string
	}{
		{routerKeyService, credentials{Router: "https://api.jevlin.ai"}, "https://api.jevl\u0130n.ai"},
		{routerKeyService, credentials{Router: "https://api.jevlin.ai"}, "https://api.jevl%C4%B0n.ai"},
		{agentsAPIKeyService, credentials{AgentsAPI: "https://agents-v1.jevlin.ai"}, "https://agents-v1.jevl\u0130n.ai"},
	} {
		c.creds.V, c.creds.APIKey = credentialsVersion, boundKey
		if err := c.s.check(&c.creds, c.dest, noEnv); err == nil {
			t.Errorf("the %s bound to %q passed %q", c.s.name, c.creds.Router+c.creds.AgentsAPI, c.dest)
		}
	}
	for _, raw := range []string{"https://api.jevlin.ai", "https://API.JEVLIN.AI:443/", "http://127.0.0.1:8080"} {
		if _, err := urlOrigin(raw); err != nil {
			t.Errorf("urlOrigin(%q): %v", raw, err)
		}
	}
	// And a recorded origin with such a host makes the file one the key is
	// not sent from: the stored key's binding fails, so nothing reaches the
	// router this config names.
	own, cfg, root := newFakeRouter(t, nil)
	storeKey(t, cfg, credentials{Router: "https://api.jevl\u0130n.ai"})
	env := map[string]string{"JEVLIN_HOME": t.TempDir()}
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code == exitOK {
		t.Fatalf("the search was served:\n%s", out)
	}
	if strings.Contains(out, "jevl\u0130n") || strings.Contains(out, boundKey) {
		t.Errorf("the refusal repeats the recorded host or the key:\n%s", out)
	}
	if n := own.requests(); n != 0 {
		t.Fatalf("%d request(s) carried the key", n)
	}
}

// A credentials.json field holding a character a terminal acts on makes the
// file unreadable, and neither the refusal nor login -show repeats it.
func TestACredentialsFileHoldingATerminalControlIsUnreadable(t *testing.T) {
	for name, c := range map[string]credentials{
		"a CSI in the router":           {APIKey: boundKey, Router: "https://api.jevlin.ai\u009b2K"},
		"an RTL override in the agents": {APIKey: boundKey, AgentsAPI: "https://agents-v1.jevlin.ai\u202e"},
		"a line separator in the key":   {APIKey: "sr-\u2028key"},   // #nosec G101 -- a planted record, not a credential
		"a newline in the key":          {APIKey: "sr-key\nrm -rf"}, // #nosec G101 -- a planted record, not a credential
	} {
		t.Run(name, func(t *testing.T) {
			_, cfg, _ := newFakeRouter(t, nil)
			path := credentialsPath(searchBlockOf(t, cfg))
			if err := writeCredentials(path, c); err != nil {
				t.Fatal(err)
			}
			_, err := readCredentials(path)
			if err == nil {
				t.Fatal("the file was read")
			}
			for _, planted := range []string{"\u009b", "\u202e", "\u2028", "rm -rf"} {
				if strings.Contains(err.Error(), planted) {
					t.Errorf("the refusal repeats the planted text: %q", err.Error())
				}
			}
			code, out, errOut := runLogin(t, "", map[string]string{"JEVLIN_HOME": t.TempDir()}, "-config", cfg, "-show")
			if code == exitOK {
				t.Errorf("login -show exited 0 over an unreadable file:\n%s", out)
			}
			for _, planted := range []string{"\u009b", "\u202e", "\u2028", "rm -rf"} {
				if strings.Contains(out+errOut, planted) {
					t.Errorf("login -show printed the planted text:\n%s%s", out, errOut)
				}
			}
		})
	}
}

// A stored key that records no router is bound by the installation's own
// config FILE, never by the endpoint variables a run inherited: a hook runs
// under whatever its host's environment set, and an origin taken from there
// would bind the key, and then record it, to a place nothing in this
// installation chose.
func TestAnUnboundKeyIsNotBoundByTheEnvironment(t *testing.T) {
	own, cfg, root := newFakeRouter(t, nil)
	storeKey(t, cfg, credentials{})
	// The installation has no config file, so its router is the compiled
	// default; the variable names the stub this -config also names.
	env := map[string]string{"JEVLIN_HOME": t.TempDir(), config.EnvRouterURL: own.srv.URL}
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	assertOriginRefusal(t, code, out, origin(t, config.DefaultRouterURL), origin(t, own.srv.URL), root, cfg, credentialsPath(searchBlockOf(t, cfg)))
	if n := own.requests(); n != 0 {
		t.Fatalf("%d request(s) carried the key to the router the environment named", n)
	}
	if c := readStoredKey(t, cfg); c.Router != "" {
		t.Errorf("the environment's router was recorded as the key's: %q", c.Router)
	}
}
