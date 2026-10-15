package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// shareOurStopGroup puts a handler of the participant's into the group
// jevlin wrote under Stop, beside jevlin's own, which is where Claude Code's
// /hooks lands a handler added under the same (empty) matcher.
func shareOurStopGroup(t *testing.T, m *fakeMachine, path, theirs string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	stop := doc["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 {
		t.Fatalf("precondition: %s has %d Stop groups, want jevlin's one", path, len(stop))
	}
	group := stop[0].(map[string]any)
	group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": theirs})
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	m.files[path] = b
}

// stopHandlers is every handler command under Stop, group by group.
func stopHandlers(t *testing.T, m *fakeMachine, path string) [][]string {
	t.Helper()
	var groups [][]string
	stop, _ := hooksOf(t, m, path)["Stop"].([]any)
	for _, g := range stop {
		var cmds []string
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			cmds = append(cmds, h.(map[string]any)["command"].(string))
		}
		groups = append(groups, cmds)
	}
	return groups
}

// Ownership is decided per handler, not per group. A handler of the
// participant's in the same group as jevlin's used to go with it: install
// replaced the whole group, uninstall dropped it and, with nothing else
// left, deleted the file, and the plan said nothing (issue #32). Now a
// reinstall keeps the participant's handler where it was and gives jevlin's
// a group of its own; a second reinstall writes nothing; and uninstall
// takes only jevlin's handler and keeps the file. Both hosts whose hook
// files group handlers, Claude Code and Codex, and in both cases the plan
// says what happened.
func TestAHandlerSharingOurGroupIsKeptByInstallAndUninstall(t *testing.T) {
	for _, host := range []struct{ client, path string }{
		{"claude", "/home/u/.claude/settings.json"},
		{"codex", "/home/u/.codex/hooks.json"},
	} {
		t.Run(host.client, func(t *testing.T) {
			m, ops := newFakeMachine(host.client)
			if code, out, _ := runAgents(t, ops, nil, "install", "-client", host.client, "-config", testCfg, "-yes"); code != exitOK {
				t.Fatalf("install: %d\n%s", code, out)
			}
			shareOurStopGroup(t, m, host.path, "say done")

			code, out, _ := runAgents(t, ops, nil, "install", "-client", host.client, "-config", testCfg, "-yes")
			if code != exitOK {
				t.Fatalf("reinstall: %d\n%s", code, out)
			}
			groups := stopHandlers(t, m, host.path)
			if len(groups) != 2 || len(groups[0]) != 1 || groups[0][0] != "say done" ||
				len(groups[1]) != 1 || !strings.Contains(groups[1][0], "jevlin") {
				t.Fatalf("Stop after reinstall = %q, want the participant's handler in its group, then jevlin's in a group of its own", groups)
			}
			if !strings.Contains(out, "shared a group") {
				t.Errorf("the reinstall did not say it split the group:\n%s", out)
			}

			before := string(m.files[host.path])
			if code, out, _ := runAgents(t, ops, nil, "install", "-client", host.client, "-config", testCfg, "-yes"); code != exitOK {
				t.Fatalf("third install: %d\n%s", code, out)
			}
			if string(m.files[host.path]) != before {
				t.Errorf("a second install after the split rewrote %s", host.path)
			}

			// Back to one shared group, and uninstall from there.
			m.files[host.path] = []byte(before)
			shareOurGroupAgain(t, m, host.path)
			code, out, _ = runAgents(t, ops, nil, "uninstall", "-client", host.client, "-config", testCfg, "-yes")
			if code != exitOK {
				t.Fatalf("uninstall: %d\n%s", code, out)
			}
			if _, ok := m.files[host.path]; !ok {
				t.Fatalf("uninstall deleted %s, which still held the participant's handler", host.path)
			}
			if groups := stopHandlers(t, m, host.path); len(groups) != 1 || len(groups[0]) != 1 || groups[0][0] != "say done" {
				t.Fatalf("Stop after uninstall = %q, want only the participant's handler", groups)
			}
			if !strings.Contains(out, "shared a group") {
				t.Errorf("the uninstall did not say it kept a handler from jevlin's group:\n%s", out)
			}
		})
	}
}

// shareOurGroupAgain merges the two Stop groups the split left back into
// one, jevlin's handler first: the shape uninstall meets when a participant
// adds a handler to jevlin's group after the last install.
func shareOurGroupAgain(t *testing.T, m *fakeMachine, path string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	stop := hooks["Stop"].([]any)
	theirs := stop[0].(map[string]any)["hooks"].([]any)
	ours := stop[1].(map[string]any)
	ours["hooks"] = append(ours["hooks"].([]any), theirs...)
	hooks["Stop"] = []any{ours}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	m.files[path] = b
}

// putInOurStopGroup adds a handler of the participant's to jevlin's Codex
// Stop group, before or after jevlin's own.
func putInOurStopGroup(t *testing.T, m *fakeMachine, path string, before bool) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatal(err)
	}
	group := doc["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)
	theirs := map[string]any{"type": "command", "command": "say done"}
	if before {
		group["hooks"] = append([]any{theirs}, group["hooks"].([]any)...)
	} else {
		group["hooks"] = append(group["hooks"].([]any), theirs)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	m.files[path] = b
}

// Codex keys an approval by an entry's place in its event's list and by a
// handler's place in its group. Inside a group jevlin's handler shares,
// removing jevlin's moves a handler listed after it up a place, so
// uninstall says so, and one listed before it does not move. The approval
// line cannot tell which key Codex holds for jevlin's handler there, so it
// says so rather than read the first place's record.
func TestCodexNamesTheRenumberingAndTheUnknownApprovalInASharedGroup(t *testing.T) {
	for _, c := range []struct {
		name         string
		theirsBefore bool
		wantShift    bool
	}{
		{"theirs after ours", false, true},
		{"theirs before ours", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ops, paths, entry := codexStatusMachine(t)
			putInOurStopGroup(t, m, paths.codexHooks, c.theirsBefore)
			if got := codexHooksAfterOurs(ops, paths.codexHooks, entry); got != c.wantShift {
				t.Errorf("codexHooksAfterOurs = %v, want %v", got, c.wantShift)
			}
			lines := codexApprovalLines(ops, paths, entry)
			if len(lines) != 1 || !strings.Contains(lines[0], "shares a group") {
				t.Errorf("approval lines = %q, want the shared group named as unknown", lines)
			}
		})
	}
}
