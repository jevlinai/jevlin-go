package main

// Codex's own entry points (issue #19), driven with the payloads Codex sent
// (codex_fixtures_test.go). A payload is never edited on disk: where a case
// needs this installation's search in place of the stand-in command the
// capture ran, or another key, it derives it here, in the open, by naming
// what it sets.
//
// Two ways in. Most cases call hookCodexOn for macOS's or Linux's
// declaration, so they hold on every runner, Windows included. The gate
// cases drive the real hookMain, where the decision lives, and there the OS
// is the runner's own: on one with no established runner for Codex's hooks
// (Windows) a Codex entry does nothing at all, and those cases assert exactly
// that there rather than skipping.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// codexTestInstall is enough of an installation for the hook to recognize
// its own search: a regular file named jevlin, which is all the identity
// check and the bridge's placement rule look at, and the config the hook was
// started with.
type codexTestInstall struct {
	bin, cfg string
}

func newCodexTestInstall(t *testing.T, cfg string) codexTestInstall {
	t.Helper()
	bin := filepath.Join(t.TempDir(), ".jevlin", "bin", exeName("jevlin"))
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("stand-in for the binary\n"), 0o700); err != nil { // #nosec G306 -- the test's own stand-in executable
		t.Fatal(err)
	}
	return codexTestInstall{bin: bin, cfg: cfg}
}

func (in codexTestInstall) entry() binEntry { return binEntry{command: in.bin, cfg: in.cfg} }

// search is the search Codex's skill renders for this installation on macOS
// and Linux: the POSIX heredoc, with the request inside it.
func (in codexTestInstall) search(t *testing.T) string {
	t.Helper()
	_, script, err := searchBlockForShell(shellPOSIX, in.entry(), `{"version":1,"query":"exact query text"}`)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func (in codexTestInstall) ops(env map[string]string) (*fakeHookFS, hookOps) {
	fs, ops := newFakeHookOps(env)
	ops.executable = func() (string, error) { return in.bin, nil }
	return fs, ops
}

// withCommand is a captured PreToolUse payload running command instead of
// the stand-in the capture ran. Nothing else changes.
func withCommand(m map[string]any, command string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	in := map[string]any{}
	if old, ok := m["tool_input"].(map[string]any); ok {
		for k, v := range old {
			in[k] = v
		}
	}
	in["command"] = command
	out["tool_input"] = in
	return out
}

// codexAnswer is a PreToolUse answer, decoded.
type codexAnswer struct {
	Out map[string]json.RawMessage `json:"hookSpecificOutput"`
}

func runCodexOn(t *testing.T, ops hookOps, hc hookContext, goos, event string, payload map[string]any) string {
	t.Helper()
	var stdout bytes.Buffer
	hookCodexOn(ops, hc, goos, event, mustJSON(t, payload), &stdout)
	return stdout.String()
}

// rewrittenCommand decodes a PreToolUse answer and returns the command it
// hands Codex, failing unless the answer is exactly the established shape.
func rewrittenCommand(t *testing.T, stdout string) string {
	t.Helper()
	var a codexAnswer
	if err := json.Unmarshal([]byte(stdout), &a); err != nil || a.Out == nil {
		t.Fatalf("no PreToolUse answer: %q", stdout)
	}
	var keys []string
	for k := range a.Out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"hookEventName", "permissionDecision", "updatedInput"}; !slices.Equal(keys, want) {
		t.Fatalf("answer keys %v, want exactly %v: the one shape a live run established", keys, want)
	}
	var event, decision string
	var input map[string]any
	if json.Unmarshal(a.Out["hookEventName"], &event) != nil || event != "PreToolUse" ||
		json.Unmarshal(a.Out["permissionDecision"], &decision) != nil || decision != "allow" ||
		json.Unmarshal(a.Out["updatedInput"], &input) != nil {
		t.Fatalf("answer %s", stdout)
	}
	cmd, _ := input["command"].(string)
	return cmd
}

var codexHC = hookContext{sessionsDir: "/sessions"}

func hcFor(in codexTestInstall) hookContext {
	hc := codexHC
	hc.cfgPath = in.cfg
	return hc
}

// ── what a Codex entry does ──────────────────────────────────────────────

