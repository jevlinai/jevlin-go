package main

// The Pi lineage extension, executed for real.
//
// Source-text greps cannot answer the questions that matter about this
// file — whether the history it attaches belongs to THIS tool call, whether
// a secret survives the caps, whether a malformed session leaves the tool
// call untouched — so these tests run the rendered extension in Node
// against a synthetic Pi, the same mechanism TestOpencodeRedactionBoundaries
// uses for the opencode plugin. Every identifier here is synthetic.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/redact"
)

// ── a synthetic Pi ──────────────────────────────────────────────────────

// piCall is one tool_call event delivered to the extension. Several calls in
// one case share ONE extension instance, which is how "a new session does
// not inherit the last one's window" and "a fork reconstructs its own
// generation" are asked as questions about process-wide state.
type piCall struct {
	// Fire names the Pi event delivered; tool_call when empty.
	Fire         string `json:"fire"`
	Session      string `json:"session"`
	Entries      any    `json:"entries"`
	Command      string `json:"command"`
	ToolCallID   string `json:"toolCallId"`
	ToolName     string `json:"toolName"`
	NoSession    bool   `json:"noSession"`
	BranchThrows bool   `json:"branchThrows"`
	// SessionThrows makes the session manager itself throw when asked who it is.
	SessionThrows bool           `json:"sessionThrows"`
	Event         map[string]any `json:"event"`
}

type piOutcome struct {
	Command  string          `json:"command"`
	Returned json.RawMessage `json:"returned"`
	Threw    string          `json:"threw"`
}

// piEntry builders mirror Pi's persisted session entries: a message entry
// wraps an AgentMessage, an assistant message's content is always an array
// of parts, a tool call inside one is a part whose id field is `id`, and a
// compaction is its own top-level entry type.
func piUser(text string) map[string]any {
	return map[string]any{"type": "message", "message": map[string]any{"role": "user", "content": text}}
}

func piAssistant(text string, toolCallIDs ...string) map[string]any {
	content := []map[string]any{}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, id := range toolCallIDs {
		content = append(content, map[string]any{"type": "toolCall", "id": id, "name": "bash", "arguments": map[string]any{}})
	}
	return map[string]any{"type": "message", "message": map[string]any{"role": "assistant", "content": content}}
}

// piToolResult is a tool result entry, as Pi persists one: its own record of
// the tool's name, how long it ran and whether it failed, beside the output the
// extension must never read.
func piToolResult(callID, tool string, ms float64, isError bool) map[string]any {
	return map[string]any{"type": "message", "message": map[string]any{
		"role": "toolResult", "toolCallId": callID, "toolName": tool, "isError": isError, "durationMs": ms,
		"content": []map[string]any{{"type": "text", "text": "TOOL OUTPUT MUST NOT LEAVE"}},
	}}
}

// piCustomMessage is an extension's message, which Pi persists as an entry of
// its own and sends to the model as if the user had written it.
func piCustomMessage(text string) map[string]any {
	return map[string]any{"type": "custom_message", "customType": "synthetic", "content": text, "display": true}
}

// piID gives a synthetic entry the id Pi persists for every entry. A turn is
// named by the id of its user entry, so the tests that ask for a turn id have
// to give one; the rest need not.
func piID(id string, e map[string]any) map[string]any {
	e["id"] = id
	return e
}

func piCompaction() map[string]any {
	return map[string]any{"type": "compaction", "summary": "synthetic summary", "firstKeptEntryId": "e1", "tokensBefore": 1000}
}

const piSearchCommand = `jevlin search -format model "q"`

// runPiExtension executes the rendered extension in Node, one fresh
// extension instance per case, and returns each call's outcome.
func runPiExtension(t *testing.T, cases map[string][]piCall) map[string][]piOutcome {
	t.Helper()
	return runPiExtensionFor(t, cases, "")
}

// runPiExtensionFor is runPiExtension for an extension rendered with bin, the
// binary it hands a finished turn to.
func runPiExtensionFor(t *testing.T, cases map[string][]piCall, bin string) map[string][]piOutcome {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded Pi extension")
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const mod = await import('data:text/javascript;base64,'+Buffer.from(input.extension).toString('base64'));
 const result = {};
 for (const [name, calls] of Object.entries(input.cases)) {
  const handlers = {};
  // The synthetic Pi: an extension registers with pi.on(event, handler).
  mod.default({ on: (ev, fn) => { handlers[ev] = fn; } });
  const outcomes = [];
  for (const c of calls) {
   const fire = c.fire || 'tool_call';
   const handler = handlers[fire];
   const event = c.event ?? (fire === 'tool_call' ? { type: 'tool_call', toolName: c.toolName || 'bash', toolCallId: c.toolCallId, input: { command: c.command } } : { type: fire });
   const ctx = c.noSession ? {} : { sessionManager: {
     getSessionId: () => { if (c.sessionThrows) throw new Error('synthetic host failure'); return c.session; },
     getBranch: () => { if (c.branchThrows) throw new Error('synthetic host failure'); return c.entries; },
   } };
   const out = { command: null, returned: null, threw: '' };
   try { out.returned = (handler ? await handler(event, ctx) : undefined) ?? null; } catch (e) { out.threw = String(e); }
   out.command = event?.input?.command ?? null;
   outcomes.push(out);
  }
  result[name] = outcomes;
 }
 process.stdout.write(JSON.stringify(result));`
	input, err := json.Marshal(map[string]any{"extension": renderAgentScriptFor(piExtensionTS, shellPOSIX, testCfg, bin), "cases": cases})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extension: %v\n%s", err, output)
	}
	var results map[string][]piOutcome
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	return results
}

// piBridge decodes the envelope the extension put in front of the command,
// and fails the test if there is none.
func piBridge(t *testing.T, o piOutcome) *traceEnvelope {
	t.Helper()
	if o.Threw != "" {
		t.Fatalf("extension threw, which BLOCKS the tool call in Pi: %s", o.Threw)
	}
	return decodeBridgeFromCommand(t, o.Command)
}

// piNoBridge asserts the command came back exactly as it went in.
func piNoBridge(t *testing.T, o piOutcome, original string) {
	t.Helper()
	if o.Threw != "" {
		t.Fatalf("extension threw, which BLOCKS the tool call in Pi: %s", o.Threw)
	}
	if o.Command != original {
		t.Fatalf("command was rewritten when it should not have been:\n got %q\nwant %q", o.Command, original)
	}
}

// ── the adapter is an observer, never an authority ──────────────────────

// A Pi tool_call handler gates execution by RETURNING something: {block:
// true} refuses the call, and a thrown error refuses it too (Pi rethrows as
// "Extension failed, blocking execution"). This extension must do neither,
// for any input at all — recognizing our own search command tells us where a
// trace belongs, and says nothing about whether the command may run. The
// guard is deliberately independent of search recognition: it holds for the
// commands we rewrite and the ones we ignore alike.
func TestPiExtensionNeverAuthorizesOrBlocksAToolCall(t *testing.T) {
	cases := map[string][]piCall{
		"our search":      {{Session: "s", Command: piSearchCommand, ToolCallID: "c1", Entries: []any{}}},
		"foreign command": {{Session: "s", Command: "rm -rf /", ToolCallID: "c1", Entries: []any{}}},
		"already bridged": {{Session: "s", Command: "JEVLIN_TRACE_BRIDGE=x " + piSearchCommand, ToolCallID: "c1", Entries: []any{}}},
		"other tool":      {{Session: "s", ToolName: "read", Command: piSearchCommand, ToolCallID: "c1", Entries: []any{}}},
		"no session":      {{NoSession: true, Command: piSearchCommand, ToolCallID: "c1", Entries: []any{}}},
		"branch throws":   {{Session: "s", Command: piSearchCommand, ToolCallID: "c1", BranchThrows: true}},
		"entries garbage": {{Session: "s", Command: piSearchCommand, ToolCallID: "c1", Entries: "not-a-branch"}},
		"event garbage":   {{Event: map[string]any{"toolName": "bash"}}},
		"empty event":     {{Event: map[string]any{}}},
	}
	for name, outcomes := range runPiExtension(t, cases) {
		t.Run(name, func(t *testing.T) {
			o := outcomes[0]
			if o.Threw != "" {
				t.Fatalf("extension threw, which Pi turns into a blocked tool call: %s", o.Threw)
			}
			if s := strings.TrimSpace(string(o.Returned)); s != "null" {
				t.Fatalf("handler returned %s; a truthy return is how a Pi extension gates execution, and this one must never gate anything", s)
			}
		})
	}
}

// ── recognition ─────────────────────────────────────────────────────────

// The extension must reach the SAME verdict as the binary's own
// recognizer for the same command — that is what the drift guard on the
// pattern is for, checked here as behavior rather than as text.
func TestPiExtensionRewritesOurSearchAndNothingElse(t *testing.T) {
	entries := []any{piUser("find me a thing"), piAssistant("Looking that up now.", "c1")}
	commands := map[string]string{
		"bare":              piSearchCommand,
		"absolute path":     `/opt/bin/jevlin search "q"`,
		"quoted path":       `"/opt/bin/jevlin" search "q"`,
		"windows exe":       `C:\bin\jevlin.exe search "q"`,
		"lookalike binary":  "jevlin-helper search q",
		"another tool":      "grep -rn jevlinsearch .",
		"another verb":      "jevlin status",
		"our name in prose": "echo 'run jevlin to search'",
		"already bridged":   "JEVLIN_TRACE_BRIDGE=stale " + piSearchCommand,
	}
	cases := map[string][]piCall{}
	for name, cmd := range commands {
		cases[name] = []piCall{{Session: "s", Command: cmd, ToolCallID: "c1", Entries: entries}}
	}
	// A tool that is not the shell is never rewritten, whatever it carries.
	cases["other tool"] = []piCall{{Session: "s", ToolName: "read", Command: piSearchCommand, ToolCallID: "c1", Entries: entries}}
	// Recognized, but the prefix cannot reach it: in `echo hi | jevlin
	// search`, a POSIX assignment binds to echo, so the adapter stands
	// down and the command runs exactly as written. This case used to
	// expect a rewrite — a green test encoding the defect where one
	// conversation's searches split across unrelated fallback sessions.
	const afterPipe = `echo hi | jevlin search "q"`
	cases["after a pipe"] = []piCall{{Session: "s", Command: afterPipe, ToolCallID: "c1", Entries: entries}}

	got := runPiExtension(t, cases)
	for name, cmd := range commands {
		t.Run(name, func(t *testing.T) {
			// H-R4: a bridge already on the command is no longer a reason to
			// stand down. A stale one — or one a model wrote itself — is
			// removed and replaced by this call's own, so the harness on the
			// envelope is only ever ours when we put it there.
			if !isSearchCommand(cmd) {
				piNoBridge(t, got[name][0], cmd)
				return
			}
			if env := piBridge(t, got[name][0]); env.Harness != "pi" {
				t.Errorf("harness = %q, want pi", env.Harness)
			}
			if strings.Contains(got[name][0].Command, "stale") {
				t.Errorf("the stale bridge survived: %q", got[name][0].Command)
			}
		})
	}
	piNoBridge(t, got["after a pipe"][0], afterPipe)
	piNoBridge(t, got["other tool"][0], piSearchCommand)
}

