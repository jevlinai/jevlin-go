package main

// The mark on a line of ours in a participant's own table: it names the
// installation that wrote it, uninstall takes back only its own, and a path
// that could end the comment is never written into one.

import (
	"strings"
	"testing"
)

// Marks name their installation: an uninstall takes back only its own
// lines from a participant's profile, and names the other installation's.
func TestUninstallTakesBackOnlyItsOwnMarkedLines(t *testing.T) {
	cfgA, _ := sandboxTestConfig(t)
	cfgB, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	m.files[codexConfigPath] = []byte(workProfile)
	for _, cfg := range []string{cfgA, cfgB} {
		if code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfg, "-client", "codex", "-yes"); code != exitOK {
			t.Fatalf("install %s: exit %d\n%s", cfg, code, out)
		}
	}
	both := string(m.files[codexConfigPath])
	if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgA, "-client", "codex", "-yes"); code != exitOK {
		t.Fatalf("uninstall A: exit %d\n%s", code, out)
	} else if !strings.Contains(out, "another installation marked") {
		t.Errorf("uninstall did not name the other installation's lines:\n%s", out)
	}
	left := string(m.files[codexConfigPath])
	if strings.Contains(left, mustTOMLString(cfgA)) {
		t.Errorf("A's marked lines survived A's uninstall:\n%s", left)
	}
	if strings.Count(left, mustTOMLString(cfgB)) != strings.Count(both, mustTOMLString(cfgB)) {
		t.Errorf("A's uninstall touched B's lines\n--- before ---\n%s\n--- after ---\n%s", both, left)
	}
}

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