// The search this installation's skill renders gets the bridge in front of it
// and an allow, and the answer carries every key of the tool's input with
// only the command changed.
func TestCodexRewritesAndAllowsOnlyTheRenderedSearch(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	search := in.search(t)
	payload := withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), search)
	// Derived: Codex's Bash input carried only `command` in every capture; a
	// second key shows the answer echoes the input rather than rebuilding it.
	payload["tool_input"].(map[string]any)["workdir"] = "/tmp/x"

	for _, goos := range []string{"darwin", "linux"} {
		_, ops := in.ops(nil)
		out := runCodexOn(t, ops, hcFor(in), goos, "PreToolUse", payload)
		cmd := rewrittenCommand(t, out)
		if assignment, rest, _ := strings.Cut(cmd, " "); rest != search || !strings.HasPrefix(assignment, bridgeEnv+"=") {
			t.Errorf("%s: want one bridge assignment in front of the search as written:\n%s", goos, cmd)
		}
		decodeBridgeFromCommand(t, cmd)
		var a codexAnswer
		_ = json.Unmarshal([]byte(out), &a)
		var input map[string]any
		_ = json.Unmarshal(a.Out["updatedInput"], &input)
		if input["workdir"] != "/tmp/x" || len(input) != 2 {
			t.Errorf("%s: the tool's other input did not come back as it went: %v", goos, input)
		}
	}

	// Everything else gets silence: no answer, and no file.
	other := newCodexTestInstall(t, "/home/u/dm-disposable/jevlin.toml")
	for _, c := range []struct{ name, command string }{
		{"the search with a second statement after it", strings.Replace(search, "\nJSON", "\nJSON\nrm -rf /tmp/x", 1)},
		{"the search with a command chained onto its end", search + " && rm -rf /tmp/x"},
		{"another installation's search", strings.Replace(other.search(t), other.bin, in.bin, 1)},
		{"a search by pattern only", "jevlin search -format model \"q\""},
		{"the human form", strings.TrimSuffix(in.entry().searchCommand(), " -format model") + " -format model \"q\""},
		{"the search after cd, as the captured command was", "cd /tmp && " + search},
		{"the search piped on, as the captured command was", search + " | cat"},
		{"the search in a loop, as the captured command was", "for i in 1; do " + search + "; done"},
		{"a command that is not ours", "echo hello-one"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs, ops := in.ops(nil)
			if out := runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), c.command)); out != "" || len(fs.files) != 0 {
				t.Fatalf("answered %q and wrote %v for a command that is not this installation's rendered search", out, keys(fs.files))
			}
		})
	}
}

// The matcher is "*", so the hook runs in front of every tool. Spawning an
// agent is the captured case; with this installation's search put into its
// input as well, it is the tool's name alone that keeps the hook out.
func TestCodexLineageIsSilentForEveryToolButBash(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	spawn := codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-spawn-agent.json")
	for _, c := range []struct {
		name    string
		payload map[string]any
	}{
		{"spawning an agent, as captured", spawn},
		{"spawning an agent whose input carries our search", withCommand(spawn, in.search(t))},
		{"a tool with no name", func() map[string]any {
			m := withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), in.search(t))
			delete(m, "tool_name")
			return m
		}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs, ops := in.ops(nil)
			if out := runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", c.payload); out != "" || len(fs.files) != 0 {
				t.Fatalf("answered %q and wrote %v for a tool that is not the shell", out, keys(fs.files))
			}
		})
	}
	// The control: the same search under the shell tool's name is answered.
	_, ops := in.ops(nil)
	if out := runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), in.search(t))); out == "" {
		t.Fatal("the control was not answered, so the silences above prove nothing")
	}
}

