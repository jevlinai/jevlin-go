package main

// The agent-command helpers. Every test injects getenv — a developer's
// real TOKENDROP_LISTEN or JEVLIN_CONFIG must never leak into a test,
// because env beats the config file in config.Load's precedence.

import (
	"strings"
	"testing"
)

// noEnv is the getenv of a hermetic shell.
func noEnv(string) string { return "" }

// envOf builds a getenv from a literal map.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// installedAs is getenv for a test whose installation is the config it
// passes with -config, as it is for a participant whose every rendered
// command names the installation's own config: JEVLIN_CONFIG names that
// file, unless the test set JEVLIN_CONFIG or JEVLIN_HOME itself. The stored
// key's binding and the environment key's rule are both read against the
// installation's config, so a test that means to drive another
// installation's config says which is the installation's.
func installedAs(getenv func(string) string, args []string) func(string) string {
	if getenv("JEVLIN_CONFIG") != "" || getenv("JEVLIN_HOME") != "" {
		return getenv
	}
	cfg := ""
	for i, a := range args {
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			cfg = args[i+1]
		} else if v, ok := strings.CutPrefix(a, "-config="); ok {
			cfg = v
		}
	}
	if cfg == "" {
		return getenv
	}
	return func(k string) string {
		if k == "JEVLIN_CONFIG" {
			return cfg
		}
		return getenv(k)
	}
}

// The env-only half of key resolution; the credentials file's place in the
// order is covered in credentials_test.go.
func TestAPIKeyUsesOnlySearchEnvironment(t *testing.T) {
	m := emptySearch(t)
	got, src, err := resolveAPIKey(envOf(map[string]string{ // #nosec G101 -- env-var names and synthetic canaries, not credentials
		"JEVLIN_API_KEY": "canary-tenant-key",
		"OPENAI_API_KEY": "canary-personal-key",
	}), m)
	if err != nil || got != "canary-tenant-key" || src != keyFromEnv {
		t.Errorf("a personal OpenAI key must not shadow the tenant key: got %q from %q (%v)", got, src, err)
	}
	if k, src, _ := resolveAPIKey(envOf(map[string]string{"OPENAI_API_KEY": "canary-personal-key"}), m); k != "" || src != keyFromNone {
		t.Errorf("unrelated source: %q from %q", k, src)
	}
	if k, src, err := resolveAPIKey(noEnv, m); k != "" || src != keyFromNone || err != nil {
		t.Errorf("no key set must mean no key: %q %q %v", k, src, err)
	}
}
