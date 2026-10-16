package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// harnessTurnTranscript is a session whose last turn was started by a `user`
// record Claude Code wrote, not the participant: start is that record's
// fields. The turn then runs one search, as the main agent does after a
// background subagent's result comes back (issue #70, case 2).
func harnessTurnTranscript(start map[string]any) string {
	line := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	start["type"], start["promptId"], start["timestamp"] = "user", "p1", "2026-10-07T13:28:06.000Z"
	return strings.Join([]string{
		line(map[string]any{"type": "user", "promptId": "p0", "timestamp": "2026-10-07T13:27:39.000Z",
			"message": map[string]any{"role": "user", "content": "Launch two subagents in parallel"}}),
		line(map[string]any{"type": "assistant", "timestamp": "2026-10-07T13:27:45.000Z",
			"message": map[string]any{"id": "m0", "role": "assistant", "content": []map[string]any{{"type": "text", "text": "Both subagents are running."}}}}),
		line(start),
		line(map[string]any{"type": "assistant", "timestamp": "2026-10-07T13:28:09.000Z",
			"message": map[string]any{"id": "m1", "role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "tu_search", "name": "Bash", "input": map[string]any{"command": `jevlin search "who won"`}}}}}),
		line(map[string]any{"type": "user", "promptId": "p1", "timestamp": "2026-10-07T13:28:10.000Z", "toolUseResult": map[string]any{"stdout": "1. result"},
			"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tu_search", "content": "1. result"}}}}),
	}, "\n")
}

// The turn end promises the router "the user's message" (invariant 2). A
// `user` record the harness wrote is not one: Claude Code hands a background
// task's result back as a user record starting `<task-notification>`, with
// the task's output file under the participant's account in it, and a
// continuation or autonomous-loop tick is marked isMeta. Either may still
// start the turn, which is reported with its searches, but with no user
// text.
func TestATurnTheHarnessStartedIsSentWithoutUserText(t *testing.T) {
	for name, start := range map[string]map[string]any{
		"a background task's notification": {"message": map[string]any{"role": "user", "content": "<task-notification>\n<task-id>a64e</task-id>\n" +
			"<output-file>/private/tmp/claude-501/-Users-someone-work/tasks/a64e.output</output-file>\n<result>SUBAGENT OUTPUT</result>\n</task-notification>"}},
		"a notification in a text block": {"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text",
			"text": "<task-notification>\n<output-file>/private/tmp/claude-501/-Users-someone-work/tasks/b.output</output-file>\n</task-notification>"}}}},
		"a continuation the harness wrote": {"isMeta": true, "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text",
			"text": "Continue from where you left off. SUBAGENT OUTPUT /private/tmp/claude-501/-Users-someone-work"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTurnEndHarness(nil, true)
			h.fs.files["/t/sess.jsonl"] = []byte(harnessTurnTranscript(start))
			h.served(traceHash("sess|p1"))
			runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{
				"transcript_path": "/t/sess.jsonl", "last_assistant_message": "Here is what they found.",
			}))
			rec := h.record(t)
			raw := string(h.fs.files[h.queued[0]])
			for _, leak := range []string{"task-notification", "/private/tmp", "Users-someone", "SUBAGENT OUTPUT", "Continue from where"} {
				if strings.Contains(raw, leak) {
					t.Errorf("the turn end carries %q, which the harness wrote:\n%s", leak, raw)
				}
			}
			if rec.UserText != "" {
				t.Errorf("UserText = %q, want none for a turn the harness started", rec.UserText)
			}
			searched := false
			for _, st := range rec.Steps {
				if st.Kind == "search" {
					searched = true
				}
			}
			if !searched {
				t.Errorf("the turn's search was not reported: %+v", rec.Steps)
			}
		})
	}
}

// harnessTurnRecord runs the real Stop hook over a session whose last turn
// was started by start and searched once, and returns the queued record and
// its bytes.
func harnessTurnRecord(t *testing.T, start map[string]any) (turnEndRecord, string) {
	t.Helper()
	h := newTurnEndHarness(nil, true)
	h.fs.files["/t/sess.jsonl"] = []byte(harnessTurnTranscript(start))
	h.served(traceHash("sess|p1"))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "Here is what they found.",
	}))
	return h.record(t), string(h.fs.files[h.queued[0]])
}

