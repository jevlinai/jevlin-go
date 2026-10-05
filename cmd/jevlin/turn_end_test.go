package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// turnEndHarness is a fake hook environment that records what was queued.
type turnEndHarness struct {
	fs     *fakeHookFS
	ops    hookOps
	hc     hookContext
	queued []string // files handed to the detached sender
}

func newTurnEndHarness(env map[string]string, enabled bool) *turnEndHarness {
	h := &turnEndHarness{}
	h.fs, h.ops = newFakeHookOps(env)
	h.ops.spawnTurnEnd = func(_, file string) error {
		h.queued = append(h.queued, file)
		return nil
	}
	h.hc = hookContext{sessionsDir: "/home/sessions", turnEnd: enabled}
	return h
}

func (h *turnEndHarness) record(t *testing.T) turnEndRecord {
	t.Helper()
	if len(h.queued) != 1 {
		t.Fatalf("queued %d turn ends, want 1", len(h.queued))
	}
	var rec turnEndRecord
	if err := json.Unmarshal(h.fs.files[h.queued[0]], &rec); err != nil {
		t.Fatalf("queued file: %v", err)
	}
	return rec
}

// served leaves the mark `jevlin search` leaves after the router answers a
// search made in the given turn.
func (h *turnEndHarness) served(turn string) {
	markTurnSearched(h.ops, config.Miner{TurnEnd: true, SessionsDir: h.hc.sessionsDir}, &traceEnvelope{TurnID: turn})
}

// proposed leaves what the lineage hook leaves BEFORE a search runs: the
// workspace's lineage file stamped with the turn. It is not evidence that a
// search happened.
func (h *turnEndHarness) proposed(t *testing.T, session, turn string) {
	t.Helper()
	if err := updateLineage(h.ops, lineagePath(h.hc.sessionsDir, "/work"), time.Now(), func(l *lineageFile) {
		l.SessionID, l.TurnID = session, turn
	}); err != nil {
		t.Fatal(err)
	}
}

func claudeStop(over map[string]any) map[string]any {
	p := map[string]any{
		"hook_event_name": "Stop", "session_id": "sess", "prompt_id": "p1", "cwd": "/work",
		"stop_hook_active": false, "last_assistant_message": "The MVP was Yang Eui-ji.",
	}
	for k, v := range over {
		p[k] = v
	}
	return p
}

func TestClaudeTurnEndIsQueuedForATurnThatSearched(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	turn := traceHash("sess|p1")
	h.served(turn)

	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	rec := h.record(t)
	if rec.SessionID != traceHash("sess") || rec.TurnID != turn || rec.Harness != "claude-code" || rec.Status != turnCompleted {
		t.Errorf("record ids = %+v", rec)
	}
	if rec.FinalText != "The MVP was Yang Eui-ji." || rec.FinalChars != 24 || rec.Truncated {
		t.Errorf("record text = %+v", rec)
	}
	// The file is where the sender will accept it from, and nowhere else.
	if filepath.Dir(h.queued[0]) != filepath.Clean(h.hc.sessionsDir) || !strings.HasSuffix(h.queued[0], turnEndSuffix) {
		t.Errorf("queued at %q", h.queued[0])
	}
	// The mark is taken: the same turn ending again reports nothing.
	h.queued = nil
	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	if len(h.queued) != 0 {
		t.Errorf("a turn ended twice: %v", h.queued)
	}
}

// A subagent's search carries the orchestrator's turn id, so it marks the
// orchestrator's turn; the turn end is reported under the orchestrator's
// session.
func TestClaudeTurnEndAfterASubagentSearch(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("sess|p1"))
	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	if rec := h.record(t); rec.SessionID != traceHash("sess") {
		t.Errorf("session = %q, want the orchestrator's", rec.SessionID)
	}
}

// The lineage hook stamps a turn when a search is PROPOSED, before the
// permission decision and before the command runs. A search the user refused,
// or one that failed, leaves that stamp and no mark, and the turn's answer
// stays on the machine.
func TestClaudeTurnEndNeedsAServedSearch(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.proposed(t, traceHash("sess"), traceHash("sess|p1"))
	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	if len(h.queued) != 0 {
		t.Errorf("a proposed search was taken for a served one: %v", h.queued)
	}
}

// `search` marks a turn only when the installation opted in and the search
// carried a turn id.
func TestMarkTurnSearched(t *testing.T) {
	turn := traceHash("sess|p1")
	for name, tc := range map[string]struct {
		m     config.Miner
		trace *traceEnvelope
		want  bool
	}{
		"opted in":        {config.Miner{TurnEnd: true, SessionsDir: "/home/sessions"}, &traceEnvelope{TurnID: turn}, true},
		"not opted in":    {config.Miner{SessionsDir: "/home/sessions"}, &traceEnvelope{TurnID: turn}, false},
		"no sessions dir": {config.Miner{TurnEnd: true}, &traceEnvelope{TurnID: turn}, false},
		"no turn id":      {config.Miner{TurnEnd: true, SessionsDir: "/home/sessions"}, &traceEnvelope{SessionID: "s"}, false},
		"no trace":        {config.Miner{TurnEnd: true, SessionsDir: "/home/sessions"}, nil, false},
	} {
		fs, ops := newFakeHookOps(nil)
		markTurnSearched(ops, tc.m, tc.trace)
		if got := len(fs.files) == 1; got != tc.want {
			t.Errorf("%s: marked=%v, want %v (%d files)", name, got, tc.want, len(fs.files))
		}
		if tc.want && !takeTurnSearched(ops, "/home/sessions", turn) {
			t.Errorf("%s: the mark was not found by the turn that left it", name)
		}
		if tc.want && (takeTurnSearched(ops, "/home/sessions", turn) || len(fs.files) != 0) {
			t.Errorf("%s: the mark outlived being taken", name)
		}
	}
}

