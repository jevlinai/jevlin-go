package main

import (
	"encoding/json"
	"reflect"
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
	markTurnSearched(h.ops, config.Search{TurnEnd: true, SessionsDir: h.hc.sessionsDir}, &traceEnvelope{
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
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p3",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "I started a subagent on it."}))
	if len(h.queued) != 0 {
		t.Fatalf("p3's own Stop queued %d turn ends before any search was served", len(h.queued))
	}

	h.subagentServed("p3")
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, false))
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p4",
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
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p5",
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
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p4",
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
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p4", "transcript_path": "/t/sess.jsonl"}))
	if rec := h.record(t); rec.TurnID != traceHash("sess|p4") {
		t.Errorf("reported turn %s, want p4's own alone", rec.TurnID)
	}
	if _, ok := h.fs.files[turnSearchedPath(h.hc.sessionsDir, traceHash("sess|p3"))]; !ok {
		t.Errorf("the earlier turn's mark was taken by a Stop that was not its own")
	}
}

// Issue #70, case 3. Only Claude Code gives a subagent the turn that
// launched it. A Codex subagent's search carries its own thread's turn and an
// opencode subagent's its child session's, and no turn end of ours is sent
// for either, so they leave no mark to dangle; neither does a subagent of a
// host not known to share its turn. A main agent's search marks its turn
// under every host, and only a Claude Code subagent marks the turn as a
// subagent's and marks its session.
func TestOnlyAClaudeCodeSubagentsSearchLeavesAMark(t *testing.T) {
	m := config.Search{TurnEnd: true, SessionsDir: "/home/sessions"}
	turn := traceHash("thread|t1")
	for name, tc := range map[string]struct {
		trace            *traceEnvelope
		turn, sub, owner bool
	}{
		"a Codex subagent":         {&traceEnvelope{Harness: "codex", SessionID: traceHash("agent"), ParentSessionID: traceHash("thread"), TurnID: turn}, false, false, false},
		"a Codex main agent":       {&traceEnvelope{Harness: "codex", SessionID: traceHash("thread"), TurnID: turn}, true, false, false},
		"an opencode subagent":     {&traceEnvelope{Harness: "opencode", SessionID: traceHash("ses_child"), ParentSessionID: traceHash("thread"), TurnID: turn}, false, false, false},
		"an opencode main agent":   {&traceEnvelope{Harness: "opencode", SessionID: traceHash("thread"), TurnID: turn}, true, false, false},
		"another host's subagent":  {&traceEnvelope{Harness: "pi", SessionID: traceHash("thread|a"), ParentSessionID: traceHash("thread"), TurnID: turn}, false, false, false},
		"a Claude Code subagent":   {&traceEnvelope{Harness: "claude-code", SessionID: traceHash("thread|a"), ParentSessionID: traceHash("thread"), TurnID: turn}, true, true, true},
		"a Claude Code main agent": {&traceEnvelope{Harness: "claude-code", SessionID: traceHash("thread"), TurnID: turn}, true, false, false},
	} {
		fs, ops := newFakeHookOps(nil)
		markTurnSearched(ops, m, tc.trace)
		_, marked := fs.files[turnSearchedPath(m.SessionsDir, turn)]
		_, sub := fs.files[turnSubagentPath(m.SessionsDir, turn)]
		_, owner := fs.files[sessionSearchedPath(m.SessionsDir, traceHash("thread"))]
		want := 0
		for _, b := range []bool{tc.turn, tc.sub, tc.owner} {
			if b {
				want++
			}
		}
		if marked != tc.turn || sub != tc.sub || owner != tc.owner || len(fs.files) != want {
			t.Errorf("%s: turn marked %v, as a subagent's %v, session marked %v (%d files); want %v, %v and %v",
				name, marked, sub, owner, len(fs.files), tc.turn, tc.sub, tc.owner)
		}
	}
}

