package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// backgroundTurns is a Claude Code session in which the participant's turn
// p3 launched a background subagent and ended, and the subagent's result
// came back as a task notification that started turn p4. With p4Search, p4
// ran a search of its own too.
func backgroundTurns(withP4, p4Search bool) string {
	line := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	text := func(id, s string) map[string]any {
		return map[string]any{"type": "assistant", "timestamp": "2026-10-17T10:00:01.000Z",
			"message": map[string]any{"id": id, "role": "assistant", "content": []map[string]any{{"type": "text", "text": s}}}}
	}
	lines := []string{
		line(map[string]any{"type": "user", "promptId": "p2", "timestamp": "2026-10-17T09:59:00.000Z",
			"message": map[string]any{"role": "user", "content": "An earlier question"}}),
		line(text("m2", "An earlier answer.")),
		line(map[string]any{"type": "user", "promptId": "p3", "timestamp": "2026-10-17T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": "Research the release in the background"}}),
		line(map[string]any{"type": "assistant", "timestamp": "2026-10-17T10:00:01.000Z",
			"message": map[string]any{"id": "m3", "role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "tu_agent", "name": "Agent", "input": map[string]any{}}}}}),
		line(map[string]any{"type": "user", "promptId": "p3", "timestamp": "2026-10-17T10:00:02.000Z", "toolUseResult": map[string]any{"status": "async_launched"},
			"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tu_agent", "content": "launched"}}}}),
		line(text("m4", "I started a subagent on it.")),
	}
	if withP4 {
		lines = append(lines,
			line(map[string]any{"type": "user", "promptId": "p4", "timestamp": "2026-10-17T10:03:00.000Z",
				"message": map[string]any{"role": "user", "content": "<task-notification>\n<result>SUBAGENT OUTPUT</result>\n</task-notification>"}}))
		if p4Search {
			lines = append(lines,
				line(map[string]any{"type": "assistant", "timestamp": "2026-10-17T10:03:01.000Z",
					"message": map[string]any{"id": "m5", "role": "assistant", "content": []map[string]any{
						{"type": "tool_use", "id": "tu_search", "name": "Bash", "input": map[string]any{"command": `jevlin search "release date"`}}}}}),
				line(map[string]any{"type": "user", "promptId": "p4", "timestamp": "2026-10-17T10:03:02.000Z", "toolUseResult": map[string]any{"stdout": "1. result"},
					"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tu_search", "content": "1. result"}}}}))
		}
		lines = append(lines, line(text("m6", "The subagent found the release date.")))
	}
	return strings.Join(lines, "\n")
}

// subagentServed leaves what `jevlin search` leaves when a Claude Code
// subagent's search is answered: the turn that launched the subagent, and
// the subagent's own lane under the session.
func (h *turnEndHarness) subagentServed(prompt string) {
	markTurnSearched(h.ops, config.Miner{TurnEnd: true, SessionsDir: h.hc.sessionsDir}, &traceEnvelope{
		Harness: "claude-code", SessionID: traceHash("sess|agent-1"), ParentSessionID: traceHash("sess"),
		TurnID: traceHash("sess|" + prompt),
	})
}

func (h *turnEndHarness) records(t *testing.T) []turnEndRecord {
	t.Helper()
	var recs []turnEndRecord
	for _, f := range h.queued {
		var rec turnEndRecord
		if err := json.Unmarshal(h.fs.files[f], &rec); err != nil {
			t.Fatalf("queued file: %v", err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// Issue #70, case 1. A background subagent searches under the turn that
// launched it, after that turn's Stop has found nothing. The next Stop in the
// session reports that turn, late and once: the participant's message, the
// turn's steps, and its last assistant text from the transcript. The turn
// whose Stop it is did not search and is not reported, and a later search
// under the ended turn is not a second end for it.
func TestABackgroundSubagentsSearchIsReportedLateWithTheTurnThatLaunchedIt(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(false, false))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p3",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "I started a subagent on it."}))
	if len(h.queued) != 0 {
		t.Fatalf("p3's own Stop queued %d turn ends before any search was served", len(h.queued))
	}

	h.subagentServed("p3")
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, false))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p4",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "The subagent found the release date."}))
	rec := h.record(t)
	if rec.TurnID != traceHash("sess|p3") || rec.SessionID != traceHash("sess") {
		t.Fatalf("reported turn %s in session %s, want p3's in the session's", rec.TurnID, rec.SessionID)
	}
	if rec.UserText != "Research the release in the background" {
		t.Errorf("UserText = %q, want p3's message", rec.UserText)
	}
	if rec.FinalText != "I started a subagent on it." {
		t.Errorf("FinalText = %q, want p3's last assistant text", rec.FinalText)
	}
	if len(rec.Steps) != 1 || rec.Steps[0].Name != "Agent" {
		t.Errorf("Steps = %+v, want p3's Agent call alone", rec.Steps)
	}
	if raw := string(h.fs.files[h.queued[0]]); strings.Contains(raw, "SUBAGENT OUTPUT") || strings.Contains(raw, "release date") {
		t.Errorf("the late end carries p4's text:\n%s", raw)
	}

	h.queued = nil
	h.subagentServed("p3")
	if _, ok := h.fs.files[turnSearchedPath(h.hc.sessionsDir, traceHash("sess|p3"))]; ok {
		t.Errorf("a search under p3 after p3's end marked it again")
	}
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p5",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "Anything else?"}))
	if len(h.queued) != 0 {
		t.Errorf("a later Stop queued %d more turn ends, want none: p3 ends once", len(h.queued))
	}
}

// When the Stop's own turn searched as well, both are reported: the late
// turn first, then the turn that is ending, each with its own text.
func TestALateTurnAndTheEndingTurnAreBothReported(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.subagentServed("p3")
	h.served(traceHash("sess|p4"))
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, true))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p4",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "The subagent found the release date."}))
	recs := h.records(t)
	if len(recs) != 2 || recs[0].TurnID != traceHash("sess|p3") || recs[1].TurnID != traceHash("sess|p4") {
		t.Fatalf("queued %+v, want p3's end and then p4's", recs)
	}
	if recs[0].FinalText != "I started a subagent on it." || recs[1].FinalText != "The subagent found the release date." {
		t.Errorf("final texts %q and %q, want each turn's own", recs[0].FinalText, recs[1].FinalText)
	}
	if recs[1].UserText != "" || !reportsItsSearch(recs[1]) {
		t.Errorf("p4: UserText %q, steps %+v; want no user text and its search", recs[1].UserText, recs[1].Steps)
	}
}