func TestClaudeTurnEndSendsNothing(t *testing.T) {
	turn := traceHash("sess|p1")
	for name, tc := range map[string]struct {
		enabled  bool
		env      map[string]string
		searched string // the turn the lineage file holds; "" for no file
		over     map[string]any
	}{
		"not opted in":                 {false, nil, turn, nil},
		"tracing switched off":         {true, map[string]string{"JEVLIN_TRACE": "off"}, turn, nil},
		"no search in this turn":       {true, nil, traceHash("sess|p0"), nil},
		"no search at all":             {true, nil, "", nil},
		"a stop hook is continuing it": {true, nil, turn, map[string]any{"stop_hook_active": true}},
		"no prompt id":                 {true, nil, turn, map[string]any{"prompt_id": ""}},
		"another event":                {true, nil, turn, map[string]any{"hook_event_name": "SubagentStop"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTurnEndHarness(tc.env, tc.enabled)
			if tc.searched != "" {
				h.served(tc.searched)
			}
			runHook(t, h.ops, h.hc, "flush", claudeStop(tc.over))
			// Nothing handed to the sender, and no record written for it.
			for path := range h.fs.files {
				if strings.HasSuffix(path, turnEndSuffix) {
					t.Errorf("a record was written: %s", path)
				}
			}
			if len(h.queued) != 0 {
				t.Errorf("queued %v", h.queued)
			}
		})
	}
}

// A turn that searched and ends with nothing to send is still reported:
// completed, without text.
func TestClaudeTurnEndWithoutAMessageIsStatusOnly(t *testing.T) {
	for name, msg := range map[string]string{
		"no message":                   "",
		"a message too large to scrub": strings.Repeat("x", hookTailBytes+1),
	} {
		h := newTurnEndHarness(nil, true)
		h.served(traceHash("sess|p1"))
		runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"last_assistant_message": msg}))
		if rec := h.record(t); rec.Status != turnCompleted || rec.FinalText != "" || rec.FinalChars != 0 || rec.Truncated {
			t.Errorf("%s: %+v", name, rec)
		}
	}
}

// The scrub runs on the whole message before the cut, as it does for trace
// history, and nothing unscrubbed reaches the file.
func TestTurnEndTextIsScrubbedThenCapped(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("sess|p1"))
	secret := "sk-" + strings.Repeat("A", 40)
	long := strings.Repeat("word ", traceHistoryCap/5+100) + "the key is " + secret + " and that is the end"
	runHook(t, h.ops, h.hc, "flush", claudeStop(map[string]any{"last_assistant_message": long}))
	rec := h.record(t)
	if strings.Contains(string(h.fs.files[h.queued[0]]), secret) || !strings.Contains(rec.FinalText, "[REDACTED]") {
		t.Error("the secret reached the queued file")
	}
	if len(rec.FinalText) > traceHistoryCap || !rec.Truncated || !strings.HasSuffix(rec.FinalText, "that is the end") {
		t.Errorf("cap: %d bytes, truncated=%v, tail=%q", len(rec.FinalText), rec.Truncated, rec.FinalText[len(rec.FinalText)-20:])
	}
	if rec.FinalChars != len(long) {
		t.Errorf("final_chars = %d, want the original %d", rec.FinalChars, len(long))
	}
}

