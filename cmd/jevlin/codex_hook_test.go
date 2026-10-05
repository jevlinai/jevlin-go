package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// codexRollout is a Codex session file in the shape codex-cli 0.132.0 writes
// (record and field names taken from a real one): an earlier turn, then the
// turn under test with commentary, a file read, our search, a failed patch
// and the final answer.
func codexRollout() string {
	line := func(ts, typ string, payload map[string]any) string {
		b, _ := json.Marshal(map[string]any{"timestamp": "2026-10-05T10:00:" + ts + "Z", "type": typ, "payload": payload})
		return string(b)
	}
	args := func(v map[string]any) string { b, _ := json.Marshal(v); return string(b) }
	tokens := func(in, out int) map[string]any {
		return map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]any{"input_tokens": in, "output_tokens": out}}}
	}
	msg := func(role, kind, text string) map[string]any {
		return map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": kind, "text": text}}}
	}
	return strings.Join([]string{
		line("00.000", "session_meta", map[string]any{"id": "thread-1", "cwd": "/Users/someone/private-project"}),
		line("01.000", "event_msg", map[string]any{"type": "task_started", "turn_id": "turn-0"}),
		line("01.100", "event_msg", map[string]any{"type": "user_message", "message": "AN EARLIER QUESTION"}),
		line("01.200", "response_item", msg("assistant", "output_text", "AN EARLIER ANSWER")),
		line("01.300", "event_msg", tokens(1000, 100)),
		line("02.000", "event_msg", map[string]any{"type": "task_started", "turn_id": "turn-1"}),
		line("02.010", "response_item", msg("developer", "input_text", "DEVELOPER INSTRUCTIONS")),
		line("02.020", "response_item", msg("user", "input_text", "<environment_context>ENVIRONMENT</environment_context>")),
		line("02.030", "turn_context", map[string]any{"turn_id": "turn-1", "model": "gpt-5.5", "cwd": "/Users/someone/private-project"}),
		line("02.040", "event_msg", map[string]any{"type": "user_message", "message": "who designed the stadium? token ghp_" + strings.Repeat("a", 30)}),
		line("02.050", "response_item", map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "REASONING"}),
		line("03.000", "response_item", msg("assistant", "output_text", "Let me look.")),
		line("03.100", "response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c1", "arguments": args(map[string]any{"cmd": "cat /Users/someone/secret.txt", "workdir": "/Users/someone"})}),
		line("03.112", "response_item", map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Chunk ID: 1\nFILE CONTENTS"}),
		line("03.113", "event_msg", map[string]any{"type": "exec_command_end", "call_id": "c1", "exit_code": 0}),
		line("04.000", "response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c2", "arguments": args(map[string]any{"cmd": "'/opt/jevlin/bin/jevlin' search -format model \"stadium architect\""})}),
		line("09.200", "response_item", map[string]any{"type": "function_call_output", "call_id": "c2", "output": "SEARCH RESULTS"}),
		line("09.300", "response_item", map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "c3", "status": "completed", "input": "*** PATCH BODY"}),
		line("09.340", "event_msg", map[string]any{"type": "patch_apply_end", "call_id": "c3", "success": false, "stderr": "PATCH ERROR"}),
		line("09.350", "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "c3", "output": "PATCH OUTPUT"}),
		line("10.000", "event_msg", tokens(5000, 350)),
		line("10.100", "response_item", msg("assistant", "output_text", "Populous designed it.")),
		line("10.200", "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-1", "last_agent_message": "Populous designed it."}),
	}, "\n") + "\n"
}

