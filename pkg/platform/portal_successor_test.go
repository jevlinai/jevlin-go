package platform

import "testing"

// The portal moved from platform.nyks.dev to platform.jevlin.ai, and the
// platform now issues claim and console links on the new host. Every
// installation setup made before the move names the old one as
// platform.base_url, and setup never rewrites an existing config, so the
// retired default accepts exactly its one compiled successor. Nothing else
// widens: another origin is refused for the retired default, the successor
// is not accepted for any other base, and the alias does not run backward.
func TestTheRetiredPortalDefaultAcceptsItsSuccessorAndNothingElse(t *testing.T) {
	cases := []struct {
		name, raw, base string
		ok              bool
	}{
		{"a successor claim link for the retired default", "https://platform.jevlin.ai/claim/abc1", "https://platform.nyks.dev", true},
		{"a successor console link for the retired default", "https://platform.jevlin.ai/agents/a1", "https://platform.nyks.dev", true},
		{"the retired default's own origin still", "https://platform.nyks.dev/claim/abc1", "https://platform.nyks.dev", true},
		{"another origin for the retired default", "https://evil.example/claim/abc1", "https://platform.nyks.dev", false},
		{"a lookalike of the successor", "https://platform.jevlin.ai.evil.example/claim/abc1", "https://platform.nyks.dev", false},
		{"the successor over http", "http://platform.jevlin.ai/claim/abc1", "https://platform.nyks.dev", false},
		{"the successor for a base of the participant's own", "https://platform.jevlin.ai/claim/abc1", "https://portal.example", false},
		{"the retired origin for the new default", "https://platform.nyks.dev/claim/abc1", "https://platform.jevlin.ai", false},
	}
	for _, c := range cases {
		err := validatePlatformURL(c.raw, c.base)
		if (err == nil) != c.ok {
			t.Errorf("%s: validatePlatformURL(%q, %q) = %v, want ok=%v", c.name, c.raw, c.base, err, c.ok)
		}
	}
}