func reportsItsSearch(rec turnEndRecord) bool {
	for _, st := range rec.Steps {
		if st.Kind == "search" {
			return true
		}
	}
	return false
}

// leadingTag is the envelope tag a fixture text opens with, or "".
var leadingTag = regexp.MustCompile(`^<[a-z-]+>`)

// claude_code_turn_starts.jsonl holds one real turn start of each kind,
// taken from Claude Code 2.1.263 to 2.1.292 sessions and reduced by a
// script: only `type`, the three flags (isMeta, isCompactSummary,
// isVisibleInTranscriptOnly), `entrypoint`, `version` and `message.role`
// are kept; `origin` is cut to its `kind`; the content keeps its shape, a
// string or that many text blocks; and every text is replaced by
// PARTICIPANT TEXT or HARNESS TEXT, keeping only the record's leading
// envelope tag. Each line is spliced in as the start of a turn that
// searched, and its sentinel says what the turn end must do with it: a
// participant's text is sent as it is, which is what keeps a rule that
// withholds everything from passing; text the harness wrote is not sent,
// nor its tag, and the turn's search is still reported.
func TestRealClaudeCodeTurnStartsSendOnlyTheParticipantsText(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "hook", "claude_code_turn_starts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	participant, harness := 0, 0
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var start map[string]any
		if err := json.Unmarshal([]byte(line), &start); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		rec, sent := harnessTurnRecord(t, start)
		switch {
		case strings.Contains(line, "PARTICIPANT TEXT"):
			participant++
			if rec.UserText != "PARTICIPANT TEXT" {
				t.Errorf("line %d, a participant's message: UserText = %q, want it sent", i+1, rec.UserText)
			}
		case strings.Contains(line, "HARNESS TEXT"):
			harness++
			if rec.UserText != "" {
				t.Errorf("line %d, written by the harness: UserText = %q, want none", i+1, rec.UserText)
			}
			if strings.Contains(sent, "HARNESS TEXT") {
				t.Errorf("line %d: the turn end carries the harness's text:\n%s", i+1, sent)
			}
			var text string
			switch c := start["message"].(map[string]any)["content"].(type) {
			case string:
				text = c
			case []any:
				text, _ = c[0].(map[string]any)["text"].(string)
			}
			if tag := leadingTag.FindString(text); tag != "" && strings.Contains(sent, tag) {
				t.Errorf("line %d: the turn end carries the tag %s", i+1, tag)
			}
			if !reportsItsSearch(rec) {
				t.Errorf("line %d: the turn's search was not reported: %+v", i+1, rec.Steps)
			}
		default:
			t.Fatalf("line %d carries neither sentinel", i+1)
		}
	}
	if participant != 2 || harness != 6 {
		t.Fatalf("read %d participant and %d harness lines, want 2 and 6", participant, harness)
	}
}

// origin, where a record carries it, must be an object whose kind is
// exactly "human", or the text is withheld. Neither record here carries a
// flag or a tag, so only origin can withhold it. A kind nobody has seen is
// not a participant. Nor is an origin of another shape, and that one also
// holds the decoding: were origin a typed field, a string there would fail
// the whole record's decode and drop it, an earlier turn's message would
// become the start, the Stop's prompt check would fail, and the turn end
// would go out without its steps.
func TestATurnStartWhoseOriginIsNotAHumanObjectSendsNoUserText(t *testing.T) {
	for name, origin := range map[string]any{
		"a kind nobody has seen":  map[string]any{"kind": "scheduled"},
		"a string, not an object": "human",
	} {
		t.Run(name, func(t *testing.T) {
			rec, sent := harnessTurnRecord(t, map[string]any{"origin": origin,
				"message": map[string]any{"role": "user", "content": "ORIGIN TEXT"}})
			if rec.UserText != "" || strings.Contains(sent, "ORIGIN TEXT") {
				t.Errorf("UserText = %q, want none for origin %v", rec.UserText, origin)
			}
			if !reportsItsSearch(rec) {
				t.Errorf("the turn's search was not reported: %+v", rec.Steps)
			}
		})
	}
}