// Codex names a turn `turn_id`, not Claude Code's `prompt_id`. The envelope
// carries the turn and the call, each hashed with the session, under the
// harness `codex`, and no history: Codex's transcript is not read.
func TestCodexEnvelopeCarriesTurnAndCall(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	for _, name := range []string{"codex-0.158.0-linux-PreToolUse-bash.json", "codex-0.160.0-macos-PreToolUse-bash.json", "codex-0.160.0-macos-app-PreToolUse-bash.json"} {
		t.Run(name, func(t *testing.T) {
			payload := codexPayloadFixture(t, name)
			session, turn, call := payload["session_id"].(string), payload["turn_id"].(string), payload["tool_use_id"].(string)
			fs, ops := in.ops(nil)
			env := decodeBridgeFromCommand(t, rewrittenCommand(t, runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", withCommand(payload, in.search(t)))))
			want := traceEnvelope{V: traceVersion, Harness: "codex", SessionID: traceHash(session),
				TurnID: traceHash(session + "|" + turn), CallID: traceHash(session + "|" + call), Window: "none"}
			if !reflect.DeepEqual(*env, want) {
				t.Errorf("envelope\n got %+v\nwant %+v", *env, want)
			}
			l, ok := loadLineage(ops, lineagePath("/sessions", payload["cwd"].(string)))
			if !ok || l.Harness != "codex" || l.SessionID != want.SessionID || l.TurnID != want.TurnID || l.CallID != want.CallID || len(l.History) != 0 {
				t.Errorf("lineage file %+v (files %v)", l, keys(fs.files))
			}
		})
	}
	// A shell another host started exports its own harness, and that
	// declaration wins here as it does in Claude Code's hook.
	_, ops := in.ops(map[string]string{"JEVLIN_HARNESS": "outer-host"})
	env := decodeBridgeFromCommand(t, rewrittenCommand(t, runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse",
		withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), in.search(t)))))
	if env.Harness != "outer-host" {
		t.Errorf("harness %q, want the exported one", env.Harness)
	}
}