// The whole Codex path: UserPromptSubmit names the turn, the search finds it
// through CODEX_THREAD_ID, Stop reports the turn from the session file.
func TestCodexTurn(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	h.ops.now = func() time.Time { return now }
	sops := searchOps{
		getppid: func() int { return 1 }, hostname: func() (string, error) { return "host", nil },
		getwd: func() (string, error) { return "/somewhere/else", nil }, now: func() time.Time { return now }, hook: h.ops,
	}
	miner := config.Miner{SessionsDir: h.hc.sessionsDir, TurnEnd: true}
	env := map[string]string{codexThreadEnv: "thread-1"}
	search := func() *traceEnvelope {
		tr, _ := searchTrace(sops, miner, func(k string) string { return env[k] })
		return tr
	}

	// Before any hook has run, a search still knows its session.
	first := search()
	if first.Harness != "codex" || first.SessionID != traceHash("thread-1") || first.TurnID != "" || first.CallID == "" {
		t.Fatalf("with no hook: %+v", first)
	}

	if out, _ := runHook(t, h.ops, h.hc, "codex UserPromptSubmit", map[string]any{
		"session_id": "thread-1", "turn_id": "turn-1", "prompt": "A PRIVATE PROMPT", "hook_event_name": "UserPromptSubmit"}); out != "" {
		t.Errorf("UserPromptSubmit printed %q; Codex would add it to the model's context", out)
	}
	for p, b := range h.fs.files {
		if strings.Contains(string(b), "PRIVATE PROMPT") || strings.Contains(string(b), "thread-1") || strings.Contains(string(b), "turn-1") {
			t.Errorf("%s holds the prompt or a raw id: %s", p, b)
		}
	}
	turn := traceHash("thread-1|turn-1")
	tr := search()
	if tr.TurnID != turn || tr.SessionID != traceHash("thread-1") || tr.Seq != 1 || tr.Harness != "codex" {
		t.Fatalf("the search's envelope: %+v", tr)
	}
	// Another session's file is not this search's.
	env[codexThreadEnv] = "thread-2"
	if other := search(); other.TurnID != "" || other.SessionID != traceHash("thread-2") {
		t.Errorf("another thread took this one's turn: %+v", other)
	}
	env[codexThreadEnv] = "thread-1"

	h.served(turn)
	h.fs.files["/codex/rollout.jsonl"] = []byte(codexRollout())
	stop := map[string]any{"session_id": "thread-1", "turn_id": "turn-1", "transcript_path": "/codex/rollout.jsonl",
		"last_assistant_message": "Populous designed it.", "stop_hook_active": false, "hook_event_name": "Stop"}
	if out, _ := runHook(t, h.ops, h.hc, "codex Stop", stop); out != "" {
		t.Errorf("Stop printed %q; Codex would read it as a decision", out)
	}
	rec := h.record(t)
	raw := string(h.fs.files[h.queued[0]])
	for _, leak := range []string{"secret.txt", "FILE CONTENTS", "SEARCH RESULTS", "stadium architect", "PATCH", "REASONING", "ENVIRONMENT", "DEVELOPER",
		"EARLIER", "private-project", "ghp_aaaa", "thread-1", "turn-1", "/opt/jevlin"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the queued record contains %q", leak)
		}
	}
	if rec.Harness != "codex" || rec.SessionID != traceHash("thread-1") || rec.TurnID != turn || rec.Status != turnCompleted ||
		rec.FinalText != "Populous designed it." || rec.Model != "gpt-5.5" ||
		!strings.HasPrefix(rec.UserText, "who designed the stadium?") || !strings.Contains(rec.UserText, "[REDACTED]") {
		t.Errorf("record = %+v", rec)
	}
	if rec.Usage == nil || rec.Usage.InputTokens != 4000 || rec.Usage.OutputTokens != 250 {
		t.Errorf("usage = %+v, want the turn's share of the running total", rec.Usage)
	}
	want := []turnStep{
		{Kind: "assistant", Text: "Let me look."},
		{Kind: "tool", Name: "exec_command", Ms: 12},
		{Kind: "search", CallID: tr.CallID, Ms: 5200},
		{Kind: "tool", Name: "apply_patch", Ms: 50},
	}
	if len(rec.Steps) != len(want) {
		t.Fatalf("steps = %+v", rec.Steps)
	}
	for i, w := range want {
		g := rec.Steps[i]
		if g.Kind != w.Kind || g.Text != w.Text || g.Name != w.Name || g.CallID != w.CallID || g.Ms != w.Ms {
			t.Errorf("step %d = %+v, want %+v", i, g, w)
		}
	}
	if rec.Steps[1].OK == nil || !*rec.Steps[1].OK || rec.Steps[2].OK != nil || rec.Steps[3].OK == nil || *rec.Steps[3].OK {
		t.Errorf("outcomes: read %v, search %v, patch %v", rec.Steps[1].OK, rec.Steps[2].OK, rec.Steps[3].OK)
	}
}