// ── identity ────────────────────────────────────────────────────────────

func TestPiExtensionHashesHostIdentifiers(t *testing.T) {
	const sid, callID, userID = "pi-session-synthetic-0123", "toolcall-synthetic-0123", "u0synth1"
	got := runPiExtension(t, map[string][]piCall{
		"hashed": {{Session: sid, Command: piSearchCommand, ToolCallID: callID,
			Entries: []any{piID(userID, piUser("q")), piAssistant("Searching.", callID)}}},
	})
	o := got["hashed"][0]
	env := piBridge(t, o)
	if env.SessionID != traceHash(sid) {
		t.Errorf("session_id = %q, want the domain-separated hash", env.SessionID)
	}
	if env.CallID != traceHash(sid+"|"+callID) {
		t.Errorf("call_id = %q, want hash(session|toolCallId)", env.CallID)
	}
	// The turn is the user entry's persisted id, hashed with the session's.
	if env.TurnID != traceHash(sid+"|"+userID) {
		t.Errorf("turn_id = %q, want hash(session|user entry id)", env.TurnID)
	}
	for _, raw := range []string{sid, callID, userID} {
		if strings.Contains(o.Command, raw) {
			t.Errorf("a raw host identifier reached the command line: %q", raw)
		}
	}
}

// ── history belongs to THIS tool call ───────────────────────────────────

// "The latest assistant text" is the wrong question, and these are the
// cases where it gives a wrong answer: prose from the previous turn, prose
// from a sibling tool call's own turn, and a first search in a fresh turn
// whose only assistant text is older than the user's last message.
func TestPiExtensionBindsHistoryToTheCurrentToolCall(t *testing.T) {
	const stale = "PREVIOUS TURN PROSE, must never be inherited"
	const current = "Current turn: looking that up now."
	previousTurn := []any{piUser("first question"), piAssistant(stale, "c-old")}
	cases := map[string][]piCall{
		// The assistant message that emitted this call carries the prose.
		"current call": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
			Entries: append(append([]any{}, previousTurn...), piUser("second question"), piAssistant(current, "c1"))}},
		// A call id nobody on the branch emitted: no provable owner.
		"unrelated call": {{Session: "s", ToolCallID: "c-unknown", Command: piSearchCommand,
			Entries: append(append([]any{}, previousTurn...), piUser("second question"), piAssistant(current, "c1"))}},
		// The current turn opened with a tool call and no prose of its own;
		// the only assistant text on the branch is the previous turn's.
		"first search in a new turn": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
			Entries: append(append([]any{}, previousTurn...), piUser("second question"), piAssistant("", "c1"))}},
		// No user message at all: no turn boundary to floor the scan at.
		"no turn boundary": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
			Entries: []any{piAssistant(stale, "c1")}}},
		// The call is emitted by an older assistant message; text that came
		// after it belongs to a later turn and must not be picked up.
		"later turn ignored": {{Session: "s", ToolCallID: "c-old", Command: piSearchCommand,
			Entries: append(append([]any{}, previousTurn...), piUser("second question"), piAssistant(current, "c1"))}},
	}
	got := runPiExtension(t, cases)

	env := piBridge(t, got["current call"][0])
	if len(env.History) != 1 || env.History[0].Text != current {
		t.Fatalf("current call did not get its own assistant text: %+v", env.History)
	}
	for _, name := range []string{"unrelated call", "first search in a new turn", "no turn boundary"} {
		env := piBridge(t, got[name][0])
		if len(env.History) != 0 {
			t.Errorf("%s: attached history it could not prove belongs here: %+v", name, env.History)
		}
	}
	older := piBridge(t, got["later turn ignored"][0])
	if len(older.History) != 1 || older.History[0].Text != stale {
		t.Fatalf("a call from an earlier turn got the wrong turn's prose: %+v", older.History)
	}
	for _, name := range []string{"unrelated call", "first search in a new turn", "no turn boundary"} {
		if strings.Contains(got[name][0].Command, "PREVIOUS") {
			t.Errorf("%s: previous-turn prose reached the bridge", name)
		}
	}
}

// ── the window generation, reconstructed from persisted state ───────────

// A process-global counter gets every one of these wrong. The generation is
// counted from the compaction entries on the current branch, so it survives
// a restart (the entries are read back from the session file), stays
// per-session when two sessions share one extension instance, and follows a
// fork rather than the process's own history.
func TestPiExtensionReconstructsTheWindowGeneration(t *testing.T) {
	turn := func(extra ...any) []any {
		return append(extra, piUser("q"), piAssistant("Searching.", "c1"))
	}
	cases := map[string][]piCall{
		"fresh":    {{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn()}},
		"one":      {{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction())}},
		"multiple": {{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction(), piCompaction(), piCompaction())}},
		// A restart: this extension instance never saw the compaction
		// happen, it only reads the branch Pi persisted.
		"resumed": {{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction(), piCompaction())}},
		// One instance, two sessions: the second must not inherit the first.
		"second session": {
			{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction())},
			{Session: "s2", ToolCallID: "c1", Command: piSearchCommand, Entries: turn()},
		},
		// A branch that does not contain the compaction does not count it.
		"fork": {
			{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction(), piCompaction())},
			{Session: "s1", ToolCallID: "c1", Command: piSearchCommand, Entries: turn(piCompaction())},
		},
	}
	got := runPiExtension(t, cases)
	for _, tc := range []struct {
		name, want string
		call       int
	}{
		{"fresh", "none", 0}, {"one", "1", 0}, {"multiple", "3", 0}, {"resumed", "2", 0},
		{"second session", "none", 1}, {"fork", "1", 1},
	} {
		if w := piBridge(t, got[tc.name][tc.call]).Window; w != tc.want {
			t.Errorf("%s: window = %q, want %q", tc.name, w, tc.want)
		}
	}
}