// A subagent's payload carries the PARENT's session id and its own agent id,
// a Codex thread id of its own. Its search runs in its own lane and names the
// lane it hangs off, which is the id the orchestrator's searches carry; the
// orchestrator's next search clears the pointer from the lineage file.
func TestACodexSubagentsSearchNamesItsParent(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	sub := codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash-subagent.json")
	session, agent := sub["session_id"].(string), sub["agent_id"].(string)
	if session == agent {
		t.Fatal("the capture's agent id is its session id, so nothing below could tell them apart")
	}
	_, ops := in.ops(nil)
	env := decodeBridgeFromCommand(t, rewrittenCommand(t, runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", withCommand(sub, in.search(t)))))
	if env.SessionID != traceHash(agent) || env.ParentSessionID != traceHash(session) {
		t.Errorf("subagent envelope: session %q parent %q, want %q under %q", env.SessionID, env.ParentSessionID, traceHash(agent), traceHash(session))
	}
	path := lineagePath("/sessions", sub["cwd"].(string))
	if l, ok := loadLineage(ops, path); !ok || l.ParentSessionID != traceHash(session) || l.SessionID != traceHash(agent) {
		t.Errorf("lineage file under the subagent: %+v", l)
	}

	// Derived: the same session's orchestrator, without the subagent's keys.
	top := withCommand(sub, in.search(t))
	delete(top, "agent_id")
	delete(top, "agent_type")
	env = decodeBridgeFromCommand(t, rewrittenCommand(t, runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", top)))
	if env.SessionID != traceHash(session) || env.ParentSessionID != "" {
		t.Errorf("orchestrator envelope: session %q parent %q", env.SessionID, env.ParentSessionID)
	}
	if l, ok := loadLineage(ops, path); !ok || l.ParentSessionID != "" || l.SessionID != traceHash(session) {
		t.Errorf("lineage file back under the orchestrator: %+v", l)
	}
}

// One generation per compaction, from the payloads Codex sent: a session
// starts, compacts (PreCompact, PostCompact, then a SessionStart whose source
// is "compact"), and its next search reads window 1. SessionStart and Stop
// start one flush each; nothing else does.
func TestCodexWindowCountsOneGenerationPerCompaction(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	fs, ops := in.ops(nil)
	compact := codexPayloadFixture(t, "codex-0.160.0-macos-SessionStart-compact.json")
	// Derived: that session's first SessionStart, which the compaction run
	// did not keep as a fixture.
	startup := map[string]any{}
	for k, v := range compact {
		startup[k] = v
	}
	startup["source"] = "startup"
	search := withCommand(codexPayloadFixture(t, "codex-0.160.0-macos-PreToolUse-bash.json"), in.search(t))
	search["session_id"] = compact["session_id"]

	window := func() string {
		t.Helper()
		return decodeBridgeFromCommand(t, rewrittenCommand(t, runCodexOn(t, ops, hcFor(in), "darwin", "PreToolUse", search))).Window
	}
	runCodexOn(t, ops, hcFor(in), "darwin", "SessionStart", startup)
	if w := window(); w != "none" {
		t.Fatalf("window before any compaction: %q", w)
	}
	for _, step := range []struct {
		event   string
		payload map[string]any
	}{
		{"PreCompact", codexPayloadFixture(t, "codex-0.160.0-macos-PreCompact-auto.json")},
		{"PostCompact", codexPayloadFixture(t, "codex-0.160.0-macos-PostCompact-auto.json")},
		{"SessionStart", compact},
	} {
		if out := runCodexOn(t, ops, hcFor(in), "darwin", step.event, step.payload); out != "" {
			t.Errorf("%s printed %q", step.event, out)
		}
	}
	if w := window(); w != "1" {
		t.Errorf("window after one compaction: %q, want 1", w)
	}
	if len(fs.flushes) != 2 {
		t.Errorf("%d flushes, want one per SessionStart", len(fs.flushes))
	}
	runCodexOn(t, ops, hcFor(in), "darwin", "Stop", codexPayloadFixture(t, "codex-0.158.0-linux-Stop.json"))
	if len(fs.flushes) != 3 {
		t.Errorf("Stop started %d flushes, want one", len(fs.flushes)-2)
	}
}

// Stop starts the flush and does nothing else, whatever the installation
// opted into: the turn's final message is the model's words, and Codex's
// turn end is not built. A turn mark that would let one be sent is put there
// first, so a turn end that read `turn_id` would find it.
func TestCodexStopStartsOneFlushAndQueuesNothing(t *testing.T) {
	stop := codexPayloadFixture(t, "codex-0.158.0-linux-Stop.json")
	hc := hookContext{cfgPath: "/home/u/.jevlin/jevlin.toml", sessionsDir: "/sessions", turnEnd: true}
	fs, ops := newFakeHookOps(nil)
	spawnedTurnEnds := 0
	ops.spawnTurnEnd = func(string, string) error { spawnedTurnEnds++; return nil }
	session, turn := stop["session_id"].(string), stop["turn_id"].(string)
	mark := turnSearchedPath("/sessions", traceHash(session+"|"+turn))
	fs.files[mark] = nil

	if out := runCodexOn(t, ops, hc, "linux", "Stop", stop); out != "" {
		t.Errorf("Stop printed %q; Codex reads a Stop hook's output as a decision", out)
	}
	if len(fs.flushes) != 1 || spawnedTurnEnds != 0 {
		t.Errorf("flushes %d, turn ends %d; want one flush and nothing else", len(fs.flushes), spawnedTurnEnds)
	}
	if got := keys(fs.files); len(got) != 1 || got[0] != mark {
		t.Errorf("files after Stop: %v, want only the untouched turn mark", got)
	}
	last, _ := stop["last_assistant_message"].(string)
	for p, b := range fs.files {
		if last != "" && bytes.Contains(b, []byte(last)) {
			t.Errorf("%s holds the turn's final message", p)
		}
	}
}

// Where nothing has established what runs a Codex hook, the install writes
// none, and an entry found there anyway was not written for that OS: every
// event, every payload, nothing at all.
func TestCodexHooksDoNothingWhereNoRunnerIsEstablished(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	cases := map[string]map[string]any{
		"PreToolUse":   withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), in.search(t)),
		"SessionStart": codexPayloadFixture(t, "codex-0.158.0-linux-SessionStart-startup.json"),
		"PreCompact":   codexPayloadFixture(t, "codex-0.160.0-macos-PreCompact-auto.json"),
		"PostCompact":  codexPayloadFixture(t, "codex-0.160.0-macos-PostCompact-auto.json"),
		"Stop":         codexPayloadFixture(t, "codex-0.158.0-linux-Stop.json"),
	}
	for _, goos := range []string{"windows", "freebsd"} {
		for event, payload := range cases {
			fs, ops := in.ops(nil)
			if out := runCodexOn(t, ops, hcFor(in), goos, event, payload); out != "" || len(fs.files) != 0 || len(fs.flushes) != 0 {
				t.Errorf("%s on %s: stdout %q files %v flushes %d", event, goos, out, keys(fs.files), len(fs.flushes))
			}
		}
	}
	// The control: the same payloads on Linux do something.
	fs, ops := in.ops(nil)
	for event, payload := range cases {
		runCodexOn(t, ops, hcFor(in), "linux", event, payload)
	}
	if len(fs.files) == 0 || len(fs.flushes) == 0 {
		t.Fatal("the control did nothing either, so the silences above prove nothing")
	}
}

// ── who is calling a Codex entry ─────────────────────────────────────────

// codexServesHere: does this runner's OS have an established runner for
// Codex's hooks? Where it does not, the real hookMain answers nothing to a
// Codex entry, and the cases below assert that instead of skipping.
func codexServesHere() bool {
	_, err := declaredShells(codexTarget{}, runtime.GOOS, channelHook)
	return err == nil
}

// runCodexEntry runs `hook -config <cfg> codex <event>` through the real
// hookMain, as an installation whose config names /sessions. A payload that
// carries a shell command has it replaced by that installation's own
// rendered search, so a hook that let the payload through would answer it:
// without that, a stand-down and a command that is simply not ours look the
// same.
func runCodexEntry(t *testing.T, event string, payload map[string]any) hookRun {
	t.Helper()
	in := newCodexTestInstall(t, writeHookConfig(t))
	if input, ok := payload["tool_input"].(map[string]any); ok {
		if _, shell := input["command"].(string); shell {
			payload = withCommand(payload, in.search(t))
		}
	}
	return runCodexEntryRaw(t, in, event, mustJSON(t, payload))
}

func runCodexEntryRaw(t *testing.T, in codexTestInstall, event string, raw []byte) hookRun {
	t.Helper()
	fs, ops := in.ops(nil)
	var stdout, stderr bytes.Buffer
	if code := hookMain(ops, []string{"-config", in.cfg, "codex", event}, bytes.NewReader(raw), &stdout, &stderr); code != exitOK {
		t.Fatalf("hook codex %s exited %d (stderr %q); a hook never fails its host", event, code, stderr.String())
	}
	files := keys(fs.files)
	sort.Strings(files)
	return hookRun{stdout: stdout.String(), files: files, flushes: len(fs.flushes)}
}

// Every captured payload, into the entry point Codex was configured to run it
// with, does what that entry is for.
func TestCodexEntriesServeTheRealCodexPayloads(t *testing.T) {
	for _, c := range []struct {
		fixture, event  string
		lineage, window bool
		flushes         int
	}{
		{"codex-0.158.0-linux-PreToolUse-bash.json", "PreToolUse", true, false, 0},
		{"codex-0.160.0-macos-PreToolUse-bash.json", "PreToolUse", true, false, 0},
		{"codex-0.160.0-macos-app-PreToolUse-bash.json", "PreToolUse", true, false, 0},
		{"codex-0.158.0-linux-PreToolUse-bash-subagent.json", "PreToolUse", true, false, 0},
		{"codex-0.158.0-linux-SessionStart-startup.json", "SessionStart", false, true, 1},
		{"codex-0.158.0-linux-SessionStart-resume.json", "SessionStart", false, true, 1},
		{"codex-0.160.0-macos-SessionStart-compact.json", "SessionStart", false, true, 1},
		{"codex-0.160.0-macos-PreCompact-auto.json", "PreCompact", false, true, 0},
		{"codex-0.160.0-macos-PostCompact-auto.json", "PostCompact", false, true, 0},
		{"codex-0.158.0-linux-Stop.json", "Stop", false, false, 1},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			payload := codexPayloadFixture(t, c.fixture)
			got := runCodexEntry(t, c.event, payload)
			if !codexServesHere() {
				if !got.nothing() {
					t.Fatalf("on %s, where no runner is established, a Codex entry did something: %+v", runtime.GOOS, got)
				}
				return
			}
			var want []string
			if c.lineage {
				want = append(want, lineagePath("/sessions", payload["cwd"].(string)))
				rewrittenCommand(t, got.stdout)
			} else if got.stdout != "" {
				t.Errorf("printed %q", got.stdout)
			}
			if c.window {
				want = append(want, filepath.Join("/sessions", hookStateFile))
			}
			sort.Strings(want)
			if !slices.Equal(got.files, want) || got.flushes != c.flushes {
				t.Errorf("files %v flushes %d, want %v and %d", got.files, got.flushes, want, c.flushes)
			}
		})
	}
}

