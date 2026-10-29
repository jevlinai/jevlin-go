package platform

import "testing"

// A printed claim or console link must be on the configured portal's own
// origin, exactly. There is no alias between portals: a link on another
// origin, a lookalike or the same host over http is refused, whichever portal
// is configured.
func TestAClaimLinkMustBeOnTheConfiguredPortalsOwnOrigin(t *testing.T) {
	cases := []struct {
		name, raw, base string
		ok              bool
	}{
		{"a claim link on the configured portal", "https://platform.jevlin.ai/claim/abc1", "https://platform.jevlin.ai", true},
		{"a console link on the configured portal", "https://platform.jevlin.ai/agents/a1", "https://platform.jevlin.ai", true},
		{"a link on a portal this client once used", "https://platform.nyks.dev/claim/abc1", "https://platform.jevlin.ai", false},
		{"the default portal's link for another portal", "https://platform.jevlin.ai/claim/abc1", "https://platform.nyks.dev", false},
		{"another origin", "https://evil.example/claim/abc1", "https://platform.jevlin.ai", false},
		{"a lookalike", "https://platform.jevlin.ai.evil.example/claim/abc1", "https://platform.jevlin.ai", false},
		{"the portal over http", "http://platform.jevlin.ai/claim/abc1", "https://platform.jevlin.ai", false},
	}
	for _, c := range cases {
		err := validatePlatformURL(c.raw, c.base)
		if (err == nil) != c.ok {
			t.Errorf("%s: validatePlatformURL(%q, %q) = %v, want ok=%v", c.name, c.raw, c.base, err, c.ok)
		}
	}
}
