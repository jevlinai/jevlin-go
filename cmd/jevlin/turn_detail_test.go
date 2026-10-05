package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A Claude Code transcript with the record shapes a real one has (taken from
// a 2.1.289 session: one record per content block, usage repeated on each,
// promptId on user records, tool results as `user` records), holding an
// earlier turn and then the turn under test.
func turnTranscript() string {
	line := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	asst := func(ts, msgID string, blocks ...map[string]any) string {
		return line(map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{
			"id": msgID, "role": "assistant", "model": "claude-opus-5-5", "content": blocks,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 990, "cache_creation_input_tokens": 0, "output_tokens": 50},
		}})
	}
	result := func(ts, toolUseID, content string, isError bool) string {
		return line(map[string]any{"type": "user", "promptId": "p1", "timestamp": ts, "toolUseResult": map[string]any{"stdout": content},
			"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": toolUseID, "content": content, "is_error": isError}}}})
	}
	text := func(t string) map[string]any { return map[string]any{"type": "text", "text": t} }
	use := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
	}
	return strings.Join([]string{
		// an earlier turn
		line(map[string]any{"type": "user", "promptId": "p0", "timestamp": "2026-10-05T10:00:00.000Z", "message": map[string]any{"role": "user", "content": "an earlier question"}}),
		asst("2026-10-05T10:00:02.000Z", "m0", text("an earlier answer")),
		// the turn under test
		line(map[string]any{"type": "queue-operation", "timestamp": "2026-10-05T10:01:00.000Z"}),
		line(map[string]any{"type": "user", "promptId": "p1", "timestamp": "2026-10-05T10:01:00.010Z",
			"message": map[string]any{"role": "user", "content": "Who won? My key is sk-" + strings.Repeat("A", 40) + " by the way."}}),
		line(map[string]any{"type": "attachment", "timestamp": "2026-10-05T10:01:00.011Z"}),
		asst("2026-10-05T10:01:02.000Z", "m1", map[string]any{"type": "thinking", "thinking": "PRIVATE REASONING"}),
		asst("2026-10-05T10:01:02.001Z", "m1", text("I'll check the notes first.")),
		asst("2026-10-05T10:01:02.002Z", "m1", use("tu_read", "Read", map[string]any{"file_path": "/Users/someone/secret/plan.md"})),
		result("2026-10-05T10:01:02.212Z", "tu_read", "FILE CONTENTS: the acquisition closes Friday", false),
		// a Skill call, then the skill body the host injects as a `user` record
		asst("2026-10-05T10:01:03.000Z", "m2", use("tu_skill", "Skill", map[string]any{"skill": "jevlin"})),
		result("2026-10-05T10:01:03.050Z", "tu_skill", "launching skill", false),
		line(map[string]any{"type": "user", "isMeta": true, "sourceToolUseID": "tu_skill", "promptId": "p1", "timestamp": "2026-10-05T10:01:03.051Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "SKILL BODY TEXT"}}}}),
		asst("2026-10-05T10:01:04.000Z", "m3", text("Now the search.")),
		asst("2026-10-05T10:01:04.001Z", "m3", use("tu_search", "Bash", map[string]any{"command": `jevlin search "2020 Korean Series MVP"`})),
		result("2026-10-05T10:01:09.901Z", "tu_search", "1. Yang Eui-ji named MVP ...", false),
		// a subagent's own records, in the same file on some versions
		line(map[string]any{"type": "assistant", "isSidechain": true, "timestamp": "2026-10-05T10:01:10.000Z",
			"message": map[string]any{"id": "side", "role": "assistant", "content": []map[string]any{text("SIDECHAIN TEXT")}}}),
		asst("2026-10-05T10:01:11.000Z", "m4", use("tu_bash", "Bash", map[string]any{"command": "cat /etc/hosts && rm -f build.log"})),
		result("2026-10-05T10:01:11.400Z", "tu_bash", "permission denied", true),
		asst("2026-10-05T10:01:12.000Z", "m5", use("tu_odd", "a tool with spaces", map[string]any{})),
		asst("2026-10-05T10:01:14.000Z", "m6", text("The MVP was Yang Eui-ji.")),
	}, "\n")
}

func TestClaudeTurnDetail(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.fs.files["/t/sess.jsonl"] = []byte(turnTranscript())
	h.served(traceHash("sess|p1"))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "The MVP was Yang Eui-ji.",
	}))
	rec := h.record(t)
	raw := string(h.fs.files[h.queued[0]])

	// Nothing of a tool's input or output, no private reasoning, nothing the
	// host injected, nothing from another turn or a subagent's own records.
	for _, leak := range []string{
		"FILE CONTENTS", "acquisition", "/Users/someone", "plan.md", "/etc/hosts", "build.log", "permission denied",
		"Yang Eui-ji named MVP", "2020 Korean Series MVP", "PRIVATE REASONING", "SKILL BODY", "SIDECHAIN", "an earlier",
		"sk-AAAA", "a tool with spaces",
	} {
		if strings.Contains(raw, leak) {
			t.Errorf("the queued record contains %q", leak)
		}
	}

	if !strings.HasPrefix(rec.UserText, "Who won? My key is [REDACTED]") || rec.UserTruncated {
		t.Errorf("user text = %q", rec.UserText)
	}
	if rec.Model != "claude-opus-5-5" {
		t.Errorf("model = %q", rec.Model)
	}
	// Six replies (m1..m6), each counted once though m1 and m3 span records.
	if rec.Usage == nil || rec.Usage.InputTokens != 6*1000 || rec.Usage.OutputTokens != 6*50 {
		t.Errorf("usage = %+v", rec.Usage)
	}

	type brief struct {
		kind, name, text string
		ok               string
	}
	var got []brief
	for _, st := range rec.Steps {
		ok := "-"
		if st.OK != nil {
			ok = map[bool]string{true: "ok", false: "failed"}[*st.OK]
		}
		got = append(got, brief{st.Kind, st.Name, st.Text, ok})
	}
	want := []brief{
		{"assistant", "", "I'll check the notes first.", "-"},
		{"tool", "Read", "", "ok"},
		{"tool", "Skill", "", "ok"},
		{"assistant", "", "Now the search.", "-"},
		{"search", "", "", "ok"},
		{"tool", "Bash", "", "failed"},
		{"tool", "tool", "", "-"}, // a name that is not an identifier; and it never returned
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The search step carries the id its trace carried; timings come from the
	// records' own clocks.
	if s := rec.Steps[4]; s.CallID != traceHash("sess|tu_search") || s.Ms != 5900 {
		t.Errorf("search step = %+v", s)
	}
	if s := rec.Steps[1]; s.Ms != 210 {
		t.Errorf("Read took %d ms, want 210", s.Ms)
	}
	// The closing text is the final answer, sent once.
	if rec.FinalText != "The MVP was Yang Eui-ji." {
		t.Errorf("final = %q", rec.FinalText)
	}
}