// ── malformed host data fails open ──────────────────────────────────────

func TestPiExtensionFailsOpenOnMalformedHostData(t *testing.T) {
	cases := map[string][]piCall{
		"branch throws":     {{Session: "s", ToolCallID: "c1", Command: piSearchCommand, BranchThrows: true}},
		"entries not array": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand, Entries: "nonsense"}},
		"entries null":      {{Session: "s", ToolCallID: "c1", Command: piSearchCommand, Entries: nil}},
		"junk entries": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
			Entries: []any{nil, 7, "x", map[string]any{"type": "message"}, map[string]any{"type": "message", "message": map[string]any{"role": "assistant", "content": "a string, not parts"}}}}},
		"no session manager": {{NoSession: true, ToolCallID: "c1", Command: piSearchCommand}},
		"empty session":      {{Session: "", ToolCallID: "c1", Command: piSearchCommand, Entries: []any{}}},
		"no tool call id": {{Session: "s", ToolCallID: "", Command: piSearchCommand,
			Entries: []any{piUser("q"), piAssistant("text", "c1")}}},
	}
	got := runPiExtension(t, cases)
	// Host state we cannot read costs the history, never the search: the
	// identifiers still thread where the session is known.
	for _, name := range []string{"branch throws", "entries not array", "entries null", "junk entries", "no tool call id"} {
		env := piBridge(t, got[name][0])
		if len(env.History) != 0 {
			t.Errorf("%s: invented history out of unreadable state", name)
		}
	}
	// No session identity at all: no bridge, and the search falls back to
	// its own per-shell trace rather than to a bridge that means nothing.
	for _, name := range []string{"no session manager", "empty session"} {
		piNoBridge(t, got[name][0], piSearchCommand)
	}
}

// ── redaction and the caps, at the boundary ─────────────────────────────

// The bridge is a process argument: whatever is in it is visible in `ps` the
// moment the command runs. So the extension must scrub BEFORE it truncates
// and before it builds the envelope — never hand raw text to the binary to
// be cleaned up later. These are the same synthetic canaries the Go and
// opencode boundary tests use, placed so that each one straddles the cap.
func TestPiExtensionRedactsAndCapsBeforeTheBridge(t *testing.T) {
	cases := map[string][]piCall{}
	inputs := traceBoundaryInputs()
	for name, text := range inputs {
		cases[name] = []piCall{{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
			Entries: []any{piUser("q"), piAssistant(text, "c1")}}}
	}
	// A complete entry over the source budget is omitted whole, never
	// sliced: slicing is what cuts a secret in half and hides it from the
	// scrubber.
	oversize := "sk-SyntheticCredentialBody0123456789 " + strings.Repeat("x", hookTailBytes)
	cases["source_budget"] = []piCall{{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
		Entries: []any{piUser("q"), piAssistant(oversize, "c1")}}}
	// An envelope too large even after history is dropped yields no bridge
	// at all — a trace must never push a valid search past the host's
	// command-length limit.
	cases["envelope_budget"] = []piCall{{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
		Entries: []any{piUser("q"), piAssistant(strings.Repeat("\"", traceHistoryCap), "c1")}}}

	got := runPiExtension(t, cases)
	for name, text := range inputs {
		t.Run(name, func(t *testing.T) {
			env := piBridge(t, got[name][0])
			if len(env.History) != 1 {
				t.Fatal("bounded source lost history")
			}
			assertPreparedHistory(t, env.History[0].Text, text)
		})
	}
	t.Run("source_budget", func(t *testing.T) {
		o := got["source_budget"][0]
		if env := piBridge(t, o); len(env.History) != 0 {
			t.Fatal("an over-budget entry was sliced instead of omitted")
		}
		if strings.Contains(o.Command, "CredentialBody0123456789") {
			t.Fatal("a synthetic credential reached the command line")
		}
	})
	t.Run("envelope_budget", func(t *testing.T) {
		env := piBridge(t, got["envelope_budget"][0])
		if len(env.History) != 0 {
			t.Fatal("an oversized envelope kept its history")
		}
	})
}

// ── the turn ─────────────────────────────────────────────────────────────

// A turn is named by the user entry Pi persisted for it: two searches made
// under one prompt share it, the next prompt's differ, and it is built from
// that id, never from a count the extension keeps.
func TestPiExtensionNamesTheTurnByTheUsersPersistedEntry(t *testing.T) {
	const sid = "pi-session-turns"
	firstTurn := []any{
		piID("u1", piUser("first question")),
		piID("a1", piAssistant("Looking.", "c1")),
		piID("r1", piToolResult("c1", "bash", 9, false)),
		piID("a2", piAssistant("Once more.", "c2")),
	}
	secondTurn := append(append([]any{}, firstTurn...),
		piID("r2", piToolResult("c2", "bash", 9, false)),
		piID("a3", piAssistant("Done.")),
		piID("u2", piUser("second question")),
		piID("a4", piAssistant("Looking again.", "c3")),
	)
	// Each call sees the branch as it stands when that call is made.
	got := runPiExtension(t, map[string][]piCall{"turns": {
		{Session: sid, ToolCallID: "c1", Command: piSearchCommand, Entries: firstTurn[:2]},
		{Session: sid, ToolCallID: "c2", Command: piSearchCommand, Entries: firstTurn},
		{Session: sid, ToolCallID: "c3", Command: piSearchCommand, Entries: secondTurn},
	}})["turns"]
	first, again, next := piBridge(t, got[0]).TurnID, piBridge(t, got[1]).TurnID, piBridge(t, got[2]).TurnID

	if first != traceHash(sid+"|u1") || again != first {
		t.Errorf("two searches of one turn: turn_id %q and %q, want both hash(session|u1)", first, again)
	}
	if next != traceHash(sid+"|u2") || next == first {
		t.Errorf("the next turn's search: turn_id %q, want hash(session|u2), different from %q", next, first)
	}
	for i, o := range got {
		if strings.Contains(o.Command, "u1") || strings.Contains(o.Command, "u2") {
			t.Errorf("call %d: a raw entry id reached the command line", i)
		}
	}
}

// A resumed session is a new process reading the entries back from the file,
// and a fork is a new session holding copies of them. The turn id has to come
// out the same for the first, and be the fork's own for the second, because
// the entry ids a fork copies are unique within one session and no further.
func TestPiExtensionKeepsATurnIdAcrossAResumeAndTiesItToItsSession(t *testing.T) {
	branch := []any{
		piID("u1", piUser("question")),
		piID("a1", piAssistant("Looking.", "c1")),
		piID("r1", piToolResult("c1", "bash", 9, false)),
		piID("a2", piAssistant("Done.")),
		piID("u2", piUser("follow-up")),
		piID("a3", piAssistant("Looking again.", "c9")),
	}
	got := runPiExtension(t, map[string][]piCall{
		// One extension that has been through the whole conversation...
		"original": {
			{Session: "s", ToolCallID: "c1", Command: piSearchCommand, Entries: branch[:2]},
			{Session: "s", ToolCallID: "c9", Command: piSearchCommand, Entries: branch},
		},
		// ...and one started after a resume, which saw neither turn begin. A
		// count kept in memory would restart here and name the turn differently.
		"resumed": {{Session: "s", ToolCallID: "c9", Command: piSearchCommand, Entries: branch}},
		// A fork: another session, the same entries with the same ids.
		"fork": {{Session: "s-fork", ToolCallID: "c9", Command: piSearchCommand, Entries: branch}},
	})
	original, resumed, fork := piBridge(t, got["original"][1]).TurnID, piBridge(t, got["resumed"][0]).TurnID, piBridge(t, got["fork"][0]).TurnID

	if original != traceHash("s|u2") || resumed != original {
		t.Errorf("after a resume: turn_id %q, want %q (hash(session|u2), as before it)", resumed, original)
	}
	if fork != traceHash("s-fork|u2") || fork == original {
		t.Errorf("in a fork: turn_id %q, want hash(fork session|u2), not the original's %q", fork, original)
	}
}

