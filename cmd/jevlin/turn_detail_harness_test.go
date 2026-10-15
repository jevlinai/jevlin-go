package main

import (
	"encoding/json"
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