func TestCursorTurnEnd(t *testing.T) {
	path := conversationLineagePath("/home/sessions", "/work", "conv")
	turn := traceHash("conv|gen1")
	// What Cursor's hooks leave by the time `stop` fires in a turn that
	// searched: the reply, stamped with its turn by afterAgentResponse, and
	// the mark `search` left.
	setupFor := func(role, text, answerTurn string) *turnEndHarness {
		h := newTurnEndHarness(nil, true)
		if err := updateLineage(h.ops, path, time.Now(), func(l *lineageFile) {
			l.Harness, l.SessionID, l.TurnID = "cursor", traceHash("conv"), turn
			l.History = []traceHistory{{Role: role, Text: text}}
			l.AnswerTurnID = answerTurn
		}); err != nil {
			t.Fatal(err)
		}
		h.served(turn)
		return h
	}
	setup := func(role, text string) *turnEndHarness { return setupFor(role, text, turn) }
	stop := func(status, gen string) map[string]any {
		return map[string]any{"conversation_id": "conv", "generation_id": gen, "cwd": "/work", "status": status}
	}

	h := setup("assistant", "Changwon NC Park seats about 22,000.")
	runHook(t, h.ops, h.hc, "cursor stop", stop("completed", "gen1"))
	if rec := h.record(t); rec.SessionID != traceHash("conv") || rec.TurnID != turn || rec.Harness != "cursor" ||
		rec.Status != turnCompleted || rec.FinalText != "Changwon NC Park seats about 22,000." {
		t.Errorf("completed = %+v", rec)
	}

	// Aborted and failed turns are reported, without text.
	for status, want := range map[string]string{"aborted": turnInterrupted, "error": turnFailed} {
		h := setup("assistant", "half an answ")
		runHook(t, h.ops, h.hc, "cursor stop", stop(status, "gen1"))
		if rec := h.record(t); rec.Status != want || rec.FinalText != "" {
			t.Errorf("%s = %+v", status, rec)
		}
	}

	// A completed turn whose reply is not this turn's to send — reasoning was
	// written last, the saved reply is another turn's, or it names no turn —
	// is reported as completed, without text. Never with the wrong text.
	for name, h := range map[string]*turnEndHarness{
		"the last thing written was reasoning": setup("reasoning", "thinking it over"),
		"the saved reply is another turn's":    setupFor("assistant", "the previous turn's answer", traceHash("conv|gen0")),
		"the saved reply names no turn":        setupFor("assistant", "an answer from before the stamp existed", ""),
	} {
		runHook(t, h.ops, h.hc, "cursor stop", stop("completed", "gen1"))
		if rec := h.record(t); rec.Status != turnCompleted || rec.FinalText != "" {
			t.Errorf("%s: %+v", name, rec)
		}
	}

	for name, run := range map[string]func() *turnEndHarness{
		"no search in this turn": func() *turnEndHarness {
			h := setup("assistant", "an answer to something else")
			runHook(t, h.ops, h.hc, "cursor stop", stop("completed", "gen2"))
			return h
		},
		"not opted in": func() *turnEndHarness {
			h := setup("assistant", "an answer")
			h.hc.turnEnd = false
			runHook(t, h.ops, h.hc, "cursor stop", stop("completed", "gen1"))
			return h
		},
		"session end is not a turn end": func() *turnEndHarness {
			h := setup("assistant", "an answer")
			runHook(t, h.ops, h.hc, "cursor sessionEnd", stop("completed", "gen1"))
			return h
		},
	} {
		if h := run(); len(h.queued) != 0 {
			t.Errorf("%s: queued %v", name, h.queued)
		}
	}
}

// afterAgentResponse stamps the reply with the turn it was written in, and a
// later thought clears the stamp with the reply.
func TestCursorReplyIsStampedWithItsTurn(t *testing.T) {
	_, ops := newFakeHookOps(nil)
	hc := hookContext{sessionsDir: "/home/sessions"}
	path := conversationLineagePath("/home/sessions", "/work", "conv")
	event := func(name, gen, text string) *lineageFile {
		t.Helper()
		runHook(t, ops, hc, "cursor "+name, map[string]any{"conversation_id": "conv", "generation_id": gen, "cwd": "/work", "text": text})
		l, ok := loadLineage(ops, path)
		if !ok {
			t.Fatal("no lineage file")
		}
		return l
	}
	if l := event("afterAgentResponse", "gen1", "the answer"); l.AnswerTurnID != traceHash("conv|gen1") {
		t.Errorf("reply stamp = %q", l.AnswerTurnID)
	}
	if l := event("afterAgentThought", "gen2", "thinking"); l.AnswerTurnID != "" {
		t.Errorf("a thought kept the reply's stamp: %q", l.AnswerTurnID)
	}
	if l := event("afterAgentResponse", "", "an answer with no generation"); l.AnswerTurnID != "" {
		t.Errorf("a reply with no generation was stamped: %q", l.AnswerTurnID)
	}
}

func TestSendTurnEnd(t *testing.T) {
	var got struct {
		path, auth, contentType string
		body                    turnEndRecord
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth, got.contentType = r.Method+" "+r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	rec := turnEndRecord{SessionID: "s", TurnID: "t", Harness: "claude-code", Status: turnCompleted, FinalText: "done", FinalChars: 4}
	if code := sendTurnEnd(srv.Client(), srv.URL, "sr-test", rec); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	if got.path != "POST /v1/turns" || got.auth != "Bearer sr-test" || got.contentType != "application/json" || !reflect.DeepEqual(got.body, rec) {
		t.Errorf("request = %+v", got)
	}

	// A router that predates the endpoint, and one that is gone: both are
	// final, and neither is an error anyone handles.
	old := httptest.NewServer(http.NotFoundHandler())
	if code := sendTurnEnd(old.Client(), old.URL, "sr-test", rec); code != http.StatusNotFound {
		t.Errorf("old router: %d", code)
	}
	old.Close()
	if code := sendTurnEnd(old.Client(), old.URL, "sr-test", rec); code != 0 {
		t.Errorf("unreachable router: %d", code)
	}
}
