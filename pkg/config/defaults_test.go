package config

import (
	"net/url"
	"testing"
)

// Every network default is an https origin on a jevlin.ai host, with no
// path and no credential. A cutover changes these literals; this is what
// notices a miss, a scheme downgrade, or a stray path that every request
// would then carry.
func TestDefaultsAreJevlin(t *testing.T) {
	defaults := map[string]string{
		"DefaultRouterURL":       DefaultRouterURL,
		"DefaultPlatformBaseURL": DefaultPlatformBaseURL,
		"DefaultAgentsAPIURL":    DefaultAgentsAPIURL,
	}
	for name, raw := range defaults {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			t.Errorf("%s = %q: want an https origin with no path and no credential (%v)", name, raw, err)
			continue
		}
		if u.Host != "api.jevlin.ai" && u.Host != "platform.jevlin.ai" && u.Host != "agents-v1.jevlin.ai" {
			t.Errorf("%s = %q: not a jevlin.ai host", name, raw)
		}
		// Each default passes the rule every configured endpoint passes,
		// so a config naming none loads.
		if _, err := ParseEndpoint(raw, name); err != nil {
			t.Errorf("%s = %q fails ParseEndpoint: %v", name, raw, err)
		}
	}
	// The portal and the agents API are two hosts (defaults.go says why).
	if DefaultPlatformBaseURL == DefaultAgentsAPIURL {
		t.Error("the portal and the agents API share one default origin")
	}
	cfg := load(t, nil, noEnv)
	if cfg.Search.RouterURL.String() != DefaultRouterURL {
		t.Errorf("the router default is not DefaultRouterURL: %v", cfg.Search.RouterURL)
	}
	if cfg.Platform.BaseURL != DefaultPlatformBaseURL || cfg.Platform.AgentsAPIURL != DefaultAgentsAPIURL {
		t.Errorf("the platform defaults are not the compiled pair: %+v", cfg.Platform)
	}
}
