package main

import (
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/egress"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

// testRouterKey is a key bound to the origin of endpoint, as resolveAPIKey
// returns one for a router at that origin.
func testRouterKey(endpoint, value string) routerKey {
	o, err := egress.Origin(endpoint)
	if err != nil {
		panic(err)
	}
	return routerKey{value: value, source: keyFromEnv, bound: o, secret: "the test key"}
}

// testOrigin is raw's origin, as a stored credential records it.
func testOrigin(raw string) string {
	o, err := egress.Origin(raw)
	if err != nil {
		panic(err)
	}
	return o
}

// resolveKeyParts is resolveAPIKey's key and source, for the tests about the
// resolution order.
func resolveKeyParts(getenv func(string) string, m config.Miner) (string, keySource, error) {
	k, err := resolveAPIKey(getenv, m)
	return k.value, k.source, err
}

// boundCredentials is a credentials file for key as connect publishes one
// for cfg: bound to cfg's router and agents API.
func boundCredentials(cfg *config.Config, key string) credentials {
	c := keyBindingOf(cfg)
	c.APIKey = key
	return c
}

// withoutOriginNotice is stderr less the one line a call to a non-default
// router prints, for a test whose router is a loopback stub and whose
// subject is everything else a command says.
func withoutOriginNotice(stderr string) string {
	var kept []string
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if strings.HasPrefix(line, "jevlin: ") && strings.Contains(line, " is not the default ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "")
}

// The notice withoutOriginNotice strips is exactly one line naming the router,
// and nothing a machine caller would parse.
func TestWithoutOriginNoticeStripsOnlyTheNotice(t *testing.T) {
	var b strings.Builder
	noteNonDefaultOrigin(&b, "http://127.0.0.1:9", "router", config.DefaultRouterURL, "the configuration")
	if got := withoutOriginNotice(b.String() + "jevlin: something else\n"); got != "jevlin: something else\n" {
		t.Errorf("stripped %q", got)
	}
}