// A turn whose own Stop never came — interrupted — keeps the mark its main
// agent's search left. A later Stop that looks at earlier turns because a
// subagent of the session searched reports only the turn that subagent
// searched in, and leaves the interrupted turn's mark for the sweep: that
// turn did not complete, and reporting it late would say it had, with an
// answer it may never have given.
func TestAnInterruptedTurnsOwnMarkIsNotReportedLate(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("sess|p2")) // p2's main agent searched; p2 was interrupted, and no Stop came
	h.subagentServed("p3")
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, false))
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p4",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "The subagent found the release date."}))
	if rec := h.record(t); rec.TurnID != traceHash("sess|p3") {
		t.Errorf("reported turn %s, want p3's alone", rec.TurnID)
	}
	if _, ok := h.fs.files[turnSearchedPath(h.hc.sessionsDir, traceHash("sess|p2"))]; !ok {
		t.Error("the interrupted turn's mark was taken by a Stop that was not its own")
	}
}

// A foreground subagent runs inside the turn, so its search is the turn's
// own and the turn's Stop takes it, with every mark it left: the turn's, the
// subagent's on the turn, and the session's. Only the ended mark stays.
func TestATurnsStopTakesEveryMarkItsSubagentLeft(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.subagentServed("p3")
	h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(false, false))
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p3",
		"transcript_path": "/t/sess.jsonl", "last_assistant_message": "I started a subagent on it."}))
	if rec := h.record(t); rec.TurnID != traceHash("sess|p3") {
		t.Fatalf("reported turn %s, want p3's", rec.TurnID)
	}
	for p := range h.fs.files {
		if strings.HasSuffix(p, turnSearchedSuffix) && p != turnEndedPath(h.hc.sessionsDir, traceHash("sess|p3")) {
			t.Errorf("p3's Stop left %s", p)
		}
	}
}