// Anything the turn hangs on that cannot be shown sends no turn, as it sends no
// history: a wrong turn would make unrelated searches look related.
func TestPiExtensionSendsNoTurnIdItCannotProve(t *testing.T) {
	cases := map[string]piCall{
		"no user entry on the branch": {Session: "s", ToolCallID: "c1", Entries: []any{piID("a1", piAssistant("Looking.", "c1"))}},
		"a user entry only after the call": {Session: "s", ToolCallID: "c1",
			Entries: []any{piID("a1", piAssistant("Looking.", "c1")), piID("u1", piUser("q"))}},
		"a user entry with no id": {Session: "s", ToolCallID: "c1",
			Entries: []any{piUser("q"), piID("a1", piAssistant("Looking.", "c1"))}},
		"a user entry with an id that is not text": {Session: "s", ToolCallID: "c1",
			Entries: []any{piID("", func() map[string]any { e := piUser("q"); e["id"] = 7; return e }()), piID("a1", piAssistant("Looking.", "c1"))}},
		"a call nobody on the branch emitted": {Session: "s", ToolCallID: "c-unknown",
			Entries: []any{piID("u1", piUser("q")), piID("a1", piAssistant("Looking.", "c1"))}},
		"no tool call id": {Session: "s", ToolCallID: "",
			Entries: []any{piID("u1", piUser("q")), piID("a1", piAssistant("Looking.", "c1"))}},
		"a branch that cannot be read": {Session: "s", ToolCallID: "c1", BranchThrows: true},
	}
	byName := map[string][]piCall{}
	for name, c := range cases {
		c.Command = piSearchCommand
		byName[name] = []piCall{c}
	}
	for name, outcomes := range runPiExtension(t, byName) {
		t.Run(name, func(t *testing.T) {
			if env := piBridge(t, outcomes[0]); env.TurnID != "" {
				t.Errorf("turn_id = %q, want none", env.TurnID)
			}
		})
	}
}

// What starts a turn is the user's message. Pi's system prompt is a message
// entry of its own role, and an extension's message is an entry of its own
// type; neither is a turn, and neither is read.
func TestPiExtensionStartsATurnOnlyAtAUserMessage(t *testing.T) {
	const canary = "SYSTEM PROMPT MUST NEVER BE READ"
	system := piID("sys", map[string]any{"type": "message", "message": map[string]any{"role": "system", "content": canary}})
	got := runPiExtension(t, map[string][]piCall{"turn": {{Session: "s", ToolCallID: "c1", Command: piSearchCommand,
		Entries: []any{system, piID("u1", piUser("q")), piID("cm", piCustomMessage("injected")), piID("a1", piAssistant("Looking.", "c1"))}}}})
	o := got["turn"][0]
	if env := piBridge(t, o); env.TurnID != traceHash("s|u1") {
		t.Errorf("turn_id = %q, want the user entry's (hash(s|u1))", env.TurnID)
	}
	if strings.Contains(o.Command, canary) {
		t.Error("the system prompt reached the command line")
	}
}

// ── a real Pi ────────────────────────────────────────────────────────────

// piRecorded is one moment captured from a live Pi (testdata/pi): the event an
// extension was handed and the branch its session manager returned then.
type piRecorded struct {
	Run    string         `json:"run"`
	Ev     string         `json:"ev"`
	Sid    string         `json:"sid"`
	Event  map[string]any `json:"event"`
	Branch []any          `json:"branch"`
}

func piFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pi", name)) // #nosec G304 -- a fixed testdata path this file builds, not an external one
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
}

func loadPiRecording(t *testing.T) []piRecorded {
	t.Helper()
	var out []piRecorded
	for _, line := range piFixtureLines(t, "pi-1.3.0-linux-events.jsonl") {
		var r piRecorded
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("pi-1.3.0-linux-events.jsonl: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// loadPiSession is Pi's own session file, the entries as written to disk.
func loadPiSession(t *testing.T) (header map[string]any, entries []map[string]any) {
	t.Helper()
	for i, line := range piFixtureLines(t, "pi-1.3.0-linux-session.jsonl") {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("pi-1.3.0-linux-session.jsonl: %v", err)
		}
		if i == 0 {
			header = e
			continue
		}
		entries = append(entries, e)
	}
	return header, entries
}

// userEntryIDs lists, in order, the ids of the user messages in entries.
func userEntryIDs(entries []map[string]any) []string {
	var ids []string
	for _, e := range entries {
		if m, _ := e["message"].(map[string]any); e["type"] == "message" && m != nil && m["role"] == "user" {
			ids = append(ids, e["id"].(string))
		}
	}
	return ids
}

// The same questions asked of what a live Pi 1.3.0 handed an extension: the
// searches of one prompt share a turn, a resumed session's next prompt has its
// own, and the id is the one Pi wrote to the session file.
func TestPiTurnIdsOfARealSession(t *testing.T) {
	header, file := loadPiSession(t)
	users := userEntryIDs(file)
	if len(users) != 3 {
		t.Fatalf("the captured session has %d user messages, want 3: the fixture is not what these tests were written against", len(users))
	}
	cases := map[string][]piCall{}
	var sids = map[string]string{}
	for _, r := range loadPiRecording(t) {
		if r.Ev != "tool_call" {
			continue
		}
		sids[r.Run] = r.Sid
		cmd, _ := r.Event["input"].(map[string]any)["command"].(string)
		cases[r.Run] = append(cases[r.Run], piCall{Session: r.Sid, Entries: r.Branch, Event: r.Event, Command: cmd})
	}
	if sids["A"] != header["id"] || sids["B"] != header["id"] {
		t.Fatalf("the recorded events and the session file are not one session: %v vs %v", sids, header["id"])
	}
	got := runPiExtension(t, cases)

	// Run A: two searches and a command that is not one, all under prompt 1.
	a := got["A"]
	if len(a) != 3 {
		t.Fatalf("run A has %d tool calls, want 3", len(a))
	}
	first, second := piBridge(t, a[0]).TurnID, piBridge(t, a[1]).TurnID
	if first != traceHash(sids["A"]+"|"+users[0]) || second != first {
		t.Errorf("run A: turn_id %q and %q, want both hash(session|%s)", first, second, users[0])
	}
	piNoBridge(t, a[2], cases["A"][2].Command)
	// Run B: the resumed session's next prompt.
	if b := piBridge(t, got["B"][0]).TurnID; b != traceHash(sids["B"]+"|"+users[1]) || b == first {
		t.Errorf("run B: turn_id %q, want hash(session|%s), different from run A's", b, users[1])
	}
	// Run D: another session, whose single user message it names.
	var dUser string
	for _, r := range loadPiRecording(t) {
		if r.Run == "D" && r.Ev == "tool_call" {
			for _, e := range r.Branch {
				if m, _ := e.(map[string]any)["message"].(map[string]any); m != nil && m["role"] == "user" {
					dUser = e.(map[string]any)["id"].(string)
				}
			}
		}
	}
	if d := piBridge(t, got["D"][0]).TurnID; dUser == "" || d != traceHash(sids["D"]+"|"+dUser) {
		t.Errorf("run D: turn_id %q, want hash(session|%q)", d, dUser)
	}
}

// ── the turn end ─────────────────────────────────────────────────────────

// piStandIn is a stand-in for the jevlin binary. Run as `hook … turn pi`, it
// keeps its arguments and every byte piped on its stdin; run as `hook … start
// pi`, it does nothing. That is the whole of what the extension can do to the
// binary, so it is the whole of what these tests look at.
type piStandIn struct{ bin, dir string }

type piReport struct {
	Argv string
	Raw  string
	Turn hostTurn
}

func newPiStandIn(t *testing.T) *piStandIn {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a POSIX shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "jevlin")
	script := "#!/bin/sh\nd='" + dir + "'\ncase \"$*\" in *' start pi') exit 0;; esac\nf=$(mktemp \"$d/r.XXXXXX\") || exit 0\nprintf '%s\\n' \"$@\" > \"$f.argv\"\ncat > \"$f\"\nmv \"$f\" \"$f.done\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a test stand-in that must be executable
		t.Fatal(err)
	}
	return &piStandIn{bin: bin, dir: dir}
}

