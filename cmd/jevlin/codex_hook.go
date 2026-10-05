package main

// Codex.
//
// Codex gives a shell command one thing that says whose it is: CODEX_THREAD_ID,
// the session's id, in the command's environment (codex-rs
// protocol/src/shell_environment.rs). A search reads it, so a Codex session's
// searches share one session id with no hook at all.
//
// The turn is what a hook adds. Codex runs hooks from ~/.codex/hooks.json, in
// the shape Claude Code uses, and hands each one the session id and the turn
// id. Two are installed, and no hook sits in front of a tool call:
//
//   - UserPromptSubmit writes the turn's id into a lineage file named by the
//     session. The search finds that file by CODEX_THREAD_ID and carries the
//     turn id. Nothing of the prompt is kept.
//   - Stop reports the turn's end — when the installation opted in and a
//     search was served in the turn (turn_end.go) — and starts the flush.
//     The turn's conversation is read from the session file Codex names in
//     transcript_path, under the rules of turn_detail.go: a tool call is its
//     name, its time and its outcome, never its arguments or its output.
//
// A hook prints nothing: Codex adds a UserPromptSubmit hook's output to the
// model's context, and reads a Stop hook's as a decision.
//
// Read at rust-v0.132.0: hooks/src/events/{user_prompt_submit,stop}.rs for
// the payloads, hooks/src/engine/discovery.rs for hooks.json and the trust
// each hook needs from the user before Codex runs it.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// codexThreadEnv is the variable Codex sets on every command it runs.
const codexThreadEnv = "CODEX_THREAD_ID"

type codexPayload struct {
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
}

// codexLineagePath names a Codex session's lineage file. By the session
// alone: a command's working directory is the model's choice, and the hook's
// need not be the same.
func codexLineagePath(dir, thread string) string {
	return filepath.Join(dir, traceHash("codex|thread|"+thread)+".json")
}

func codexID(s string) bool { return s != "" && len(s) <= 256 }

func hookCodex(ops hookOps, hc hookContext, event string, payload []byte) {
	var p codexPayload
	if err := json.Unmarshal(payload, &p); err != nil || !codexID(p.SessionID) {
		return
	}
	switch event {
	case "UserPromptSubmit":
		if hc.sessionsDir == "" || !codexID(p.TurnID) {
			return
		}
		turn := traceHash(p.SessionID + "|" + p.TurnID)
		_ = updateLineage(ops, codexLineagePath(hc.sessionsDir, p.SessionID), ops.now(), func(l *lineageFile) {
			l.V, l.Harness, l.SessionID = lineageVersion, "codex", traceHash(p.SessionID)
			if l.TurnID != turn {
				l.TurnID, l.TurnSearches = turn, nil
			}
		})
	case "Stop":
		// A Stop that follows a hook's own continuation is the same turn
		// still running; its end is the Stop after that.
		if !p.StopHookActive {
			codexTurnEnd(ops, hc, p)
		}
		if ops.spawnFlush != nil {
			_ = ops.spawnFlush(hc.cfgPath)
		}
	}
}

// codexTrace is the envelope of a search run by Codex: the session from the
// command's environment, the turn from the file UserPromptSubmit wrote. With
// no such file — the hooks not installed, or not yet trusted — the search
// still carries its session.
func codexTrace(ops hookOps, sessionsDir string, turnEnd bool, thread string, now time.Time) *traceEnvelope {
	env := &traceEnvelope{V: traceVersion, Harness: "codex", SessionID: traceHash(thread), CallID: traceRandomID()}
	if sessionsDir == "" {
		return env
	}
	path := codexLineagePath(sessionsDir, thread)
	l, ok := loadLineage(ops, path)
	if !ok || l.SessionID != env.SessionID {
		return env
	}
	l.Seq++
	env.Seq, env.TurnID = l.Seq, l.TurnID
	// The turn end names the turn's searches in order. Kept only when the
	// installation opted in, and only for the turn.
	if turnEnd && l.TurnID != "" && len(l.TurnSearches) < turnStepsMax {
		l.TurnSearches = append(l.TurnSearches, env.CallID)
	}
	_ = saveLineage(ops, path, l, now)
	return env
}

