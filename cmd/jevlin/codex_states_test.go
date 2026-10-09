package main

// Every starting state of Codex's config.toml that the maintainer's decisions
// name (D3–D7, C4–C6), driven through the real `agents install` and
// `agents uninstall` on files, each held to a golden of the rendered file.
//
// A state's "before" is what a participant writes, so it is typed here; what
// Codex itself writes is never typed — those cases (C1, C2) read Codex's own
// output from testdata/codex/, as testdata/hermes keeps Hermes' dumper output.
//
// The goldens hold the file with the installation's paths replaced by
// placeholders (statePlaceholders), so one golden serves every runner.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexState is one starting state and what install does to it.
type codexState struct {
	name   string
	before string
	answer string // typed at the one question; "" when none is asked
	exit   int    // agents install's exit code
	// codexNothing: nothing is written for Codex at all (no skill either).
	nothing bool
}

const workProfile = `default_permissions = "work"

[permissions.work]
extends = ":workspace"

[permissions.work.filesystem]
"/home/u/src" = "write"
`

func codexStates() []codexState {
	return []codexState{
		{name: "fresh", before: "model = \"gpt-5\"\n\n[tui]\nscreen_reader_detection_done = true\n"},
		{name: "empty", before: ""},
		{name: "old-block", before: "[tui]\nscreen_reader_detection_done = true\n\n" + agentsMarkerBegin + "\n# Lets jevlin's search reach the router and record its mining\n# observation under your jevlin home. Without this, Codex's default\n# sandbox blocks the write and searches earn nothing.\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [STATEROOTS]\n[hooks.state.\"/home/u/.codex/hooks.json:PreToolUse:0:0\"]\ntrusted_hash = \"sha256:0\"\n" + agentsMarkerEnd + "\n"},
		{name: "own-profile-network-absent", before: workProfile, answer: "y"},
		{name: "own-profile-network-off", before: workProfile + "\n[permissions.work.network]\nenabled = false\n", answer: "y"},
		{name: "own-profile-network-open", before: workProfile + "\n[permissions.work.network]\nenabled = true\n", answer: "y"},
		{name: "features-network-proxy-false", before: "[features]\nmemories = true\nnetwork_proxy = false\n", answer: "y"},
		{name: "features-network-proxy-true", before: "[features]\nnetwork_proxy = true\n"},
		{name: "default-permissions-workspace", before: "default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n", answer: "y"},
		{name: "sandbox-mode-workspace-write", before: "sandbox_mode = \"workspace-write\"\nmodel = \"gpt-5\"\n", answer: "y"},
		{name: "sandbox-mode-read-only", before: "sandbox_mode = \"read-only\"\n", exit: exitTransport, nothing: true},
		{name: "default-permissions-read-only", before: "default_permissions = \":read-only\"\n", exit: exitTransport, nothing: true},
		{name: "own-sandbox-table", before: "[sandbox_workspace_write]\nnetwork_access = false\n", exit: exitTransport, nothing: true},
		{name: "danger-full-access", before: "sandbox_mode = \"danger-full-access\"\n"},
	}
}

// stateInstall seeds config.toml, runs a real install at a terminal with
// answer typed (or nothing, when none is expected), and returns the file and
// the output.
func stateInstall(t *testing.T, st codexState) (m *fakeMachine, ops agentOps, cfgPath, home, got, out string, code int) {
	t.Helper()
	cfgPath, home = sandboxTestConfig(t)
	m, ops = newFakeMachine("codex")
	m.terminal = true
	before := strings.ReplaceAll(st.before, "STATEROOTS", quotedRootsOf(t, cfgPath))
	if st.before != "" || st.name == "empty" {
		m.files[codexConfigPath] = []byte(before)
	}
	stdin := ""
	if st.answer != "" {
		stdin = st.answer + "\n"
	}
	code, out = runAgentsAt(t, ops, stdin, "install", "-config", cfgPath, "-yes")
	return m, ops, cfgPath, home, string(m.files[codexConfigPath]), out, code
}

func quotedRootsOf(t *testing.T, cfgPath string) string {
	t.Helper()
	roots := codexSandboxRoots(binEntry{cfg: cfgPath}, noEnv)
	q := make([]string, len(roots))
	for i, r := range roots {
		q[i] = mustTOMLString(r)
	}
	return strings.Join(q, ", ")
}

// statePlaceholders turns a rendered file into its golden spelling: the
// installation's home and config path, in the quoting the renderer used.
func statePlaceholders(file, home, cfgPath string) string {
	for _, p := range []struct{ from, to string }{
		{mustTOMLString(cfgPath), `"CONFIG"`},
		{strings.Trim(mustTOMLString(home), `"`), "HOME"},
	} {
		file = strings.ReplaceAll(file, p.from, p.to)
	}
	return strings.ReplaceAll(file, `HOME\\`, "HOME/")
}

func stateGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex", "states", name+".after.toml")) // #nosec G304 -- a fixed fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each state's rendered file is its golden, byte for byte.
func TestCodexStartingStatesRenderTheirGoldens(t *testing.T) {
	for _, st := range codexStates() {
		t.Run(st.name, func(t *testing.T) {
			m, _, cfgPath, home, got, out, code := stateInstall(t, st)
			if code != st.exit {
				t.Fatalf("install exit %d, want %d\n%s", code, st.exit, out)
			}
			if want := stateGolden(t, st.name); statePlaceholders(got, home, cfgPath) != want {
				t.Errorf("rendered config.toml differs from testdata/codex/states/%s.after.toml\n--- got ---\n%s\n--- want ---\n%s\n--- output ---\n%s",
					st.name, statePlaceholders(got, home, cfgPath), want, out)
			}
			_, skill := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]
			if skill == st.nothing {
				t.Errorf("skill written = %v, want %v\n%s", skill, !st.nothing, out)
			}
			if _, ok := decodeTOMLDoc(got); !ok {
				t.Errorf("the rendered file does not decode:\n%s", got)
			}
		})
	}
}

// Every state goes back on uninstall to the participant's exact bytes, and a
// second install after the first writes nothing.
func TestCodexStartingStatesRoundTrip(t *testing.T) {
	for _, st := range codexStates() {
		if st.name == "old-block" {
			continue // migrated: there is no going back to the open network
		}
		t.Run(st.name, func(t *testing.T) {
			m, ops, cfgPath, _, installed, out, _ := stateInstall(t, st)
			before := strings.ReplaceAll(st.before, "STATEROOTS", quotedRootsOf(t, cfgPath))
			if code, again := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); string(m.files[codexConfigPath]) != installed {
				t.Errorf("a second install changed the file (exit %d)\n%s", code, again)
			}
			if code, rm := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, rm)
			}
			got, present := m.files[codexConfigPath]
			if !present && st.before == "" && st.name != "empty" {
				return
			}
			if string(got) != before {
				t.Errorf("uninstall did not return the participant's bytes\n--- got ---\n%q\n--- want ---\n%q\n--- install output ---\n%s", got, before, out)
			}
		})
	}
}