// reports waits for the stand-in to have been run want times, then for a
// moment longer to see that it is not run again, and returns what it kept.
func (s *piStandIn) reports(t *testing.T, want int) []piReport {
	t.Helper()
	read := func() []piReport {
		done, _ := filepath.Glob(filepath.Join(s.dir, "r.*.done"))
		sort.Strings(done)
		var out []piReport
		for _, f := range done {
			raw, err := os.ReadFile(f) // #nosec G304 -- a file the stand-in wrote under this test's own directory
			if err != nil {
				t.Fatal(err)
			}
			argv, _ := os.ReadFile(strings.TrimSuffix(f, ".done") + ".argv") // #nosec G304 -- as above
			r := piReport{Argv: strings.Join(strings.Fields(string(argv)), " "), Raw: string(raw)}
			if err := json.Unmarshal(raw, &r.Turn); err != nil {
				t.Fatalf("the extension piped something that is not a turn: %v\n%s", err, raw)
			}
			out = append(out, r)
		}
		return out
	}
	for deadline := time.Now().Add(5 * time.Second); len(read()) < want && time.Now().Before(deadline); {
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	got := read()
	if len(got) != want {
		t.Fatalf("the binary was handed %d turns, want %d", len(got), want)
	}
	return got
}

// piToolUse, piThinking and piMessage build what an assistant entry holds.
func piToolUse(id, name string, args map[string]any) map[string]any {
	return map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": args}
}

func piText(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func piThinking(s string) map[string]any { return map[string]any{"type": "thinking", "thinking": s} }

// piAnswer is an assistant entry with the parts and bookkeeping Pi persists.
func piAnswer(id string, stop string, usage map[string]any, parts ...map[string]any) map[string]any {
	return piID(id, map[string]any{"type": "message", "message": map[string]any{
		"role": "assistant", "content": parts, "model": "claude-opus-5-5", "provider": "synthetic", "stopReason": stop, "usage": usage,
	}})
}

func piUsage(in, out, read, write float64) map[string]any {
	return map[string]any{"input": in, "output": out, "cacheRead": read, "cacheWrite": write, "totalTokens": in + out + read + write}
}

const piSearchOf = `jevlin search -format model "private-query"`

// The turn a search was made in is handed to the binary, once, when the run
// settles: what was asked, what was finally answered, each tool by name, time
// and outcome, the model and its tokens; and nothing a tool was given or
// returned, nothing the model thought, and nothing Pi's system prompt said.
func TestPiExtensionReportsTheTurnItSearchedIn(t *testing.T) {
	in := newPiStandIn(t)
	secret := "ghp_" + strings.Repeat("a", 36)
	system := piID("sys", map[string]any{"type": "message", "message": map[string]any{"role": "system", "content": "", "sections": map[string]any{"preamble": "SYSTEM PROMPT MUST NEVER BE READ"}}})
	user := piID("u1", map[string]any{"type": "message", "message": map[string]any{"role": "user",
		"content": []map[string]any{piText("who designed it? my token is " + secret), {"type": "image", "data": "IMAGE BYTES MUST NOT LEAVE"}}}})
	first := piAnswer("a1", "toolUse", piUsage(100, 20, 900, 0),
		piText("Let me look."), piToolUse("c-read", "read", map[string]any{"path": "/home/someone/secret.txt"}), piToolUse("c-search", "bash", map[string]any{"command": piSearchOf}))
	second := piAnswer("a2", "toolUse", piUsage(50, 10, 0, 0),
		piText("Checking one more thing."), piToolUse("c-rm", "bash", map[string]any{"command": "rm -rf build"}))
	last := piAnswer("a3", "stop", piUsage(60, 5, 0, 0), piThinking("PRIVATE REASONING"), piText("Populous designed it."))
	branch := []any{system, user, first,
		piID("r1", piToolResult("c-read", "read", 12, false)), piID("r2", piToolResult("c-search", "bash", 5200, false)),
		second, piID("r3", piToolResult("c-rm", "bash", 40, true)), last}

	got := runPiExtensionFor(t, map[string][]piCall{"turn": {
		{Session: "ses_1", ToolCallID: "c-search", Command: piSearchOf, Entries: branch[:3]},
		{Fire: "agent_settled", Session: "ses_1", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": false}},
	}}, in.bin)["turn"]
	for i, o := range got {
		if o.Threw != "" || strings.TrimSpace(string(o.Returned)) != "null" {
			t.Fatalf("call %d threw %q or returned %s; an observer does neither", i, o.Threw, o.Returned)
		}
	}
	reports := in.reports(t, 1)
	r := reports[0]
	if r.Argv != "hook -config "+testCfg+" turn pi" {
		t.Errorf("argv = %q", r.Argv)
	}
	// The ids the binary hashes: the session's, and the user entry's.
	if r.Turn.Session != "ses_1" || r.Turn.Turn != "u1" {
		t.Errorf("session/turn = %q/%q, want ses_1/u1", r.Turn.Session, r.Turn.Turn)
	}
	if r.Turn.Status != "completed" || r.Turn.FinalText != "Populous designed it." || r.Turn.Model != "claude-opus-5-5" {
		t.Errorf("turn = %+v", r.Turn)
	}
	if !strings.HasPrefix(r.Turn.UserText, "who designed it? my token is ") || !strings.Contains(r.Turn.UserText, "[REDACTED]") {
		t.Errorf("user_text = %q", r.Turn.UserText)
	}
	if u := r.Turn.Usage; u == nil || u.Input != 100+900+50+60 || u.Output != 20+10+5 {
		t.Errorf("usage = %+v, want input 1110 (cache reads included) and output 35", u)
	}
	steps := r.Turn.Steps
	if len(steps) != 5 || steps[0].Kind != "assistant" || steps[0].Text != "Let me look." ||
		steps[1].Kind != "tool" || steps[1].Name != "read" || steps[1].Ms != 12 || steps[1].OK == nil || !*steps[1].OK ||
		steps[2].Kind != "search" || steps[2].Call != "c-search" || steps[2].Ms != 5200 || steps[2].OK == nil || !*steps[2].OK ||
		steps[3].Kind != "assistant" || steps[3].Text != "Checking one more thing." ||
		steps[4].Kind != "tool" || steps[4].Name != "bash" || steps[4].Ms != 40 || steps[4].OK == nil || *steps[4].OK {
		t.Errorf("steps = %+v", steps)
	}
	// Byte for byte, what was piped: no argument, no output, no reasoning, no
	// system prompt, no image, no search command, no secret.
	for _, leak := range []string{"secret.txt", "TOOL OUTPUT", "rm -rf", "private-query", "JEVLIN_TRACE_BRIDGE", "PRIVATE REASONING", "SYSTEM PROMPT", "IMAGE BYTES", secret, "arguments", "command", "path"} {
		if strings.Contains(r.Raw, leak) {
			t.Errorf("the extension piped %q", leak)
		}
	}
}

// A turn is handed over when the run has settled, not when a loop of it ends:
// Pi closes a loop with agent_end each time it retries a failed request, so
// a handler there reports the failed attempt as the turn.
func TestPiExtensionReportsAtSettledNotAtAgentEnd(t *testing.T) {
	in := newPiStandIn(t)
	user := piID("u1", piUser("question"))
	failed := piAnswer("a0", "error", piUsage(0, 0, 0, 0))
	search := piAnswer("a1", "toolUse", piUsage(10, 5, 0, 0), piText("Looking."), piToolUse("c1", "bash", map[string]any{"command": piSearchOf}))
	answer := piAnswer("a2", "stop", piUsage(10, 5, 0, 0), piText("The answer."))
	result := piID("r1", piToolResult("c1", "bash", 9, false))
	runPiExtensionFor(t, map[string][]piCall{"run": {
		{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: []any{user, search}},
		// The request after the search failed: Pi ends the loop, retries.
		{Fire: "agent_end", Session: "s", Entries: []any{user, search, result, failed}, Event: map[string]any{"type": "agent_end", "messages": []any{}}},
		{Fire: "agent_end", Session: "s", Entries: []any{user, search, result, failed, answer}, Event: map[string]any{"type": "agent_end", "messages": []any{}}},
		{Fire: "agent_settled", Session: "s", Entries: []any{user, search, result, failed, answer}, Event: map[string]any{"type": "agent_settled", "aborted": false}},
	}}, in.bin)
	reports := in.reports(t, 1)
	if r := reports[0].Turn; r.Status != "completed" || r.FinalText != "The answer." {
		t.Errorf("the turn was reported as %q with %q, want completed with the answer", r.Status, r.FinalText)
	}
}

// Only a search that went out carrying its turn is a reason to report it.
func TestPiExtensionReportsNothingForATurnThatDidNotSearch(t *testing.T) {
	in := newPiStandIn(t)
	user := piID("u1", piUser("question"))
	const afterPipe = `echo hi | jevlin search "q"`
	plain := piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piText("Looking."), piToolUse("c1", "bash", map[string]any{"command": "ls"}))
	piped := piAnswer("a2", "toolUse", piUsage(1, 1, 0, 0), piText("Looking."), piToolUse("c2", "bash", map[string]any{"command": afterPipe}))
	done := piAnswer("a3", "stop", piUsage(1, 1, 0, 0), piText("Done."))
	branch := []any{user, plain, piID("r1", piToolResult("c1", "bash", 1, false)), piped, piID("r2", piToolResult("c2", "bash", 1, false)), done}
	settle := piCall{Fire: "agent_settled", Session: "s", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": false}}
	runPiExtensionFor(t, map[string][]piCall{
		// Nothing in the turn ran a search.
		"no tool": {settle},
		// A command that is not our search.
		"not a search": {{Session: "s", ToolCallID: "c1", Command: "ls", Entries: branch[:2]}, settle},
		// Our search, which the adapter left exactly as the host wrote it
		// because a bridge cannot reach it: the search carries no turn, so
		// there is none to report.
		"unbridged search": {{Session: "s", ToolCallID: "c2", Command: afterPipe, Entries: branch[:4]}, settle},
		// A tool that is not the shell.
		"another tool": {{Session: "s", ToolName: "read", ToolCallID: "c2", Command: piSearchOf, Entries: branch[:4]}, settle},
	}, in.bin)
	in.reports(t, 0)
}

// A turn is handed over once. A run an extension starts under the same user
// message, and searches in, finds the turn already reported.
func TestPiExtensionReportsATurnOnce(t *testing.T) {
	in := newPiStandIn(t)
	user := piID("u1", piUser("question"))
	search := piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piToolUse("c1", "bash", map[string]any{"command": piSearchOf}))
	done := piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText("Done."))
	again := piAnswer("a3", "toolUse", piUsage(1, 1, 0, 0), piToolUse("c2", "bash", map[string]any{"command": piSearchOf}))
	branch := []any{user, search, piID("r1", piToolResult("c1", "bash", 1, false)), done}
	later := append(append([]any{}, branch...), piCustomMessage("an extension's own message"), again)
	settled := func(entries []any) piCall {
		return piCall{Fire: "agent_settled", Session: "s", Entries: entries, Event: map[string]any{"type": "agent_settled", "aborted": false}}
	}
	runPiExtensionFor(t, map[string][]piCall{"run": {
		{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]}, settled(branch),
		{Session: "s", ToolCallID: "c2", Command: piSearchOf, Entries: later}, settled(append(later, piID("r2", piToolResult("c2", "bash", 1, false)))),
	}}, in.bin)
	in.reports(t, 1)
}

