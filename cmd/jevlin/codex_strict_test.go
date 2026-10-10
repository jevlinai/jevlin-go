package main

// The strict check is held to Codex's own verdicts.
//
// Each shape below was handed to codex-cli 0.158.0 on Linux on 2026-10-10 as
// the whole of a scratch CODEX_HOME's config.toml, and `codex features list`
// either listed the features (accept) or stopped with "failed to load
// bootstrap configuration ... TOML parse error" or "duplicate key" (refuse).
// Two shapes it accepted as TOML and then refused for a reason that is not
// syntax ("config defines [permissions] profiles but does not set
// default_permissions") are recorded as accepted, because that is the
// parser's verdict, which is the one this check stands in for. The verdicts
// are Codex's; nothing here was decided by reading the TOML specification.

import (
	"strings"
	"testing"
)

var codexTOMLVerdicts = []struct {
	name   string
	text   string
	accept bool
}{
	{"inline features, then a subtable", "model = \"gpt-5\"\nfeatures = { memories = true }\n\n[features.network_proxy]\nenabled = true\n", false},
	{"root dotted features, then a subtable", "features.memories = true\n\n[features.network_proxy]\nenabled = true\n", true},
	{"root dotted features, then its header", "features.memories = true\n\n[features]\nnetwork_proxy = true\n", false},
	{"dotted network_proxy, then its header", "[features]\nnetwork_proxy.enabled = false\n\n[features.network_proxy]\nenabled = true\n", false},
	{"inline network_proxy, then its header", "[features]\nnetwork_proxy = { enabled = true }\n\n[features.network_proxy]\nenabled = true\n", false},
	{"inline permissions, then a profile header", "permissions = { }\n\n[permissions.jevlin]\nextends = \":workspace\"\n", false},
	{"dotted profile in [permissions], then its header", "[permissions]\njevlin.extends = \":workspace\"\n\n[permissions.jevlin]\nextends = \":workspace\"\n", false},
	{"dotted filesystem in a profile, then its header", "[permissions.work]\nextends = \":workspace\"\nfilesystem.\"/home/u/src\" = \"write\"\n\n[permissions.work.filesystem]\n\"/home/u/x\" = \"write\"\n", false},
	{"a header, then a dotted child", "[permissions.work]\nextends = \":workspace\"\n\n[permissions.work.filesystem]\n\"/home/u/src\" = \"write\"\n", true},
	{"dotted features inside another table", "[tui]\nfeatures.x = 1\n\n[features.network_proxy]\nenabled = true\n", true},
	{"root dotted profile, then a subtable", "permissions.jevlin.extends = \":workspace\"\n\n[permissions.jevlin.filesystem]\n\"/home/u/x\" = \"write\"\n", true},
	{"the same header twice", "[features]\na = true\n\n[features]\nb = true\n", false},
	{"a subtable before its parent", "[features.network_proxy]\nenabled = true\n\n[features]\nmemories = true\n", true},
}

func TestTheStrictCheckAgreesWithCodex(t *testing.T) {
	for _, v := range codexTOMLVerdicts {
		t.Run(v.name, func(t *testing.T) {
			err := codexTOMLError(v.text)
			if (err == nil) != v.accept {
				t.Errorf("strict check: accept=%v (%v); Codex: accept=%v", err == nil, err, v.accept)
			}
		})
	}
}

// R1's first shape, through the real install: a participant's inline
// features table. Our header would extend it; the install says which table
// collides, writes nothing for Codex, and leaves the file as it was.
func TestInstallRefusesAHeaderThatWouldExtendAnInlineTable(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	before := "model = \"gpt-5\"\nfeatures = { memories = true }\n\n[tui]\nscreen_reader_detection_done = true\n"
	m.files[codexConfigPath] = []byte(before)
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code == exitOK {
		t.Errorf("install exited 0 over a file it could not write safely:\n%s", out)
	}
	if got := string(m.files[codexConfigPath]); got != before {
		t.Errorf("install wrote a file Codex would refuse:\n%s", got)
	}
	if !strings.Contains(out, "your config defines features inline, and adding jevlin's tables beside it") {
		t.Errorf("the refusal does not name the colliding table:\n%s", out)
	}
	if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; ok {
		t.Errorf("the skill was written though nothing could be written for Codex:\n%s", out)
	}
}

// A shape Codex accepts is not refused: root dotted features beside our
// subtable is TOML Codex loads.
func TestInstallWritesBesideADottedFeaturesKey(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.files[codexConfigPath] = []byte("features.memories = true\n\n[tui]\nscreen_reader_detection_done = true\n")
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	if err := codexTOMLError(string(m.files[codexConfigPath])); err != nil {
		t.Errorf("install wrote a file Codex refuses: %v\n%s", err, m.files[codexConfigPath])
	}
}

