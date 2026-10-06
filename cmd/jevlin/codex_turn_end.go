package main

// Codex's turn end: the turn's conversation, read from Codex's own session
// file when its Stop hook fires, under turn_detail.go's rules and hard
// invariant 2. Codex's searches already carry their session, turn and call
// (codex_hook.go); this is the one record that says how the turn went.
//
// What a live run established (codex-cli 0.160.0 on macOS, `codex exec`, a
// turn with a shell command, our search and a subagent that searched):
//
//   - Stop fires when every record of the turn is written but one: the user's
//     message, each tool call and its result, and the final message are in
//     the file; only `task_complete` follows. Nothing here waits for it.
//   - Codex records each finished item as an `item_completed` event carrying
//     the turn's id: `UserMessage` (the prompt, without the context Codex
//     wraps around it), `AgentMessage` with a phase (`commentary` while it
//     works, `final_answer` at the end), and `CommandExecution` with the
//     command and its exit code. A CommandExecution's id is the `tool_use_id`
//     the PreToolUse payload carried, so a search here gets exactly the call
//     id its trace was sent with, never one matched by position.
//   - A shell command runs inside Codex's code-running `exec` tool, so that
//     call is represented by the commands it ran, not by the code.
//   - A subagent ends with SubagentStop, which this client does not install,
//     and its records are in its own file. The parent's file holds the calls
//     that started and awaited it, sent as the tool calls they are, and the
//     subagent's answer (`agent_message`), which is not sent.
//
// A turn whose start is not in the part of the file read is sent without
// its conversation, as Claude Code's is.

import (
	"bytes"
	"encoding/json"
	"time"
)

// codexStopPayload is what codexTurnEnd reads from Codex's Stop.
type codexStopPayload struct {
	SessionID            string  `json:"session_id"`
	TurnID               string  `json:"turn_id"`
	AgentID              string  `json:"agent_id"`
	HookEventName        string  `json:"hook_event_name"`
	StopHookActive       bool    `json:"stop_hook_active"`
	LastAssistantMessage string  `json:"last_assistant_message"`
	TranscriptPath       *string `json:"transcript_path"`
	Model                string  `json:"model"`
}

// codexShellTools are the tool calls that run shell commands. Their commands
// are recorded as CommandExecution items, which is where they are read; the
// call itself would count each command a second time.
var codexShellTools = map[string]bool{"exec": true, "exec_command": true, "shell": true, "local_shell": true, "write_stdin": true}

// codexTurnEnd reports a Codex turn that searched. Like Claude Code's, the
// final message is the payload's own `last_assistant_message`.
//
// Nothing is sent when:
//   - a stop hook is already continuing the turn (`stop_hook_active`);
//   - the payload names no session or no turn;
//   - the payload names an agent: a subagent's turn is its own, and a
//     subagent's records are not sent. Codex ends a subagent with
//     SubagentStop, which is not installed; this holds if a later Codex
//     sends Stop for one instead;
//   - no search of ours was served in this turn.
func codexTurnEnd(ops hookOps, hc hookContext, payload []byte) {
	if !turnEndEnabled(ops, hc) {
		return
	}
	var p codexStopPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if p.HookEventName != "Stop" || p.StopHookActive || p.SessionID == "" || p.TurnID == "" || p.AgentID != "" {
		return
	}
	// The same derivation the PreToolUse envelope used (codexLineage), so
	// the turn end joins the turn's searches.
	turn := traceHash(p.SessionID + "|" + p.TurnID)
	if !takeTurnSearched(ops, hc.sessionsDir, turn) {
		return
	}
	rec := turnEndRecord{
		SessionID: traceHash(p.SessionID),
		TurnID:    turn,
		Harness:   orString(ops.getenv("JEVLIN_HARNESS"), "codex"),
		Status:    turnCompleted,
	}
	if text, chars, truncated := prepareFinalText(p.LastAssistantMessage); text != "" {
		rec.FinalText, rec.FinalChars, rec.Truncated = text, chars, truncated
	}
	if p.TranscriptPath != nil && isCodexRollout(*p.TranscriptPath) {
		if d := codexTurnDetail(ops, *p.TranscriptPath, p.SessionID, p.TurnID); d != nil {
			rec.UserText, rec.UserChars, rec.UserTruncated = d.UserText, d.UserChars, d.UserTruncated
			rec.Usage = d.Usage
			rec.Steps = d.Steps
			if rec.FinalText != "" {
				rec.Steps = withoutClosingText(rec.Steps)
			}
		}
	}
	if len(p.Model) <= 96 && toolNameRe.MatchString(p.Model) {
		rec.Model = p.Model
	}
	queueTurnEnd(ops, hc, rec)
}