// How the turn ended is the last answer's own stop reason, and whether the
// run was aborted — which speaks for the last turn on the branch only.
func TestPiExtensionReportsHowTheTurnEnded(t *testing.T) {
	user := func(id string) map[string]any { return piID(id, piUser("question "+id)) }
	search := func(id, call string) map[string]any {
		return piAnswer(id, "toolUse", piUsage(1, 1, 0, 0), piText("Looking."), piToolUse(call, "bash", map[string]any{"command": piSearchOf}))
	}
	type tc struct {
		name    string
		aborted bool
		last    map[string]any
		want    string
		answer  string
	}
	for _, c := range []tc{
		{"completed", false, piAnswer("z", "stop", piUsage(1, 1, 0, 0), piText("An answer.")), "completed", "An answer."},
		{"aborted by the run", true, piAnswer("z", "toolUse", piUsage(1, 1, 0, 0), piText("Half an answer.")), "interrupted", ""},
		{"an aborted answer", false, piAnswer("z", "aborted", piUsage(1, 1, 0, 0), piText("Cut short.")), "interrupted", ""},
		{"an answer that failed", false, piAnswer("z", "error", piUsage(0, 0, 0, 0)), "failed", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := newPiStandIn(t)
			branch := []any{user("u1"), search("a1", "c1"), piID("r1", piToolResult("c1", "bash", 1, false)), c.last}
			runPiExtensionFor(t, map[string][]piCall{"run": {
				{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]},
				{Fire: "agent_settled", Session: "s", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": c.aborted}},
			}}, in.bin)
			r := in.reports(t, 1)[0].Turn
			if r.Status != c.want || r.FinalText != c.answer {
				t.Errorf("status %q with %q, want %q with %q", r.Status, r.FinalText, c.want, c.answer)
			}
		})
	}

	// Two turns searched in one run — the user spoke again while it ran. Each
	// is reported as itself, and only the last is the one the abort cut short.
	in := newPiStandIn(t)
	branch := []any{
		user("u1"), search("a1", "c1"), piID("r1", piToolResult("c1", "bash", 1, false)), piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText("First answer.")),
		user("u2"), search("a3", "c3"), piID("r3", piToolResult("c3", "bash", 1, false)), piAnswer("a4", "toolUse", piUsage(1, 1, 0, 0), piText("Second, cut off.")),
	}
	runPiExtensionFor(t, map[string][]piCall{"run": {
		{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]},
		{Session: "s", ToolCallID: "c3", Command: piSearchOf, Entries: branch[:6]},
		{Fire: "agent_settled", Session: "s", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": true}},
	}}, in.bin)
	reports := in.reports(t, 2)
	byTurn := map[string]hostTurn{reports[0].Turn.Turn: reports[0].Turn, reports[1].Turn.Turn: reports[1].Turn}
	if u1, u2 := byTurn["u1"], byTurn["u2"]; u1.Status != "completed" || u1.FinalText != "First answer." || u2.Status != "interrupted" || u2.FinalText != "" {
		t.Errorf("u1 = %+v, u2 = %+v", u1, u2)
	}
}

// What leaves is scrubbed before it leaves, at every secret's boundary: the
// binary scrubs again, but a pipe is not where a secret should wait.
func TestPiExtensionScrubsTheTurnBeforeItLeaves(t *testing.T) {
	in := newPiStandIn(t)
	cases := map[string][]piCall{}
	inputs := traceBoundaryInputs()
	branchFor := func(text string) []any {
		return []any{piID("u1", piUser(text)), piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piText("Looking."), piToolUse("c1", "bash", map[string]any{"command": piSearchOf})),
			piID("r1", piToolResult("c1", "bash", 1, false)), piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText(text))}
	}
	for name, text := range inputs {
		branch := branchFor(text)
		cases[name] = []piCall{
			{Session: "s-" + name, ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]},
			{Fire: "agent_settled", Session: "s-" + name, Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": false}},
		}
	}
	// A piece over the complete-source budget is left out whole, never sliced.
	oversize := "sk-SyntheticCredentialBody0123456789 " + strings.Repeat("x", hookTailBytes)
	branch := branchFor(oversize)
	cases["source_budget"] = []piCall{
		{Session: "s-over", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]},
		{Fire: "agent_settled", Session: "s-over", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": false}},
	}
	runPiExtensionFor(t, cases, in.bin)
	reports := in.reports(t, len(cases))
	seen := 0
	for _, r := range reports {
		name := strings.TrimPrefix(r.Turn.Session, "s-")
		if name == "over" {
			if r.Turn.UserText != "" || r.Turn.FinalText != "" || strings.Contains(r.Raw, "CredentialBody0123456789") {
				t.Errorf("an over-budget piece was sent, or sliced: user %d bytes, final %d bytes", len(r.Turn.UserText), len(r.Turn.FinalText))
			}
			seen++
			continue
		}
		text, ok := inputs[name]
		if !ok {
			t.Fatalf("a report for no case: %q", r.Turn.Session)
		}
		want := redact.TraceText(text)
		if r.Turn.UserText != want || r.Turn.FinalText != want {
			t.Errorf("%s: scrubbed user %d bytes, final %d bytes, want %d each, the complete-source scrub", name, len(r.Turn.UserText), len(r.Turn.FinalText), len(want))
		}
		assertNoCanaryFragments(t, r.Raw)
		seen++
	}
	if seen != len(cases) {
		t.Fatalf("checked %d of %d cases", seen, len(cases))
	}
}

