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
	if !strings.Contains(out, "your config defines [features.network_proxy] inline or with dotted keys") {
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