func TestCodexTurnEndIsSentOnlyWhenItShouldBe(t *testing.T) {
	turn := traceHash("thread-1|turn-1")
	stop := func(over map[string]any) map[string]any {
		p := map[string]any{"session_id": "thread-1", "turn_id": "turn-1", "transcript_path": nil, "last_assistant_message": "Done.", "stop_hook_active": false}
		for k, v := range over {
			p[k] = v
		}
		return p
	}
	for name, tc := range map[string]struct {
		enabled, served bool
		over            map[string]any
	}{
		"not opted in":          {false, true, nil},
		"no search served":      {true, false, nil},
		"a hook's continuation": {true, true, map[string]any{"stop_hook_active": true}},
		"no turn id":            {true, true, map[string]any{"turn_id": ""}},
	} {
		h := newTurnEndHarness(nil, tc.enabled)
		if tc.served {
			h.served(turn)
		}
		runHook(t, h.ops, h.hc, "codex Stop", stop(tc.over))
		if len(h.queued) != 0 {
			t.Errorf("%s: queued %v", name, h.queued)
		}
	}
	// No session file and no answer: the turn is still reported, with the
	// searches the searches themselves recorded.
	h := newTurnEndHarness(nil, true)
	runHook(t, h.ops, h.hc, "codex UserPromptSubmit", map[string]any{"session_id": "thread-1", "turn_id": "turn-1"})
	if err := updateLineage(h.ops, codexLineagePath(h.hc.sessionsDir, "thread-1"), time.Now(), func(l *lineageFile) { l.TurnSearches = []string{"call-a"} }); err != nil {
		t.Fatal(err)
	}
	h.served(turn)
	runHook(t, h.ops, h.hc, "codex Stop", stop(map[string]any{"last_assistant_message": nil}))
	rec := h.record(t)
	if rec.Status != turnCompleted || rec.FinalText != "" || len(rec.Steps) != 1 || rec.Steps[0].CallID != "call-a" {
		t.Errorf("record = %+v", rec)
	}
	// A new turn starts with no searches of the last one.
	runHook(t, h.ops, h.hc, "codex UserPromptSubmit", map[string]any{"session_id": "thread-1", "turn_id": "turn-2"})
	if l, _ := loadLineage(h.ops, codexLineagePath(h.hc.sessionsDir, "thread-1")); l == nil || len(l.TurnSearches) != 0 || l.TurnID != traceHash("thread-1|turn-2") {
		t.Errorf("after the next prompt: %+v", l)
	}
}

