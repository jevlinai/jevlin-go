package main

// Installing and removing Codex's hooks (issue #19).
//
// Codex keys a hook's approval by where the hook sits — the key Codex writes
// is "<hooks.json>:<event>:<i>:<j>" — and by what it runs. So beyond writing
// the entries and taking them away again, two things are held here: a second
// install writes nothing at all, and a changed entry of ours is rewritten
// where it stands, so no participant's hook is renumbered by ours. Most cases
// plan for Linux's declared runner directly, so they hold on every runner;
// the ones that go through `agents install` say what Windows does instead.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// planCodexHooksFor plans Codex's hooks for goos on this fake machine and
// commits them, returning the plan.
func planCodexHooksFor(t *testing.T, ops agentOps, entry binEntry, goos string) agentPlan {
	t.Helper()
	var p agentPlan
	(codexTarget{}).planHooks(ops, ops.paths(noEnv), entry, goos, &p)
	if failures := commitPlan(ops, &p, io.Discard, io.Discard); failures != 0 {
		t.Fatalf("committing Codex's hooks: %d failures", failures)
	}
	return p
}

// codexEventLists decodes hooks.json into each event's list.
func codexEventLists(t *testing.T, b []byte) map[string][]any {
	t.Helper()
	var doc struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("hooks.json does not decode: %v\n%s", err, b)
	}
	return doc.Hooks
}

// Where nothing has established what runs a Codex hook (Windows), install
// writes no hooks.json and says so, and it does not refuse: Codex there gets
// the skill and the sandbox block it always had, and an install that worked
// before must not start exiting non-zero.
func TestCodexOnAnUndeclaredHookRunnerWritesNoHooksAndExitsZero(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			_, ops := newFakeMachine()
			var p agentPlan
			if (codexTarget{}).planHooks(ops, ops.paths(noEnv), goldenEntry(), goos, &p) {
				t.Fatal("planned hooks where no runner is established")
			}
			if len(p.writes) != 0 || len(p.refused) != 0 || refusedExit(&p) != exitOK {
				t.Errorf("writes %d, refused %v, exit %d; want no write, no refusal, exit 0", len(p.writes), p.refused, refusedExit(&p))
			}
			if len(p.notes) != 1 || !strings.Contains(p.notes[0], "not established") || !strings.Contains(p.notes[0], "skill and the sandbox block") {
				t.Errorf("notes %q, want the one sentence that says why there are no hooks", p.notes)
			}
		})
	}
	// The control: Linux plans them.
	_, ops := newFakeMachine()
	var p agentPlan
	if !(codexTarget{}).planHooks(ops, ops.paths(noEnv), goldenEntry(), "linux", &p) || len(p.writes) != 1 {
		t.Fatalf("Linux planned no hooks either, so the cases above prove nothing: %+v", p)
	}
}

// A second install writes nothing: not the file, not a note about approving.
// Codex approves a hook by what it runs, and an install that rewrote the
// entries each time would at best churn the file and at worst cost the
// approval.
func TestASecondCodexInstallWritesNothing(t *testing.T) {
	_, ops := newFakeMachine()
	planCodexHooksFor(t, ops, goldenEntry(), "linux")
	before, err := ops.readFile(ops.paths(noEnv).codexHooks)
	if err != nil {
		t.Fatal(err)
	}
	p := planCodexHooksFor(t, ops, goldenEntry(), "linux")
	if len(p.writes) != 0 || len(p.notes) != 0 {
		t.Errorf("a second install planned writes %d and notes %q", len(p.writes), p.notes)
	}
	if after, _ := ops.readFile(ops.paths(noEnv).codexHooks); !bytes.Equal(after, before) {
		t.Errorf("hooks.json changed on a second install:\n%s", after)
	}
}