func codexTurnEnd(ops hookOps, hc hookContext, p codexPayload) {
	if !turnEndEnabled(ops, hc) || !codexID(p.TurnID) {
		return
	}
	turn := traceHash(p.SessionID + "|" + p.TurnID)
	if !takeTurnSearched(ops, hc.sessionsDir, turn) {
		return // no search of ours was served in this turn
	}
	rec := turnEndRecord{SessionID: traceHash(p.SessionID), TurnID: turn, Harness: "codex", Status: turnCompleted}
	if text, chars, truncated := prepareFinalText(p.LastAssistantMessage); text != "" {
		rec.FinalText, rec.FinalChars, rec.Truncated = text, chars, truncated
	}
	var calls []string
	if l, ok := loadLineage(ops, codexLineagePath(hc.sessionsDir, p.SessionID)); ok && l.TurnID == turn {
		calls = l.TurnSearches
	}
	if d := codexTurnDetail(ops, p.TranscriptPath, p.TurnID, calls); d != nil {
		rec.UserText, rec.UserChars, rec.UserTruncated = d.UserText, d.UserChars, d.UserTruncated
		rec.Model, rec.Usage = d.Model, d.Usage
		rec.Steps = d.Steps
		if rec.FinalText != "" {
			rec.Steps = withoutClosingText(rec.Steps)
		}
	} else {
		// The session file could not be read: the searches are still known.
		for _, c := range calls {
			rec.Steps = append(rec.Steps, turnStep{Kind: "search", CallID: c})
		}
	}
	queueTurnEnd(ops, hc, rec)
}

// codexTurnDetail reads one turn out of a Codex session file ("rollout"): a
// JSONL of {timestamp, type, payload}. The turn begins at the task_started
// event carrying its id and runs to the end of the file, or to the next
// task_started.
//
// calls are the call ids of the turn's searches as the searches themselves
// recorded them, in order. Codex's own id for a tool call never reaches the
// command it runs, so a search in the file is matched to its call id by
// position — and only when the file shows exactly as many searches as were
// recorded. Otherwise the search steps carry no id.
func codexTurnDetail(ops hookOps, path, turnID string, calls []string) *turnDetail {
	if path == "" || ops.readTail == nil {
		return nil
	}
	tail, err := ops.readTail(path, turnDetailTail)
	if err != nil {
		return nil
	}
	type usage struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
	}
	type record struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type    string `json:"type"`
			TurnID  string `json:"turn_id"`
			Model   string `json:"model"`
			Message string `json:"message"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Status    string `json:"status"`
			ExitCode  *int   `json:"exit_code"`
			Success   *bool  `json:"success"`
			Info      *struct {
				Total *usage `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	var recs []record
	start, whole := -1, false
	for i, line := range strings.Split(string(tail), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue // the tail may begin mid-line
		}
		if i == 0 && r.Type == "session_meta" {
			whole = true
		}
		if r.Type == "event_msg" && r.Payload.Type == "task_started" && r.Payload.TurnID == turnID {
			start = len(recs)
		}
		recs = append(recs, r)
	}
	if start < 0 {
		return nil
	}
	// Tokens are a running total for the session; the turn's are the
	// difference. With no total before the turn and no start of file in
	// view, the difference is not known and none is sent.
	var before, after *usage
	for _, r := range recs[:start] {
		if r.Type == "event_msg" && r.Payload.Type == "token_count" && r.Payload.Info != nil && r.Payload.Info.Total != nil {
			before = r.Payload.Info.Total
		}
	}
	if before == nil && whole {
		before = &usage{}
	}

	d := &turnDetail{}
	at := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	type open struct {
		step  int
		began time.Time
	}
	pending := map[string]open{}
	budget := turnStepsText
	var searches []int
	call := func(r record, search bool) {
		if len(d.Steps) >= turnStepsMax {
			return
		}
		step := turnStep{Kind: "tool", Name: "tool"}
		if search {
			step = turnStep{Kind: "search"}
			searches = append(searches, len(d.Steps))
		} else if toolNameRe.MatchString(r.Payload.Name) {
			step.Name = r.Payload.Name
		}
		if r.Payload.Status == "failed" || r.Payload.Status == "declined" {
			no := false
			step.OK = &no
		}
		if r.Payload.CallID != "" {
			pending[r.Payload.CallID] = open{len(d.Steps), at(r.Timestamp)}
		}
		d.Steps = append(d.Steps, step)
	}
	outcome := func(id string, ok bool) {
		if o, found := pending[id]; found {
			d.Steps[o.step].OK = &ok
		}
	}