// A search whose count in the session file does not match what the searches
// recorded is not given a call id by guesswork; and a search another host
// already claimed is not Codex's.
func TestCodexSearchMatching(t *testing.T) {
	fsys, ops := newFakeHookOps(nil)
	fsys.files["/r.jsonl"] = []byte(codexRollout())
	d := codexTurnDetail(ops, "/r.jsonl", "turn-1", []string{"a", "b"})
	if d == nil {
		t.Fatal("no detail")
	}
	for _, s := range d.Steps {
		if s.Kind == "search" && s.CallID != "" {
			t.Errorf("a call id was guessed: %+v", s)
		}
	}
	if codexTurnDetail(ops, "/r.jsonl", "turn-9", nil) != nil {
		t.Error("a turn that is not in the file was read")
	}
	for args, want := range map[string]bool{
		`{"cmd":"jevlin search x"}`:                     true,
		`{"command":"/opt/bin/jevlin search x"}`:        true,
		`{"command":["bash","-lc","jevlin search x"]}`:  true,
		`{"cmd":"grep -r search ."}`:                    false,
		`{"plan":[{"step":"run jevlin search later"}]}`: false,
		`not json`: false,
	} {
		if got := codexCallIsSearch(args); got != want {
			t.Errorf("codexCallIsSearch(%s) = %v", args, got)
		}
	}

	now := time.Now()
	sops := searchOps{getppid: func() int { return 1 }, hostname: func() (string, error) { return "h", nil },
		getwd: func() (string, error) { return "/w", nil }, now: func() time.Time { return now }, hook: ops}
	env := map[string]string{codexThreadEnv: "thread-1", "JEVLIN_HARNESS": "cursor"}
	if tr, _ := searchTrace(sops, config.Miner{SessionsDir: "/home/sessions"}, func(k string) string { return env[k] }); tr.Harness == "codex" {
		t.Errorf("a search declaring another harness was taken for Codex's: %+v", tr)
	}
}

// The reader against session files Codex itself wrote, for whoever has some:
// set JEVLIN_CODEX_SESSIONS to a Codex sessions directory (~/.codex/sessions).
// Every turn it finds must come out well formed. Nothing of them is printed.
func TestCodexTurnDetailOnRealSessions(t *testing.T) {
	dir := os.Getenv("JEVLIN_CODEX_SESSIONS")
	if dir == "" {
		t.Skip("JEVLIN_CODEX_SESSIONS is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*.jsonl"))
	if len(files) == 0 {
		t.Fatalf("no session files under %s", dir)
	}
	sort.Strings(files)
	ops := hookOps{readTail: func(p string, max int64) ([]byte, error) {
		b, err := os.ReadFile(p) // #nosec G304 -- the developer's own Codex session files
		if err == nil && int64(len(b)) > max {
			b = b[int64(len(b))-max:]
		}
		return b, err
	}}
	turns, withUser, withSteps := 0, 0, 0
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- as above
		if err != nil {
			continue
		}
		var ids []string
		for _, line := range strings.Split(string(b), "\n") {
			var r struct {
				Type    string `json:"type"`
				Payload struct {
					Type   string `json:"type"`
					TurnID string `json:"turn_id"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &r) == nil && r.Type == "event_msg" && r.Payload.Type == "task_started" && r.Payload.TurnID != "" {
				ids = append(ids, r.Payload.TurnID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		d := codexTurnDetail(ops, f, ids[len(ids)-1], nil)
		if d == nil {
			continue // the turn began before the tail that is read
		}
		turns++
		if d.UserText != "" {
			withUser++
		}
		if len(d.Steps) > 0 {
			withSteps++
		}
		for _, s := range d.Steps {
			switch s.Kind {
			case "assistant":
				if s.Text == "" {
					t.Errorf("%s: an empty assistant step", filepath.Base(f))
				}
			case "tool":
				if !toolNameRe.MatchString(s.Name) || s.Text != "" {
					t.Errorf("%s: tool step %+v", filepath.Base(f), s)
				}
			case "search":
			default:
				t.Errorf("%s: step kind %q", filepath.Base(f), s.Kind)
			}
		}
	}
	t.Logf("%d real Codex turns read: %d with the user's message, %d with steps", turns, withUser, withSteps)
	if turns > 0 && (withUser == 0 || withSteps == 0) {
		t.Errorf("real session files were read but nothing was found in them: %d turns, %d with a user message, %d with steps", turns, withUser, withSteps)
	}
}
