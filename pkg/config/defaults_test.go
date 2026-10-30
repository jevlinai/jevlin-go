package config

import (
	"net/url"
	"testing"
)

// Every network default is an https URL on a jevlin.ai host, or the legacy
// pair this version still implies the agents API for. A cutover changes
// these literals; this is what notices a miss or a scheme downgrade.
func TestDefaultsAreJevlin(t *testing.T) {
	for name, raw := range map[string]string{
		"DefaultRouterURL":       DefaultRouterURL,
		"DefaultPlatformBaseURL": DefaultPlatformBaseURL,
		"DefaultAgentsAPIURL":    DefaultAgentsAPIURL,
		"LegacyPlatformBaseURL":  LegacyPlatformBaseURL,
		"LegacyAgentsAPIURL":     LegacyAgentsAPIURL,
	} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" {
			t.Errorf("%s = %q: want an https origin with no path and no credential (%v)", name, raw, err)
		}
	}
	for name, raw := range map[string]string{
		"DefaultRouterURL":       DefaultRouterURL,
		"DefaultPlatformBaseURL": DefaultPlatformBaseURL,
		"DefaultAgentsAPIURL":    DefaultAgentsAPIURL,
	} {
		if u, _ := url.Parse(raw); u.Host != "api.jevlin.ai" && u.Host != "platform.jevlin.ai" && u.Host != "agents-v1.jevlin.ai" {
			t.Errorf("%s = %q: not a jevlin.ai host", name, raw)
		}
	}
	cfg := load(t, nil, noEnv)
	if cfg.Search.RouterURL.String() != DefaultRouterURL {
		t.Errorf("the router default is not DefaultRouterURL: %v", cfg.Search.RouterURL)
	}
}