// An entry of ours that is out of date — another spelling, an older
// command — is rewritten where it stands. Codex numbers each event's hooks
// by place and keys their approvals by it, so a participant's hook after
// ours keeps its place and the one before it keeps its own; an event that has
// no entry of ours gets one at the end, behind every hook already there.
func TestAReplacedCodexHookKeepsItsIndex(t *testing.T) {
	m, ops := newFakeMachine()
	entry := goldenEntry()
	stale, err := entry.hookCommandForShell(shellPOSIX, "codex", "PreToolUse")
	if err != nil {
		t.Fatal(err)
	}
	stale = strings.Replace(stale, "'", `"`, 2) // ours, spelled as an earlier renderer might have
	if !hookCommandIsOurs(stale, refFor(entry)) {
		t.Fatalf("the stale spelling %q is not recognized as ours, so replacing it proves nothing", stale)
	}
	mine := func(cmd string) map[string]any {
		return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}
	}
	path := ops.paths(noEnv).codexHooks
	m.files[slash(path)] = mustJSON(t, map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{mine("audit-before"), map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": stale}}}, mine("audit-after")},
		"Stop":       []any{mine("say done")},
	}})
	p := planCodexHooksFor(t, ops, entry, "linux")
	if len(p.writes) != 1 {
		t.Fatalf("planned %d writes, want the one hooks.json rewrite", len(p.writes))
	}
	lists := codexEventLists(t, m.files[slash(path)])
	pre := lists["PreToolUse"]
	if len(pre) != 3 || !entryIsOurs(pre[1], refFor(entry)) || !sameJSONValue(pre[1], codexWant(t, entry, "PreToolUse")) {
		t.Fatalf("PreToolUse after the refresh: %v; want ours rewritten at index 1", pre)
	}
	if !reflect.DeepEqual(pre[0], decodeAny(t, mine("audit-before"))) || !reflect.DeepEqual(pre[2], decodeAny(t, mine("audit-after"))) {
		t.Errorf("a participant's hook moved or changed around ours: %v", pre)
	}
	stop := lists["Stop"]
	if len(stop) != 2 || !reflect.DeepEqual(stop[0], decodeAny(t, mine("say done"))) || !entryIsOurs(stop[1], refFor(entry)) {
		t.Errorf("Stop after the install: %v; want the participant's hook first and ours appended", stop)
	}
	if !slices.ContainsFunc(p.notes, func(n string) bool { return strings.Contains(n, "also holds hooks jevlin did not write") }) {
		t.Errorf("the plan did not say the file holds others' hooks: %q", p.notes)
	}
}

func codexWant(t *testing.T, entry binEntry, event string) map[string]any {
	t.Helper()
	spec, err := codexHooks(entry, shellPOSIX)
	if err != nil {
		t.Fatal(err)
	}
	return spec.entries[event]
}

func decodeAny(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(mustJSON(t, v), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Uninstall removes ours and nothing else, leaves Codex's record of the
// approvals alone and says so, and — when a hook of someone else's followed
// ours — says that it moves up a place, which is where Codex keys its
// approval.
func TestCodexUninstallLeavesTheApprovalsAndSaysWhatMoves(t *testing.T) {
	m, ops := newFakeMachine()
	entry := goldenEntry()
	planCodexHooksFor(t, ops, entry, "linux")
	path := ops.paths(noEnv).codexHooks
	lists := codexEventLists(t, m.files[slash(path)])
	lists["Stop"] = append(lists["Stop"], map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}})
	m.files[slash(path)] = mustJSON(t, map[string]any{"hooks": lists})

	var p agentPlan
	(codexTarget{}).PlanUninstall(ops, ops.paths(noEnv), entry, noEnv, &p)
	notes := strings.Join(p.notes, "\n")
	for _, want := range []string{"record of your approval of these hooks stays in", "move up one place"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the plan did not say %q:\n%s", want, notes)
		}
	}
	if failures := commitPlan(ops, &p, io.Discard, io.Discard); failures != 0 {
		t.Fatalf("uninstall: %d failures", failures)
	}
	left := codexEventLists(t, m.files[slash(path)])
	if len(left) != 1 || len(left["Stop"]) != 1 || entryIsOurs(left["Stop"][0], refFor(entry)) {
		t.Errorf("hooks.json after uninstall: %v, want only the participant's Stop hook", left)
	}

	// With nothing of anyone else's after ours, nothing moves and nothing is
	// said about it.
	_, ops = newFakeMachine()
	planCodexHooksFor(t, ops, entry, "linux")
	var q agentPlan
	(codexTarget{}).PlanUninstall(ops, ops.paths(noEnv), entry, noEnv, &q)
	if strings.Contains(strings.Join(q.notes, "\n"), "move up one place") {
		t.Errorf("said something moves where nothing follows ours: %q", q.notes)
	}
}