walk:
	for _, r := range recs[start+1:] {
		p := r.Payload
		switch r.Type {
		case "turn_context":
			if len(p.Model) <= 96 && toolNameRe.MatchString(p.Model) {
				d.Model = p.Model
			}
		case "event_msg":
			switch p.Type {
			case "task_started":
				break walk
			case "user_message":
				if d.UserText == "" {
					d.UserText, d.UserChars, d.UserTruncated = headText(p.Message, traceHistoryCap)
				}
			case "token_count":
				if p.Info != nil && p.Info.Total != nil {
					after = p.Info.Total
				}
			case "exec_command_end":
				if p.ExitCode != nil {
					outcome(p.CallID, *p.ExitCode == 0)
				}
			case "patch_apply_end":
				if p.Success != nil {
					outcome(p.CallID, *p.Success)
				}
			}
		case "response_item":
			switch p.Type {
			case "message":
				if p.Role != "assistant" || budget <= 0 || len(d.Steps) >= turnStepsMax {
					continue
				}
				var b strings.Builder
				for _, c := range p.Content {
					if c.Type == "output_text" && c.Text != "" {
						if b.Len() > 0 {
							b.WriteString("\n")
						}
						b.WriteString(c.Text)
					}
				}
				text, chars, truncated := headText(b.String(), min(turnStepText, budget))
				if text == "" {
					continue
				}
				budget -= len(text)
				d.Steps = append(d.Steps, turnStep{Kind: "assistant", Text: text, Chars: chars, Truncated: truncated})
			case "function_call":
				call(r, codexCallIsSearch(p.Arguments))
			case "custom_tool_call", "local_shell_call":
				call(r, false)
			case "function_call_output", "custom_tool_call_output":
				if o, found := pending[p.CallID]; found && !o.began.IsZero() {
					if ms := at(r.Timestamp).Sub(o.began).Milliseconds(); ms > 0 && ms <= 24*3600*1000 {
						d.Steps[o.step].Ms = ms
					}
				}
			}
		}
	}
	if len(searches) == len(calls) {
		for i, s := range searches {
			d.Steps[s].CallID = calls[i]
		}
	}
	if before != nil && after != nil {
		if in, out := after.Input-before.Input, after.Output-before.Output; in >= 0 && out >= 0 && (in > 0 || out > 0) {
			d.Usage = &turnUsage{InputTokens: in, OutputTokens: out}
		}
	}
	return d
}

// codexCallIsSearch looks at a tool call's arguments for one thing: whether
// the command it runs is our search. Codex's shell tools carry the command
// as `cmd` (exec_command) or `command` (shell: a string, or an argv).
func codexCallIsSearch(arguments string) bool {
	var a struct {
		Cmd     string          `json:"cmd"`
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return false
	}
	if a.Cmd != "" {
		return isSearchCommand(a.Cmd)
	}
	var s string
	if json.Unmarshal(a.Command, &s) == nil {
		return isSearchCommand(s)
	}
	var argv []string
	if json.Unmarshal(a.Command, &argv) == nil && len(argv) > 0 {
		return isSearchCommand(argv[len(argv)-1]) || isSearchCommand(strings.Join(argv, " "))
	}
	return false
}