// The extension is an observer on the way out too: whatever it is handed when
// a run settles, it neither throws nor returns anything, with a binary to hand
// a turn to or without one.
func TestPiExtensionNeverFailsARunWhenItSettles(t *testing.T) {
	user := piID("u1", piUser("question"))
	search := piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piToolUse("c1", "bash", map[string]any{"command": piSearchOf}))
	good := []any{user, search, piID("r1", piToolResult("c1", "bash", 1, false)), piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText("Done."))}
	call := piCall{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: good[:2]}
	settled := func(c piCall) piCall {
		c.Fire, c.Command, c.ToolCallID = "agent_settled", "", ""
		return c
	}
	cases := map[string][]piCall{
		"well formed":            {call, settled(piCall{Session: "s", Entries: good})},
		"branch unreadable":      {call, settled(piCall{Session: "s", BranchThrows: true})},
		"entries not a list":     {call, settled(piCall{Session: "s", Entries: "nonsense"})},
		"junk entries":           {call, settled(piCall{Session: "s", Entries: []any{nil, 7, "x", map[string]any{"type": "message"}}})},
		"no session manager":     {call, settled(piCall{NoSession: true})},
		"session manager throws": {call, settled(piCall{SessionThrows: true})},
		"no event":               {call, {Fire: "agent_settled", Session: "s", Entries: good, Event: map[string]any{}}},
		"nothing at all":         {{Fire: "agent_settled", NoSession: true, Event: map[string]any{}}},
	}
	for _, bin := range []string{"", newPiStandIn(t).bin} {
		for name, outcomes := range runPiExtensionFor(t, cases, bin) {
			for i, o := range outcomes {
				if o.Threw != "" || strings.TrimSpace(string(o.Returned)) != "null" {
					t.Errorf("%s (bin %q), call %d: threw %q, returned %s", name, bin, i, o.Threw, o.Returned)
				}
			}
		}
	}
}

// The same, over what a live Pi 1.3.0 did (testdata/pi): three turns of one
// resumed session, the second with a request Pi retried inside its prompt, the
// third without a search, and an interrupted run in another session. Every
// expectation but the extension's own is read back out of the session file Pi
// wrote, not typed here.
func TestPiTurnEndOfARealSession(t *testing.T) {
	in := newPiStandIn(t)
	header, file := loadPiSession(t)
	users := userEntryIDs(file)
	if len(users) != 3 {
		t.Fatalf("the captured session has %d user messages, want 3", len(users))
	}
	// What Pi recorded of each tool, in the order it ran them, by turn.
	type outcome struct {
		ms int64
		ok bool
	}
	ran := map[string][]outcome{}
	turn := ""
	for _, e := range file {
		m, _ := e["message"].(map[string]any)
		switch {
		case m != nil && m["role"] == "user":
			turn = e["id"].(string)
		case m != nil && m["role"] == "toolResult":
			ms, _ := m["durationMs"].(float64)
			ran[turn] = append(ran[turn], outcome{int64(ms), m["isError"] == false})
		}
	}

	cases := map[string][]piCall{}
	sids := map[string]string{}
	var dUser string
	for _, r := range loadPiRecording(t) {
		c := piCall{Fire: r.Ev, Session: r.Sid, Entries: r.Branch, Event: r.Event}
		key := "resumed session"
		if r.Run == "D" {
			key = "interrupted session"
			for _, e := range r.Branch {
				if m, _ := e.(map[string]any)["message"].(map[string]any); m != nil && m["role"] == "user" {
					dUser = e.(map[string]any)["id"].(string)
				}
			}
		}
		sids[r.Run] = r.Sid
		cases[key] = append(cases[key], c)
	}
	if sids["A"] != header["id"] {
		t.Fatalf("the recorded events and the session file are not one session")
	}
	for name, outcomes := range runPiExtensionFor(t, cases, in.bin) {
		for i, o := range outcomes {
			if o.Threw != "" || strings.TrimSpace(string(o.Returned)) != "null" {
				t.Errorf("%s, event %d: threw %q, returned %s", name, i, o.Threw, o.Returned)
			}
		}
	}

	// Three reports: the first two turns of the resumed session, which
	// searched, and the interrupted one. The third turn searched nothing, and
	// the second saw agent_end twice and was still reported once.
	reports := in.reports(t, 3)
	by := map[string]hostTurn{}
	for _, r := range reports {
		by[r.Turn.Session+"|"+r.Turn.Turn] = r.Turn
		for _, leak := range []string{"SYSTEM-PROMPT-CANARY", "exit 3", "synthetic search result", "arguments", "stadium designer", "stadium architect", "stadium builder"} {
			if strings.Contains(r.Raw, leak) {
				t.Errorf("the extension piped %q", leak)
			}
		}
	}
	if _, ok := by[sids["A"]+"|"+users[2]]; ok {
		t.Error("a turn that searched nothing was reported")
	}
	steps := func(h hostTurn) string {
		var kinds []string
		for _, s := range h.Steps {
			kinds = append(kinds, s.Kind)
		}
		return strings.Join(kinds, ",")
	}

	a, ok := by[sids["A"]+"|"+users[0]]
	if !ok {
		t.Fatalf("the first turn was not reported: %v", by)
	}
	if a.Status != "completed" || a.UserText != "synthetic question: who designed the stadium?" ||
		a.FinalText != "The stadium was designed by a synthetic architect." || a.Model != "stub-1" ||
		steps(a) != "assistant,search,assistant,search,tool" {
		t.Errorf("turn 1 = %+v", a)
	}
	// Each tool step carries what Pi recorded for it, in the order it ran.
	tools := a.Steps[:0:0]
	for _, s := range a.Steps {
		if s.Kind != "assistant" {
			tools = append(tools, s)
		}
	}
	if len(tools) != len(ran[users[0]]) {
		t.Fatalf("turn 1 has %d tool steps, Pi ran %d", len(tools), len(ran[users[0]]))
	}
	for i, s := range tools {
		if want := ran[users[0]][i]; s.Ms != want.ms || s.OK == nil || *s.OK != want.ok {
			t.Errorf("tool step %d = %+v, Pi recorded %+v", i, s, want)
		}
	}
	if last := tools[len(tools)-1]; last.Kind != "tool" || last.Name != "bash" || *last.OK {
		t.Errorf("the failing command is %+v, want a bash tool that failed", last)
	}

	b, ok := by[sids["B"]+"|"+users[1]]
	if !ok {
		t.Fatalf("the second turn was not reported: %v", by)
	}
	if b.Status != "completed" || b.UserText != "synthetic follow-up: and who built it?" || b.FinalText != "It was built by a synthetic contractor." || steps(b) != "assistant,search" {
		t.Errorf("turn 2 = %+v", b)
	}

	d, ok := by[sids["D"]+"|"+dUser]
	if !ok {
		t.Fatalf("the interrupted turn was not reported: %v", by)
	}
	if d.Status != "interrupted" || d.FinalText != "" || d.UserText != "synthetic question: how many seats?" || steps(d) != "assistant,search" {
		t.Errorf("interrupted turn = %+v", d)
	}
}

