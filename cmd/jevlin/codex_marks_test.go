package main

// The mark on a line of ours in a participant's own table: it names the
// installation that wrote it, uninstall takes back only its own, and a path
// that could end the comment is never written into one.

import (
	"testing"
)

// A config path that would end the comment cannot be written as one.
func TestAMarkRefusesAPathThatWouldEndTheComment(t *testing.T) {
	for _, p := range []string{"/home/u/a\nb/jevlin.toml", "/home/u/a\rb/jevlin.toml", "/home/u/a\u2028b/jevlin.toml", "/home/u/a\x00b/jevlin.toml"} {
		if m, err := codexMark(p, ""); err == nil {
			t.Errorf("codexMark(%q) = %q, want a refusal", p, m)
		}
	}
	m, err := codexMark(`/home/u/"quoted" \ dir/jevlin.toml`, "")
	if err != nil {
		t.Fatalf("a plain path was refused: %v", err)
	}
	if _, cfg, ok := parseCodexMark(`x = 1  ` + m); !ok || cfg != `/home/u/"quoted" \ dir/jevlin.toml` {
		t.Errorf("the mark does not read back to its path: %q (%v)", cfg, ok)
	}
}