// When the turn cannot be read whole, the turn end goes out as before: the
// final answer, and no conversation.
func TestClaudeTurnDetailIsAllOrNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		transcript string
		over       map[string]any
	}{
		"no transcript path":              {turnTranscript(), map[string]any{}},
		"the transcript cannot be read":   {"", map[string]any{"transcript_path": "/t/missing.jsonl"}},
		"its last turn is another prompt": {turnTranscript(), map[string]any{"transcript_path": "/t/sess.jsonl", "prompt_id": "p2"}},
		"no turn start in what was read":  {`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"orphan"}]}}`, map[string]any{"transcript_path": "/t/sess.jsonl"}},
	} {
		h := newTurnEndHarness(nil, true)
		if tc.transcript != "" {
			h.fs.files["/t/sess.jsonl"] = []byte(tc.transcript)
		}
		turn := "p1"
		if v, ok := tc.over["prompt_id"].(string); ok {
			turn = v
		}
		h.served(traceHash("sess|" + turn))
		tc.over["last_assistant_message"] = "the answer"
		runHook(t, h.ops, h.hc, "flush", claudeStop(tc.over))
		rec := h.record(t)
		if rec.FinalText != "the answer" || rec.UserText != "" || len(rec.Steps) != 0 || rec.Usage != nil || rec.Model != "" {
			t.Errorf("%s: %+v", name, rec)
		}
	}
}

func TestHeadTextKeepsTheStart(t *testing.T) {
	long := "the question starts here " + strings.Repeat("界", 20000)
	out, chars, truncated := headText(long, 1024)
	if !strings.HasPrefix(out, "the question starts here") || len(out) > 1024 || !truncated || chars != 25+20000 {
		t.Errorf("head: %d bytes, truncated=%v, chars=%d", len(out), truncated, chars)
	}
	if strings.ContainsRune(out, '�') {
		t.Error("the cut split a character")
	}
	if out, _, truncated := headText(strings.Repeat("x", hookTailBytes+1), 1024); out != "" || !truncated {
		t.Error("a message too large to scrub whole was kept in part")
	}
}