// The whole path, with nothing stood in but Pi and the router: the extension
// rewrites a search, the search runs as the real binary and is served by a
// loopback router, the run settles, the extension hands the turn to the real
// binary's `hook turn pi`, and the router receives the turn end. Asserted on
// what the router received: the turn end carries the turn id the search did,
// and none of what a tool was given or returned.
func TestPiTurnEndReachesTheRouterJoinedToItsSearch(t *testing.T) {
	const sid = "01a152f1-14c4-76ba-b318-0a6107087104"
	searchReq, turnRec, turnRaw := piPathToTheRouter(t, sid, true)
	if searchReq == nil || turnRec == nil {
		t.Fatalf("the router received a search: %v, a turn end: %v", searchReq != nil, turnRec != nil)
	}
	if searchReq.Trace.Harness != "pi" || searchReq.Trace.TurnID != traceHash(sid+"|6ca53764") {
		t.Fatalf("the search's trace = %+v", searchReq.Trace)
	}
	// The turn end names the turn the search did.
	if turnRec.TurnID != searchReq.Trace.TurnID || turnRec.SessionID != searchReq.Trace.SessionID || turnRec.Harness != "pi" {
		t.Errorf("turn end ids %q/%q/%q, the search's %q/%q", turnRec.SessionID, turnRec.TurnID, turnRec.Harness, searchReq.Trace.SessionID, searchReq.Trace.TurnID)
	}
	if turnRec.Status != turnCompleted || turnRec.UserText != "who designed the stadium?" || turnRec.FinalText != "A synthetic architect." {
		t.Errorf("turn end = %+v", turnRec)
	}
	if len(turnRec.Steps) != 3 || turnRec.Steps[1].Kind != "search" || turnRec.Steps[1].CallID != searchReq.Trace.CallID || turnRec.Steps[2].Name != "read" {
		t.Errorf("steps = %+v, want the search joined to the call id %q", turnRec.Steps, searchReq.Trace.CallID)
	}
	for _, leak := range []string{"secret.txt", "TOOL OUTPUT", "stadium designer", "JEVLIN_TRACE_BRIDGE", sid, "6ca53764", "c-search"} {
		if strings.Contains(turnRaw, leak) {
			t.Errorf("the router received %q", leak)
		}
	}
}

// The same run in an installation that did not turn the turn end on: the
// search is served and traced as before, and the router is told nothing of the
// turn. The extension hands the turn over regardless; the binary declines it.
func TestPiTurnEndStaysHomeUnlessTheInstallationOptedIn(t *testing.T) {
	searchReq, turnRec, _ := piPathToTheRouter(t, "01a152f1-14c4-76ba-b318-0a6107087104", false)
	if searchReq == nil || searchReq.Trace.TurnID == "" {
		t.Fatal("the search was not served with its turn")
	}
	if turnRec != nil {
		t.Errorf("the router received a turn end from an installation that did not opt in: %+v", turnRec)
	}
}

// piPathToTheRouter runs the extension, the real binary and a loopback router
// through one searched turn and returns what the router received. With the
// turn end off it waits long enough for one that was wrongly sent to arrive.
func piPathToTheRouter(t *testing.T, sid string, optIn bool) (searchReq *execRouterRequest, turnRec *turnEndRecord, turnRaw string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the search runs through sh")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded Pi extension")
	}
	in := newExecInstallation(t)
	if optIn {
		doc, err := os.ReadFile(in.cfg) // #nosec G304 -- the config this test just wrote under its own temp directory
		if err != nil {
			t.Fatal(err)
		}
		// The installation opts in to the turn end.
		if err := os.WriteFile(in.cfg, bytes.Replace(doc, []byte("[miner]\n"), []byte("[miner]\nturn_end = true\n"), 1), 0o600); err != nil { // #nosec G703 -- the config this test wrote under its own temp directory
			t.Fatal(err)
		}
	}

	search := "'" + in.bin + "' search -config '" + in.cfg + "' -format model \"stadium designer\""
	user := piID("6ca53764", piUser("who designed the stadium?"))
	asked := piAnswer("2d06d93e", "toolUse", piUsage(120, 18, 0, 0), piText("Let me look that up."),
		piToolUse("c-search", "bash", map[string]any{"command": search}), piToolUse("c-cat", "read", map[string]any{"path": "/home/someone/secret.txt"}))
	branch := []any{user, asked, piID("f1", piToolResult("c-search", "bash", 5200, false)), piID("f2", piToolResult("c-cat", "read", 12, false)),
		piAnswer("f14f6406", "stop", piUsage(150, 30, 0, 0), piText("A synthetic architect."))}

	script := `
 const fs = await import('node:fs');
 const { execFileSync } = await import('node:child_process');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const mod = await import('data:text/javascript;base64,'+Buffer.from(input.extension).toString('base64'));
 const handlers = {};
 mod.default({ on: (ev, fn) => { handlers[ev] = fn; } });
 const ctx = (entries) => ({ sessionManager: { getSessionId: () => input.sid, getBranch: () => entries } });
 const event = { type: 'tool_call', toolName: 'bash', toolCallId: 'c-search', input: { command: input.search } };
 await handlers.tool_call(event, ctx(input.branch.slice(0, 2)));
 // Pi's bash tool runs what the handler left in the command.
 execFileSync('sh', ['-c', event.input.command], { stdio: 'ignore' });
 await handlers.agent_settled({ type: 'agent_settled', aborted: false }, ctx(input.branch));`
	body, err := json.Marshal(map[string]any{"extension": renderAgentScriptFor(piExtensionTS, shellPOSIX, in.cfg, in.bin), "sid": sid, "search": search, "branch": branch})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = in.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extension: %v\n%s", err, out)
	}

	look := func() {
		for _, r := range in.router.received() {
			r := r
			var rec turnEndRecord
			if r.Trace != nil {
				searchReq = &r
			} else if json.Unmarshal(r.Raw, &rec) == nil && rec.Status != "" {
				turnRec, turnRaw = &rec, string(r.Raw)
			}
		}
	}
	for deadline := time.Now().Add(15 * time.Second); (searchReq == nil || (optIn && turnRec == nil)) && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		look()
	}
	if !optIn {
		time.Sleep(2 * time.Second)
		look()
	}
	return searchReq, turnRec, turnRaw
}

// Pi persists its system prompt as a message entry of role "system": before the
// first message, and again when the prompt or the tools change, which can be
// inside a turn. It is not a turn, and it is never read; the canary is in both
// places a prompt has been seen.
const piSystemCanary = "SYSTEM PROMPT MUST NEVER BE READ"

func piSystem(id string) map[string]any {
	return piID(id, map[string]any{"type": "message", "message": map[string]any{
		"role": "system", "content": piSystemCanary, "sections": map[string]any{"preamble": piSystemCanary},
	}})
}

func TestPiExtensionNeverReadsTheSystemPrompt(t *testing.T) {
	in := newPiStandIn(t)
	branch := []any{
		piSystem("sys0"), piID("u1", piUser("question")),
		piSystem("sys1"),
		piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piText("Looking."), piToolUse("c1", "bash", map[string]any{"command": piSearchOf})),
		piID("r1", piToolResult("c1", "bash", 1, false)),
		piSystem("sys2"),
		piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText("Done.")),
	}
	got := runPiExtensionFor(t, map[string][]piCall{"run": {
		{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:4]},
		{Fire: "agent_settled", Session: "s", Entries: branch, Event: map[string]any{"type": "agent_settled", "aborted": false}},
	}}, in.bin)["run"]
	// A system entry inside the turn does not open another one.
	if env := piBridge(t, got[0]); env.TurnID != traceHash("s|u1") {
		t.Errorf("turn_id = %q, want the user entry's (hash(s|u1))", env.TurnID)
	}
	r := in.reports(t, 1)[0]
	if r.Turn.Turn != "u1" || r.Turn.UserText != "question" || r.Turn.FinalText != "Done." {
		t.Errorf("turn = %+v", r.Turn)
	}
	for _, raw := range []string{got[0].Command, r.Raw} {
		if strings.Contains(raw, piSystemCanary) || strings.Contains(raw, "sys") {
			t.Errorf("the system prompt, or the id of its entry, reached %q", raw)
		}
	}
}

// A run that settles while the branch cannot be read leaves its turns for the
// next run to settle, rather than losing them.
func TestPiExtensionKeepsATurnUntilTheBranchCanBeRead(t *testing.T) {
	in := newPiStandIn(t)
	branch := []any{
		piID("u1", piUser("question")),
		piAnswer("a1", "toolUse", piUsage(1, 1, 0, 0), piToolUse("c1", "bash", map[string]any{"command": piSearchOf})),
		piID("r1", piToolResult("c1", "bash", 1, false)),
		piAnswer("a2", "stop", piUsage(1, 1, 0, 0), piText("Done.")),
	}
	settled := piCall{Fire: "agent_settled", Session: "s", Event: map[string]any{"type": "agent_settled", "aborted": false}}
	unreadable, readable := settled, settled
	unreadable.BranchThrows = true
	readable.Entries = branch
	runPiExtensionFor(t, map[string][]piCall{"run": {
		{Session: "s", ToolCallID: "c1", Command: piSearchOf, Entries: branch[:2]}, unreadable, readable,
	}}, in.bin)
	if r := in.reports(t, 1)[0].Turn; r.Turn != "u1" || r.FinalText != "Done." {
		t.Errorf("turn = %+v", r)
	}
}