// What an opencode session leaves once its turn has ended, when a subagent
// it started searched as well as the session itself: the turn's ended mark,
// and nothing that waits for a turn end that will not come.
func TestAnOpencodeTurnWithASubagentsSearchLeavesOnlyItsEndedMark(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	markTurnSearched(h.ops, config.Search{TurnEnd: true, SessionsDir: h.hc.sessionsDir}, &traceEnvelope{
		Harness: "opencode", SessionID: traceHash("ses_child"), ParentSessionID: traceHash("ses_1"),
		TurnID: traceHash("ses_child|msg_c1"),
	})
	h.served(traceHash("ses_1|msg_u1"))
	runHook(t, h.ops, h.hc, "turn opencode", map[string]any{"session": "ses_1", "turn": "msg_u1", "status": "completed", "final_text": "done"})
	if rec := h.record(t); rec.TurnID != traceHash("ses_1|msg_u1") {
		t.Fatalf("reported turn %s, want the session's own", rec.TurnID)
	}
	for p := range h.fs.files {
		if strings.HasSuffix(p, turnSearchedSuffix) && p != turnEndedPath(h.hc.sessionsDir, traceHash("ses_1|msg_u1")) {
			t.Errorf("left %s, a mark no turn end will take", p)
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
	runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p3",
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
	markTurnSearched(ops, config.Search{TurnEnd: true, SessionsDir: dir}, &traceEnvelope{TurnID: turn})
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

// A mark's name is built from the ids a trace carries, and a trace is
// written by whatever ran the search, so only an id of traceHash's shape
// names a mark. A turn id of "ended|<turn>" would otherwise write that
// turn's ended mark, and the turn's own served search would then mark
// nothing and its end would never be sent.
func TestAnIDThatIsNotAHashNamesNoMark(t *testing.T) {
	victim := traceHash("sess|p9")
	for name, trace := range map[string]*traceEnvelope{
		"a turn id naming another turn's ended mark": {Harness: "claude-code", TurnID: "ended|" + victim},
		"a turn id naming a session's mark":          {Harness: "claude-code", TurnID: "subagent|" + traceHash("sess")},
		"a turn id that is no hash":                  {Harness: "claude-code", TurnID: "turn-1"},
		"a turn id in capitals":                      {Harness: "claude-code", TurnID: strings.ToUpper(victim)},
		"a parent session that is no hash":           {Harness: "claude-code", SessionID: traceHash("sess|a"), ParentSessionID: "ended|" + victim, TurnID: victim},
	} {
		h := newTurnEndHarness(nil, true)
		markTurnSearched(h.ops, config.Search{TurnEnd: true, SessionsDir: h.hc.sessionsDir}, trace)
		if len(h.fs.files) != 0 {
			t.Errorf("%s: wrote %d marks, want none", name, len(h.fs.files))
		}
		h.served(victim)
		runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p9"}))
		if len(h.queued) != 1 {
			t.Errorf("%s: p9's own served search was reported %d times, want once", name, len(h.queued))
		}
	}
}

// The takes hold an id to the same shape. Every caller hashes its id, so
// this is the guard on a future caller that does not: taking "ended|<turn>"
// would read that turn's ended mark as a mark of its own and remove it,
// and the turn could then be marked, and reported, again.
func TestATakeOfAnIDThatIsNotAHashTouchesNothing(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	dir, victim := "/home/sessions", traceHash("sess|p9")
	fs.files[turnEndedPath(dir, victim)] = nil
	fs.files[sessionSearchedPath(dir, "sess")] = nil // what a check-free mark of an unhashed session would leave
	if takeTurnSearched(ops, dir, "ended|"+victim) {
		t.Error(`a take of "ended|<turn>" took that turn's ended mark for a mark`)
	}
	if takeSessionSearched(ops, dir, "sess") {
		t.Error("a take of a session id that is no hash found a mark")
	}
	if len(fs.files) != 2 {
		t.Errorf("the takes left %d of the 2 marks planted", len(fs.files))
	}
}

// A Stop reads a turn out of the transcript — scrubs every message in it —
// only to report it: its own turn when it searched, and an earlier turn only
// when a subagent's search marked it. A Stop with no subagent's mark in the
// session does what a Stop did before earlier turns were looked for: it
// reads its own turn and looks at no earlier one, though one holds a mark.
func TestAStopReadsOnlyTheTurnsItReports(t *testing.T) {
	for name, tc := range map[string]struct {
		own  bool     // p4's own search was served
		sub  string   // the turn a subagent's search marked, or ""
		main []string // earlier turns a main agent's search marked
		want []string // the turns read, in order
	}{
		"its own turn searched, and no subagent": {own: true, main: []string{"p2", "p3"}, want: []string{"p4"}},
		"a subagent searched in an earlier turn": {sub: "p3", main: []string{"p2"}, want: []string{"p3"}},
		"both":                                   {own: true, sub: "p3", main: []string{"p2"}, want: []string{"p3", "p4"}},
	} {
		t.Run(name, func(t *testing.T) {
			var read []string
			claudeTurnRead = func(id string) { read = append(read, id) }
			t.Cleanup(func() { claudeTurnRead = nil })
			h := newTurnEndHarness(nil, true)
			h.fs.files["/t/sess.jsonl"] = []byte(backgroundTurns(true, true))
			for _, p := range tc.main {
				h.served(traceHash("sess|" + p))
			}
			if tc.sub != "" {
				h.subagentServed(tc.sub)
			}
			if tc.own {
				h.served(traceHash("sess|p4"))
			}
			var looked []string
			readFile := h.ops.readFile
			h.ops.readFile = func(p string) ([]byte, error) {
				for _, id := range []string{"p2", "p3"} {
					if p == turnSubagentPath(h.hc.sessionsDir, traceHash("sess|"+id)) {
						looked = append(looked, id)
					}
				}
				return readFile(p)
			}
			runHook(t, h.ops, h.hc, "stop", claudeStop(map[string]any{"prompt_id": "p4",
				"transcript_path": "/t/sess.jsonl", "last_assistant_message": "The subagent found the release date."}))
			if !reflect.DeepEqual(read, tc.want) {
				t.Errorf("read turns %v, want %v", read, tc.want)
			}
			if len(h.queued) != len(tc.want) {
				t.Errorf("queued %d turn ends, want %d", len(h.queued), len(tc.want))
			}
			if tc.sub == "" && len(looked) != 0 {
				t.Errorf("with no subagent's search in the session, the Stop looked for earlier turns %v", looked)
			}
		})
	}
}
