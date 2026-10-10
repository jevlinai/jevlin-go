package main

// Our markers are lines of the file's own structure, not text that happens
// to contain them.

import (
	"strings"
	"testing"
)

// A participant's comment that ends in our begin marker is not our block.
// Matched anywhere in the file, it made the first install's region
// unreadable to every later install and uninstall.
func TestAMarkerInsideAParticipantsCommentIsNotOurBlock(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	before := "model = \"gpt-5\"  # " + agentsMarkerBegin + "\n\n[tui]\nscreen_reader_detection_done = true\n"
	m.files[codexConfigPath] = []byte(before)
	for i, sub := range []string{"install", "install", "uninstall"} {
		if code, out := runAgentsAt(t, ops, "", sub, "-config", cfgPath, "-yes"); code != exitOK {
			t.Fatalf("%s #%d: exit %d\n%s", sub, i+1, code, out)
		}
	}
	if got := string(m.files[codexConfigPath]); got != before {
		t.Errorf("install, install, uninstall did not return the participant's bytes:\n%q", got)
	}
}

// Our block's exact text inside a multi-line string is the participant's
// string. Uninstall leaves it, and install writes a block of its own.
func TestOurBlocksTextInsideAStringIsNotOurBlock(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	region := string(m.files[codexConfigPath])
	before := "notes = '''\n\n" + region + "'''\n\n[tui]\nscreen_reader_detection_done = true\n"
	if _, ok := decodeTOMLDoc(before); !ok {
		t.Fatalf("the fixture is not TOML:\n%s", before)
	}
	m.files[codexConfigPath] = []byte(before)
	if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if got := string(m.files[codexConfigPath]); got != before {
		t.Errorf("uninstall rewrote the participant's string:\n%s", got)
	}
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install over the string: exit %d\n%s", code, out)
	}
	installed := string(m.files[codexConfigPath])
	if strings.Count(installed, agentsMarkerBegin) != 2 || !strings.Contains(installed, "notes = '''\n\n"+region+"'''") {
		t.Errorf("install did not write a block of its own beside the untouched string:\n%s", installed)
	}
	if doc, ok := decodeTOMLDoc(installed); !ok || doc["notes"] != "\n"+region {
		t.Errorf("the participant's string changed:\n%s", installed)
	}
}

// The scanner's view of which lines are structure.
func TestLinesOutsideTOMLStrings(t *testing.T) {
	text := strings.Join([]string{
		`a = 1 # a "comment" with ''' in it`, // 0: structure; the quotes are in a comment
		`b = """`,                            // 1: structure; opens a string
		`inside \"""`,                        // 2: inside (escaped quote does not close)
		`still"""`,                           // 3: inside at its start; closes here
		`c = 'x""" not a string'`,            // 4: structure
		`d = '''`,                            // 5: structure; opens a literal string
		`# not a comment`,                    // 6: inside
		`'''`,                                // 7: inside at its start; closes here
		`[t]`,                                // 8: structure
	}, "\n")
	var got []string
	for _, l := range linesOutsideTOMLStrings(text) {
		got = append(got, text[l.start:l.end])
	}
	want := []string{`a = 1 # a "comment" with ''' in it`, `b = """`, `c = 'x""" not a string'`, `d = '''`, `[t]`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("structure lines\n got %q\nwant %q", got, want)
	}
}