// Every golden this client renders for a starting state is a file Codex
// loads.
func TestEveryRenderedStateParsesAsCodexParsesIt(t *testing.T) {
	for _, st := range codexStates() {
		t.Run(st.name, func(t *testing.T) {
			_, _, _, _, got, _, _ := stateInstall(t, st)
			if err := codexTOMLError(got); err != nil {
				t.Errorf("Codex would refuse the rendered file: %v\n%s", err, got)
			}
		})
	}
}

// Codex loads a config.toml that starts with a UTF-8 byte-order mark (seen
// on 0.160.0), so install writes beside it and keeps it, uninstall gives
// the file back with it, and an installed file that gained one later is
// still uninstalled. The Windows block too.
func TestAByteOrderMarkIsKeptAndNotRefused(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			onCodexOS(t, goos)
			m, ops := newFakeMachine("codex")
			before := codexBOM + "model = \"gpt-5\"\n\n[tui]\nx = 1\n"
			m.files[codexConfigPath] = []byte(before)
			if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("install refused a file Codex loads: exit %d\n%s", code, out)
			}
			got := string(m.files[codexConfigPath])
			if !strings.HasPrefix(got, codexBOM) || strings.Count(got, codexBOM) != 1 || !strings.Contains(got, agentsMarkerBegin) {
				t.Errorf("install did not write beside the mark and keep it:\n%q", got)
			}
			if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, out)
			}
			if got := string(m.files[codexConfigPath]); got != before {
				t.Errorf("uninstall did not give the file back with its mark\n got %q\nwant %q", got, before)
			}
		})
	}
	t.Run("a mark gained after install", func(t *testing.T) {
		cfgPath, _ := sandboxTestConfig(t)
		m, ops := newFakeMachine("codex")
		m.files[codexConfigPath] = []byte("model = \"gpt-5\"\n")
		if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
			t.Fatalf("install: exit %d\n%s", code, out)
		}
		m.files[codexConfigPath] = append([]byte(codexBOM), m.files[codexConfigPath]...)
		if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
			t.Fatalf("uninstall: exit %d\n%s", code, out)
		}
		if got := string(m.files[codexConfigPath]); got != codexBOM+"model = \"gpt-5\"\n" {
			t.Errorf("uninstall left the region of a file that gained a mark: %q", got)
		}
	})
}

// The refusal names the participant's table that collides, not ours: an
// inline features table is "features inline"; a network_proxy table made
// by dotted keys is that table "with dotted keys".
func TestTheCollisionNamesTheParticipantsTable(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"features = { memories = true }\n", "features inline"},
		{"[features]\nnetwork_proxy.enabled = false\n", "[features.network_proxy] with dotted keys"},
		{"permissions = { }\n", "permissions inline"},
	} {
		got := codexHeaderCollisions(tc.text, codexProfileHeaders())
		if len(got) == 0 || got[0] != tc.want {
			t.Errorf("collisions over %q = %q, want %q first", tc.text, got, tc.want)
		}
	}
}

// A byte-order mark directly before the first table header: read with the
// mark, that header is not recognized as one, and the region would land
// after the tables, where its default_permissions is the last table's key.
func TestAByteOrderMarkBeforeTheFirstHeader(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	before := codexBOM + "[tui]\nx = 1\n"
	m.files[codexConfigPath] = []byte(before)
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	got := string(m.files[codexConfigPath])
	if !strings.HasPrefix(got, codexBOM+agentsMarkerBegin) {
		t.Errorf("the region is not before the first table:\n%q", got)
	}
	if doc, ok := decodeTOMLDoc(strings.TrimPrefix(got, codexBOM)); !ok || doc["default_permissions"] != codexProfileName {
		t.Errorf("default_permissions is not a top-level key:\n%s", got)
	}
}

// A run that commits only the safety form of our old block — no terminal
// and no -yes, or a typed no at Proceed? — writes the fallback, which is
// made from the file read without its byte-order mark; the mark Codex
// loads the file with goes back in front, once.
func TestTheFallbackKeepsTheByteOrderMark(t *testing.T) {
	var st codexState
	for _, s := range codexStates() {
		if s.name == "old-block" {
			st = s
		}
	}
	for _, tc := range []struct {
		name     string
		terminal bool
		stdin    string
	}{
		{"no terminal and no -yes", false, ""},
		{"a typed no at Proceed?", true, "n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, before := seedState(t, st)
			m.terminal = tc.terminal
			m.files[codexConfigPath] = []byte(codexBOM + before)
			runAgentsAt(t, ops, tc.stdin, "install", "-config", cfgPath, "-client", "codex")
			got := string(m.files[codexConfigPath])
			want := codexBOM + strings.Replace(before, "network_access = true\n", "", 1)
			if got != want {
				t.Errorf("the fallback did not keep the mark, or wrote more than the closed line\n got %q\nwant %q", got, want)
			}
		})
	}
}
