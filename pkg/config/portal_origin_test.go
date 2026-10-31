package config

import (
	"strings"
	"testing"
)

// The portal is matched to the compiled default as an origin, not as a
// string: a trailing slash, an upper-case scheme or host, or the default
// port spelled out all name the same portal, and so pair with the compiled
// agents API. A portal that is another origin still needs [agents] url, and
// the refusal names the variable too, since the portal may have come from
// JEVLIN_PLATFORM_URL alone.
func TestThePortalDefaultIsMatchedAsAnOrigin(t *testing.T) {
	for _, portal := range []string{
		DefaultPlatformBaseURL + "/",
		strings.ToUpper(DefaultPlatformBaseURL),
		strings.Replace(DefaultPlatformBaseURL, "https://", "HTTPS://", 1),
		strings.Replace(DefaultPlatformBaseURL, "platform.jevlin.ai", "platform.jevlin.ai:443", 1),
	} {
		cfg, err := loadFile(t, "schema = 1\n[platform]\nurl = \""+portal+"\"\n", noEnv)
		if err != nil {
			t.Errorf("%q: %v", portal, err)
			continue
		}
		if cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
			t.Errorf("%q: agents API %q, want the compiled default", portal, cfg.Platform.AgentsAPIURL)
		}
	}
	for _, portal := range []string{
		DefaultPlatformBaseURL + "/portal",
		"https://platform.example",
		strings.Replace(DefaultPlatformBaseURL, "platform.jevlin.ai", "platform.jevlin.ai:8443", 1),
	} {
		_, err := loadFile(t, "schema = 1\n[platform]\nurl = \""+portal+"\"\n", noEnv)
		if err == nil || !strings.Contains(err.Error(), "agents.url") || !strings.Contains(err.Error(), EnvAgentsAPIURL) {
			t.Errorf("%q: %v; want a refusal naming agents.url and %s", portal, err, EnvAgentsAPIURL)
		}
	}
	// From the environment alone, the same way.
	env := func(k string) string {
		if k == EnvPlatformURL {
			return "https://platform.example"
		}
		return ""
	}
	if _, _, err := Load(nil, env); err == nil || !strings.Contains(err.Error(), EnvAgentsAPIURL) {
		t.Errorf("a portal from the environment with no agents API: %v", err)
	}
}

// Every key format 1 does not define is named, in sorted order, not
// whichever one a map iteration reached first: through the undecoded-keys
// branch for keys the typed decode merely skips, and through the typed
// decode's failure for a file whose shape it cannot take at all (a format-1
// table written as an array).
func TestEveryUnknownTopLevelKeyIsNamedInOrder(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"skipped keys": {
			"schema = 1\n[zeta]\nx = 1\n[[provider]]\nname = \"r\"\n[alpha]\ny = 2\n[router]\nurl = \"https://api.jevlin.ai\"\n",
			"does not define alpha, alpha.y, provider, provider.name, zeta, zeta.x;",
		},
		"a table written as an array": {
			"schema = 1\n[zeta]\nx = 1\n[[search]]\nstate_dir = \"/tmp/s\"\n[alpha]\ny = 2\n",
			"does not define alpha, zeta;",
		},
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				_, err := loadFile(t, tc.body, noEnv)
				if err == nil {
					t.Fatal("loaded")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("run %d: %v\nwant %q", i, err, tc.want)
				}
			}
		})
	}
}
