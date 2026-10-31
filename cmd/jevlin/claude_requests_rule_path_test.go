package main

import "testing"

// Claude Code normalizes a Windows path to POSIX form before matching a rule
// ("C:\Users\alice" becomes "/c/Users/alice"), and a rule outside the project
// is written with a second leading slash, so the rule names "//c/Users/...".
// The cases are strings, not filepath results, so they run on every OS.
func TestClaudeRulePathSpellsWindowsDrivesAsAFirstElement(t *testing.T) {
	for _, c := range []struct{ dir, want string }{
		{`C:\Users\alice\.jevlin\requests`, "//c/Users/alice/.jevlin/requests"},
		{`d:\home\u\.jevlin\requests`, "//d/home/u/.jevlin/requests"},
		{`C:/Users/alice/.jevlin/requests`, "//c/Users/alice/.jevlin/requests"},
		{"/home/u/.jevlin/requests", "//home/u/.jevlin/requests"},
	} {
		if got := claudeRulePath(c.dir); got != c.want {
			t.Errorf("claudeRulePath(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}