// asClaudeCodeAtCodex is asClaudeCode's derivation with the one key Claude
// Code always sends and the Cursor capture may lack: a transcript, in Claude
// Code's own place for it.
func asClaudeCodeAtCodex(m map[string]any, event string) map[string]any {
	out := asClaudeCode(m, event)
	out["transcript_path"] = "/home/u/.claude/projects/-home-u-project/" + strings.TrimSpace(out["session_id"].(string)) + ".jsonl"
	return out
}

// Cursor's real payloads, and Claude Code's derived from them, into the Codex
// entry for the same event: nothing written, nothing spawned, nothing
// printed. Each carries this installation's search where it carries a
// command, so a hook that let it through would have answered it.
func TestCodexEntriesStandDownForAnotherHostsPayload(t *testing.T) {
	for _, c := range []struct{ fixture, codexEvent string }{
		{"cursor-3.20.21-preToolUse-runs-claude-lineage.json", "PreToolUse"},
		{"cursor-3.20.21-sessionStart-runs-claude-window-session-start.json", "SessionStart"},
		{"cursor-3.20.21-stop-runs-claude-flush.json", "Stop"},
	} {
		cursor := cursorPayloadFixture(t, c.fixture)
		if c.codexEvent == "PreToolUse" {
			cursor["tool_name"] = "Bash" // the name that would reach the rewrite
		}
		for name, payload := range map[string]map[string]any{
			"Cursor's own":           cursor,
			"Claude Code's, derived": asClaudeCodeAtCodex(cursor, c.codexEvent),
		} {
			t.Run(c.codexEvent+"/"+name, func(t *testing.T) {
				if got := runCodexEntry(t, c.codexEvent, payload); !got.nothing() {
					t.Fatalf("a Codex hook run with another host's payload did something: %+v", got)
				}
			})
		}
	}
}