// codexEntryEvent is a second statement of what codexHooks installs, held to
// it by the installer's own output: every event the install writes runs
// `hook … codex <that event>`, and the gate admits exactly those events.
func TestTheCodexGateNamesExactlyTheEventsTheInstallWrites(t *testing.T) {
	entry := binEntry{command: "/opt/x/jevlin", cfg: "/opt/x/jevlin.toml"}
	spec, err := codexHooks(entry, shellPOSIX)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range spec.order {
		hooks, _ := spec.entries[event]["hooks"].([]any)
		if len(hooks) != 1 {
			t.Fatalf("%s: %d hook commands, want 1", event, len(hooks))
		}
		installed, _ := hooks[0].(map[string]any)["command"].(string)
		want, err := entry.hookCommandForShell(shellPOSIX, "codex", event)
		if err != nil {
			t.Fatal(err)
		}
		if installed != want {
			t.Errorf("the install writes %s -> %q, want `hook … codex %s`", event, installed, event)
		}
	}
	// Every event Codex was seen to fire or its binary names.
	for _, event := range []string{"PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact", "PostCompact",
		"SessionStart", "SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop", "Interrupt"} {
		got, gated := codexEntryEvent([]string{"codex", event})
		if installed := slices.Contains(spec.order, event); gated != installed || (gated && got != event) {
			t.Errorf("%s: installed %v, gated %v as %q", event, installed, gated, got)
		}
	}
}

// The commands Codex's install writes, as literals. Codex approves a hook by
// what it runs, so any change to one of these strings is a change every
// participant has to approve again, and that has to be a reviewed diff here,
// not a side effect of a renderer change.
func TestTheCodexHookCommandIsPinned(t *testing.T) {
	spec, err := codexHooks(binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}, shellPOSIX)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PreToolUse":   `'/home/u/.jevlin/bin/jevlin' hook -config '/home/u/.jevlin/jevlin.toml' codex PreToolUse`,
		"SessionStart": `'/home/u/.jevlin/bin/jevlin' hook -config '/home/u/.jevlin/jevlin.toml' codex SessionStart`,
		"PreCompact":   `'/home/u/.jevlin/bin/jevlin' hook -config '/home/u/.jevlin/jevlin.toml' codex PreCompact`,
		"PostCompact":  `'/home/u/.jevlin/bin/jevlin' hook -config '/home/u/.jevlin/jevlin.toml' codex PostCompact`,
		"Stop":         `'/home/u/.jevlin/bin/jevlin' hook -config '/home/u/.jevlin/jevlin.toml' codex Stop`,
	}
	if len(spec.order) != len(want) {
		t.Fatalf("the install writes %d events, the pin names %d", len(spec.order), len(want))
	}
	for _, event := range spec.order {
		group := spec.entries[event]
		h, _ := group["hooks"].([]any)[0].(map[string]any)
		if h["command"] != want[event] || h["type"] != "command" || h["timeout"] != codexHookTimeout {
			t.Errorf("%s: %v, want command %q, type command, timeout %d", event, h, want[event], codexHookTimeout)
		}
		matcher, hasMatcher := group["matcher"]
		if (event == "PreToolUse") != hasMatcher || (hasMatcher && matcher != "*") {
			t.Errorf("%s: matcher %v (present %v); want \"*\" on PreToolUse alone, the shape the live runs used", event, matcher, hasMatcher)
		}
	}
}

