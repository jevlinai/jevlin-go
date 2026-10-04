package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// searched makes the workspace's lineage file say a search was made in the
// given turn, as the lineage hook leaves it.
func (h *turnEndHarness) searched(t *testing.T, path, session, turn string) {
	t.Helper()
	if err := updateLineage(h.ops, path, time.Now(), func(l *lineageFile) {
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
	h.searched(t, lineagePath("/home/sessions", "/work"), traceHash("sess"), turn)

	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	rec := h.record(t)
	if rec.SessionID != traceHash("sess") || rec.TurnID != turn || rec.Harness != "claude-code" || rec.Status != turnCompleted {
		t.Errorf("record ids = %+v", rec)
	}
	if rec.FinalText != "The MVP was Yang Eui-ji." || rec.FinalChars != 24 || rec.Truncated {
		t.Errorf("record text = %+v", rec)
	}
	// The file is where the sender will accept it from, and nowhere else.
	if !strings.HasPrefix(h.queued[0], "/home/sessions/") || !strings.HasSuffix(h.queued[0], turnEndSuffix) {
		t.Errorf("queued at %q", h.queued[0])
	}
}

// A subagent's search leaves the lineage file under the subagent's session
// id and the orchestrator's turn. The turn end is the orchestrator's.
func TestClaudeTurnEndAfterASubagentSearch(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.searched(t, lineagePath("/home/sessions", "/work"), traceHash("sess|agent-1"), traceHash("sess|p1"))
	runHook(t, h.ops, h.hc, "flush", claudeStop(nil))
	if rec := h.record(t); rec.SessionID != traceHash("sess") {
		t.Errorf("session = %q, want the orchestrator's", rec.SessionID)
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
		"no message":                   {true, nil, turn, map[string]any{"last_assistant_message": ""}},
		"a message too large to scrub": {true, nil, turn, map[string]any{"last_assistant_message": strings.Repeat("x", hookTailBytes+1)}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTurnEndHarness(tc.env, tc.enabled)
			if tc.searched != "" {
				h.searched(t, lineagePath("/home/sessions", "/work"), traceHash("sess"), tc.searched)
			}
			before := len(h.fs.files)
			runHook(t, h.ops, h.hc, "flush", claudeStop(tc.over))
			if len(h.queued) != 0 || len(h.fs.files) != before {
				t.Errorf("queued %v, files %d -> %d", h.queued, before, len(h.fs.files))
			}
		})
	}
}

// The scrub runs on the whole message before the cut, as it does for trace
// history, and nothing unscrubbed reaches the file.
func TestTurnEndTextIsScrubbedThenCapped(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.searched(t, lineagePath("/home/sessions", "/work"), traceHash("sess"), traceHash("sess|p1"))
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
	setup := func(role, text string) *turnEndHarness {
		h := newTurnEndHarness(nil, true)
		if err := updateLineage(h.ops, path, time.Now(), func(l *lineageFile) {
			l.Harness, l.SessionID, l.TurnID = "cursor", traceHash("conv"), turn
			l.History = []traceHistory{{Role: role, Text: text}}
		}); err != nil {
			t.Fatal(err)
		}
		return h
	}
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

	for name, run := range map[string]func() *turnEndHarness{
		"the last thing written was reasoning": func() *turnEndHarness {
			h := setup("reasoning", "thinking it over")
			runHook(t, h.ops, h.hc, "cursor stop", stop("completed", "gen1"))
			return h
		},
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
	if got.path != "POST /v1/turns" || got.auth != "Bearer sr-test" || got.contentType != "application/json" || got.body != rec {
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
