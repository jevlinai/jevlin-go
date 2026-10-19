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
	"strings"
	"testing"
	"time"
)

// ── a synthetic Pi ──────────────────────────────────────────────────────

// piCall is one tool_call event delivered to the extension. Several calls in
// one case share ONE extension instance, which is how "a new session does
// not inherit the last one's window" and "a fork reconstructs its own
// generation" are asked as questions about process-wide state.
type piCall struct {
	Session      string         `json:"session"`
	Entries      any            `json:"entries"`
	Command      string         `json:"command"`
	ToolCallID   string         `json:"toolCallId"`
	ToolName     string         `json:"toolName"`
	NoSession    bool           `json:"noSession"`
	BranchThrows bool           `json:"branchThrows"`
	Event        map[string]any `json:"event"`
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
  let handler;
  // The synthetic Pi: an extension registers with pi.on(event, handler).
  mod.default({ on: (ev, fn) => { if (ev === 'tool_call') handler = fn; } });
  const outcomes = [];
  for (const c of calls) {
   const event = c.event ?? { type: 'tool_call', toolName: c.toolName || 'bash', toolCallId: c.toolCallId, input: { command: c.command } };
   const ctx = c.noSession ? {} : { sessionManager: {
     getSessionId: () => c.session,
     getBranch: () => { if (c.branchThrows) throw new Error('synthetic host failure'); return c.entries; },
   } };
   const out = { command: null, returned: null, threw: '' };
   try { out.returned = (await handler(event, ctx)) ?? null; } catch (e) { out.threw = String(e); }
   out.command = event?.input?.command ?? null;
   outcomes.push(out);
  }
  result[name] = outcomes;
 }
 process.stdout.write(JSON.stringify(result));`
	input, err := json.Marshal(map[string]any{"extension": renderAgentScript(piExtensionTS, shellPOSIX, testCfg), "cases": cases})
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