// An upgrade re-renders the hosts this installation owns. For Codex's
// hooks.json that must change nothing at all: the command is the binary's
// path, the config's path and the event word, which an upgrade moves none
// of, so an approval given before the upgrade still matches after it. The
// file is re-saved compactly after the install, as Codex or a participant
// might, so that a rewrite of any kind would show in its bytes.
func TestAnUpgradeReRenderLeavesCodexHooksByteIdentical(t *testing.T) {
	f := newUpgradeFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}))
	r := &renderingRunner{f: f}
	f.runnerOverride = r
	f.agents.lookPath = func(name string) (string, error) {
		if name == "codex" {
			return filepath.Join(f.agents.home, "path", name), nil
		}
		return "", errors.New("not found")
	}
	paths := f.agents.paths(envOf(f.env))
	writeFileT(t, paths.codexHooks, `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/usr/local/bin/audit","timeout":5}]}]}}`+"\n")
	cfg := filepath.Join(f.home, setupConfigFile)
	var out, errOut bytes.Buffer
	if code := agentsMain(f.agents, []string{"install", "-yes", "-config", cfg, "-client", "codex"}, strings.NewReader(""), &out, &errOut, envOf(f.env)); code != exitOK {
		t.Fatalf("agents install: exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	installed, err := os.ReadFile(paths.codexHooks) // #nosec G304 -- this test's own sandbox
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(installed, &doc); err != nil {
		t.Fatal(err)
	}
	compact := append(mustJSON(t, doc), '\n')
	writeFileT(t, paths.codexHooks, string(compact))
	if codexServesHere() && !hooksHaveOurs(f.agents, paths.codexHooks, binEntry{command: f.exe, cfg: cfg}) {
		t.Fatal("the install wrote no hook of ours, so keeping the file proves nothing")
	}

	code, upOut, upErr := f.run()
	if code != exitOK {
		t.Fatalf("upgrade: exit %d\n%s\n%s", code, upOut, upErr)
	}
	if len(r.calls) != 1 || !strings.Contains(strings.Join(r.calls[0].args, " "), "-client codex") {
		t.Fatalf("re-render calls %+v, want one that refreshes Codex", r.calls)
	}
	if after, _ := os.ReadFile(paths.codexHooks); !bytes.Equal(after, compact) { // #nosec G304 -- this test's own sandbox
		t.Errorf("the upgrade's re-render changed hooks.json\n got %s\nwant %s", after, compact)
	}
}

// A `version` key is ours to remove only for a host whose install writes one
// (Cursor's). Codex's install never does, so a hooks.json carrying one is
// someone else's too, and uninstall keeps it with its version and without
// our entries.
func TestCodexUninstallKeepsAHooksFileWithAVersionItNeverWrote(t *testing.T) {
	m, ops := newFakeMachine()
	entry := goldenEntry()
	planCodexHooksFor(t, ops, entry, "linux")
	path := slash(ops.paths(noEnv).codexHooks)
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = 1
	m.files[path] = mustJSON(t, doc)

	var p agentPlan
	(codexTarget{}).PlanUninstall(ops, ops.paths(noEnv), entry, noEnv, &p)
	if failures := commitPlan(ops, &p, io.Discard, io.Discard); failures != 0 {
		t.Fatalf("uninstall: %d failures", failures)
	}
	b, kept := m.files[path]
	if !kept {
		t.Fatalf("uninstall removed a hooks.json holding a version jevlin never writes for Codex (removes %v)", p.removedPaths())
	}
	var left map[string]any
	if err := json.Unmarshal(b, &left); err != nil || left["version"] == nil {
		t.Fatalf("the kept file lost its version: %s", b)
	}
	if lists := codexEventLists(t, b); len(lists) != 0 {
		t.Errorf("entries left after uninstall: %v", lists)
	}
}