// Only a subagent's search can land in a turn other than the one running, so
// only a subagent's search marks the session, and a Stop with no such mark
// does not look at earlier turns: a main-agent mark left on an earlier turn
// (one whose Stop never came, an interrupted turn) is not reported by a
// later turn's Stop, even one whose own turn searched and so reads the
// transcript.
func TestOnlyASubagentsSearchSendsAStopLookingAtEarlierTurns(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("sess|p3"))
	h.served(traceHash("sess|p4"))
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, true))
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p4", "transcript_path": "/t/sess.jsonl"}))
	if rec := h.record(t); rec.TurnID != traceHash("sess|p4") {
		t.Errorf("reported turn %s, want p4's own alone", rec.TurnID)
	}
	if _, ok := h.fs.files[turnSearchedPath(h.hc.sessionsDir, traceHash("sess|p3"))]; !ok {
		t.Errorf("the earlier turn's mark was taken by a Stop that was not its own")
	}
}

// Issue #70, case 3. A Codex subagent's search carries its own thread's
// turn, which no Stop of ours ever ends, so it leaves no mark to dangle; a
// Codex main agent's search and a Claude Code subagent's still mark theirs,
// and only the subagent marks its session.
func TestACodexSubagentsSearchLeavesNoMark(t *testing.T) {
	m := config.Miner{TurnEnd: true, SessionsDir: "/home/sessions"}
	turn := traceHash("thread|t1")
	for name, tc := range map[string]struct {
		trace       *traceEnvelope
		turn, owner bool
	}{
		"a Codex subagent":       {&traceEnvelope{Harness: "codex", SessionID: traceHash("agent"), ParentSessionID: traceHash("thread"), TurnID: turn}, false, false},
		"a Codex main agent":     {&traceEnvelope{Harness: "codex", SessionID: traceHash("thread"), TurnID: turn}, true, false},
		"a Claude Code subagent": {&traceEnvelope{Harness: "claude-code", SessionID: traceHash("thread|a"), ParentSessionID: traceHash("thread"), TurnID: turn}, true, true},
	} {
		fs, ops := newFakeHookOps(nil)
		markTurnSearched(ops, m, tc.trace)
		_, marked := fs.files[turnSearchedPath(m.SessionsDir, turn)]
		_, owner := fs.files[sessionSearchedPath(m.SessionsDir, traceHash("thread"))]
		want := 0
		for _, b := range []bool{tc.turn, tc.owner} {
			if b {
				want++
			}
		}
		if marked != tc.turn || owner != tc.owner || len(fs.files) != want {
			t.Errorf("%s: turn marked %v, session marked %v (%d files); want %v and %v", name, marked, owner, len(fs.files), tc.turn, tc.owner)
		}
	}
}

// A search that lands while a Stop takes its turn's mark finds the turn
// already ended and marks nothing: the ended mark is written before the
// mark is removed. Here a subagent's search under p3 runs at the moment
// p3's own Stop removes p3's mark.
func TestASearchLandingWhileTheTurnIsTakenMarksNothing(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("sess|p3"))
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(false, false))
	mark := turnSearchedPath(h.hc.sessionsDir, traceHash("sess|p3"))
	remove := h.ops.remove
	landed := false
	h.ops.remove = func(p string) error {
		err := remove(p)
		if p == mark && !landed {
			landed = true
			h.subagentServed("p3")
		}
		return err
	}
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"prompt_id": "p3",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "I started a subagent on it."}))
	if !landed {
		t.Fatal("p3's Stop never removed p3's mark; the search did not land during the take")
	}
	if rec := h.record(t); rec.TurnID != traceHash("sess|p3") {
		t.Fatalf("reported turn %s, want p3's", rec.TurnID)
	}
	for p := range h.fs.files {
		if p == mark || p == sessionSearchedPath(h.hc.sessionsDir, traceHash("sess")) {
			t.Errorf("a search landing while p3's mark was taken left %s, which a later Stop would take for a second end", p)
		}
	}
}

// A mark beside the turn's ended mark is not a second end. A search that
// looked for the ended mark just before the turn's Stop wrote it leaves its
// mark just after; the next take finds that mark and the ended mark both,
// removes the mark, and reports nothing.
func TestAMarkBesideAnEndedMarkIsNotASecondEnd(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	dir, turn := "/home/sessions", traceHash("sess|p3")
	markTurnSearched(ops, config.Miner{TurnEnd: true, SessionsDir: dir}, &traceEnvelope{TurnID: turn})
	if !takeTurnSearched(ops, dir, turn) {
		t.Fatal("the turn's own take found no mark")
	}
	fs.files[turnSearchedPath(dir, turn)] = nil // the late search's mark
	if takeTurnSearched(ops, dir, turn) {
		t.Error("a mark beside the ended mark was taken as a second end of the turn")
	}
	if _, ok := fs.files[turnSearchedPath(dir, turn)]; ok {
		t.Error("the mark beside the ended mark was left in place")
	}
}