// codexTurnDetail reads one turn out of a Codex session file: JSON lines of
// {timestamp, type, payload}. The turn starts at the `task_started` event
// with its id and runs until the next one. nil means the start was not in
// the part read, and nothing of the conversation is sent.
func codexTurnDetail(ops hookOps, path, session, turnID string) *turnDetail {
	if ops.readTail == nil {
		return nil
	}
	tail, err := ops.readTail(path, turnDetailTail)
	if err != nil {
		return nil
	}
	type text struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type record struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type   string `json:"type"`
			TurnID string `json:"turn_id"`
			Name   string `json:"name"`
			CallID string `json:"call_id"`
			Item   struct {
				Type     string          `json:"type"`
				ID       string          `json:"id"`
				Phase    string          `json:"phase"`
				Content  []text          `json:"content"`
				Command  json.RawMessage `json:"command"`
				Status   string          `json:"status"`
				ExitCode *int            `json:"exit_code"`
			} `json:"item"`
			TurnUsage *struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"turn_token_usage"`
		} `json:"payload"`
	}
	var recs []record
	start := -1
	for _, line := range bytes.Split(tail, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue // the first line of a tail is usually a partial record
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Type == "event_msg" && r.Payload.Type == "task_started" && r.Payload.TurnID == turnID {
			start = len(recs)
		}
		recs = append(recs, r)
	}
	if start < 0 {
		return nil
	}
	joined := func(parts []text) string {
		var b bytes.Buffer
		for _, c := range parts {
			if (c.Type == "text" || c.Type == "Text") && c.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(c.Text)
			}
		}
		return b.String()
	}
	at := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	type pending struct {
		step  int
		begun time.Time
	}
	open := map[string]pending{}
	// A tool call's step by its call id, for the item Codex records when the
	// call finishes: its status is the call's outcome.
	stepOf := map[string]int{}
	d := &turnDetail{}
	userSeen := false
	budget := turnStepsText
	for _, r := range recs[start+1:] {
		p := r.Payload
		if r.Type == "event_msg" && p.Type == "task_started" {
			break // the next turn
		}
		if len(d.Steps) >= turnStepsMax {
			break
		}
		switch {
		case r.Type == "event_msg" && p.Type == "item_completed" && p.TurnID == turnID:
			it := p.Item
			switch it.Type {
			case "UserMessage":
				if !userSeen {
					userSeen = true
					d.UserText, d.UserChars, d.UserTruncated = headText(joined(it.Content), traceHistoryCap)
				}
			case "AgentMessage":
				// The final answer is the payload's own; the text written while
				// working is the turn's visible text.
				if it.Phase == "final_answer" || budget <= 0 {
					continue
				}
				out, chars, truncated := headText(joined(it.Content), min(turnStepText, budget))
				if out == "" {
					continue
				}
				budget -= len(out)
				d.Steps = append(d.Steps, turnStep{Kind: "assistant", Text: out, Chars: chars, Truncated: truncated})
			case "CollabAgentToolCall":
				// Codex records how a subagent call it already listed went, and
				// that is its outcome. A call with no such item has none.
				if i, found := stepOf[it.ID]; found && it.Status != "" {
					ok := it.Status == "completed"
					d.Steps[i].OK = &ok
				}
			case "CommandExecution":
				st := turnStep{Kind: "tool", Name: "Bash"}
				// The command is looked at for one thing: whether it is our
				// search. It is not kept.
				if isSearchCommand(codexCommandText(it.Command)) {
					st = turnStep{Kind: "search"}
					if it.ID != "" {
						st.CallID = traceHash(session + "|" + it.ID)
					}
				}
				if it.Status != "" || it.ExitCode != nil {
					ok := it.Status == "completed" && (it.ExitCode == nil || *it.ExitCode == 0)
					st.OK = &ok
				}
				// No duration: the item's times are not the command's. In the
				// run above they were 0 and 1 ms for commands whose calls took
				// 163 and 620 ms, and a wrong duration is worse than none.
				d.Steps = append(d.Steps, st)
			}
		case r.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
			if codexShellTools[p.Name] {
				continue
			}
			st := turnStep{Kind: "tool", Name: "tool"}
			if toolNameRe.MatchString(p.Name) {
				st.Name = p.Name
			}
			if p.CallID != "" {
				open[p.CallID] = pending{step: len(d.Steps), begun: at(r.Timestamp)}
				stepOf[p.CallID] = len(d.Steps)
			}
			d.Steps = append(d.Steps, st)
		case r.Type == "response_item" && (p.Type == "function_call_output" || p.Type == "custom_tool_call_output"):
			if o, found := open[p.CallID]; found {
				delete(open, p.CallID)
				if end := at(r.Timestamp); !o.begun.IsZero() && end.After(o.begun) {
					if ms := end.Sub(o.begun).Milliseconds(); ms <= 24*3600*1000 {
						d.Steps[o.step].Ms = ms
					}
				}
			}
		case r.Type == "token_usage_record" && p.TurnID == turnID && p.TurnUsage != nil:
			// A running total for the turn: the last one is the turn's.
			if u := p.TurnUsage; u.Input >= 0 && u.Output >= 0 && (u.Input > 0 || u.Output > 0) {
				d.Usage = &turnUsage{InputTokens: u.Input, OutputTokens: u.Output}
			}
		}
	}
	return d
}

// codexCommandText is the shell text of a CommandExecution's command, which
// Codex records as an argv ([shell, flag, text]) or as a string.
func codexCommandText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
		return argv[len(argv)-1]
	}
	return ""
}