// Each signal on its own, against Codex's real Stop. The decision is the
// function's on every runner; through hookMain it is also the flush that Stop
// starts, where a runner is established for Codex's hooks.
func TestWhatContradictsACodexEntry(t *testing.T) {
	for _, c := range []struct {
		name      string
		edit      func(map[string]any)
		standDown bool
	}{
		{"Codex's own payload", func(map[string]any) {}, false},
		{"cursor_version", func(m map[string]any) { m["cursor_version"] = "3.20.21" }, true},
		{"Cursor's spelling of the event", func(m map[string]any) { m["hook_event_name"] = "stop" }, true},
		{"another Codex event", func(m map[string]any) { m["hook_event_name"] = "SessionStart" }, true},
		{"no event name", func(m map[string]any) { delete(m, "hook_event_name") }, true},
		{"a transcript that is Claude Code's", func(m map[string]any) {
			m["transcript_path"] = "/home/u/.claude/projects/-home-u-project/s.jsonl"
		}, true},
		{"a transcript that is not a string", func(m map[string]any) { m["transcript_path"] = 7 }, true},
		// Not evidence: the desktop app's SessionEnd and `codex exec
		// --ephemeral` send a null transcript, and absent says nothing.
		{"a null transcript", func(m map[string]any) { m["transcript_path"] = nil }, false},
		{"no transcript", func(m map[string]any) { delete(m, "transcript_path") }, false},
		{"an empty transcript", func(m map[string]any) { m["transcript_path"] = "" }, false},
		// Derived: CODEX_HOME moves the directory, and Windows spells it
		// with backslashes; the file is still a rollout.
		{"a rollout elsewhere, spelled for Windows", func(m map[string]any) {
			m["transcript_path"] = `C:\Users\u\codex-home\sessions\2026\10\05\rollout-2026-10-05T03-14-48-01a10a0e.jsonl`
		}, false},
		{"another tool_use_id naming", func(m map[string]any) { m["tool_use_id"] = "call_x" }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			payload := codexPayloadFixture(t, "codex-0.158.0-linux-Stop.json")
			c.edit(payload)
			raw := mustJSON(t, payload)
			if got := codexEntryStandsDown(raw, "Stop"); got != c.standDown {
				t.Fatalf("codexEntryStandsDown = %v, want %v", got, c.standDown)
			}
			got := runCodexEntryRaw(t, newCodexTestInstall(t, writeHookConfig(t)), "Stop", raw)
			if want := !c.standDown && codexServesHere(); (got.flushes == 1) != want {
				t.Fatalf("through hookMain: flushes %d, want served=%v", got.flushes, want)
			}
		})
	}
	// Not a JSON object at all: no claim to be Codex, and nothing is done.
	for _, raw := range []string{"not json", "null", "[]", `"Stop"`} {
		if !codexEntryStandsDown([]byte(raw), "Stop") {
			t.Errorf("%q was taken for a Codex payload", raw)
		}
		if got := runCodexEntryRaw(t, newCodexTestInstall(t, writeHookConfig(t)), "Stop", []byte(raw)); !got.nothing() {
			t.Errorf("%q started something: %+v", raw, got)
		}
	}
}
