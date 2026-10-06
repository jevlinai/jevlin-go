package main

// The turn's conversation for hosts other than Claude Code.
//
// Claude Code hands its Stop hook a transcript, and turn_detail.go reads the
// turn out of it. No other host does. Each of these gathers the same record
// from what that host does offer, under the same rules (turn_detail.go, and
// hard invariant 2): only for a turn in which a search was served, scrubbed
// and capped before anything is written, and of a tool call its name, time
// and outcome — never its input or output.
//
//   - Cursor's hooks each carry a piece: the prompt arrives at
//     beforeSubmitPrompt, the reply at afterAgentResponse, the model and
//     token counts at stop. They are kept in the conversation's lineage file
//     until stop, each stamped with the turn it belongs to. Cursor's hooks
//     carry no text written between tool calls, and this client installs no
//     hook behind every tool, so a Cursor turn's steps are its searches.
//   - A JavaScript host (opencode) holds the whole session in its own
//     runtime. Its plugin assembles the turn and pipes it to `jevlin hook
//     turn <host>` on stdin. The text is never an argument and never a file
//     of the plugin's; the scrub, the caps, the served-search check and the
//     queue are this binary's, in one place, exactly as for Claude Code.

import (
	"encoding/json"
)

// hostTurn is what a JavaScript host pipes to `hook turn`: the host's own
// ids, raw, and the turn as the host saw it. Everything in it is treated as
// untrusted input from another program: bounded, scrubbed and rebuilt field
// by field. A tool step is read for three things and nothing else, so a
// plugin that sent more would send it nowhere.
type hostTurn struct {
	Session   string `json:"session"`
	Turn      string `json:"turn"`
	Status    string `json:"status"`
	UserText  string `json:"user_text"`
	FinalText string `json:"final_text"`
	Model     string `json:"model"`
	Usage     *struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
	Steps []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
		Name string `json:"name"`
		Call string `json:"call"`
		Ms   int64  `json:"ms"`
		OK   *bool  `json:"ok"`
	} `json:"steps"`
}

// hostTurnMaxBytes bounds what `hook turn` reads from a plugin.
const hostTurnMaxBytes = 2 << 20

// hookHostTurn turns a JavaScript host's account of a turn into a queued
// turn end. harness names the host; it is the installer's word, from the
// hook command line, not the plugin's.
func hookHostTurn(ops hookOps, hc hookContext, harness string, payload []byte) {
	if !turnEndEnabled(ops, hc) || len(payload) > hostTurnMaxBytes {
		return
	}
	var in hostTurn
	if err := json.Unmarshal(payload, &in); err != nil || in.Session == "" || in.Turn == "" {
		return
	}
	// The same derivation the host's search envelope used (opencode_plugin.js),
	// so the turn end joins the turn's searches.
	turn := traceHash(in.Session + "|" + in.Turn)
	if !takeTurnSearched(ops, hc.sessionsDir, turn) {
		return // no search of ours was served in this turn
	}
	rec := turnEndRecord{SessionID: traceHash(in.Session), TurnID: turn, Harness: harness}
	switch in.Status {
	case "", turnCompleted:
		rec.Status = turnCompleted
		if text, chars, truncated := prepareFinalText(in.FinalText); text != "" {
			rec.FinalText, rec.FinalChars, rec.Truncated = text, chars, truncated
		}
	case turnInterrupted, turnFailed:
		rec.Status = in.Status
	default:
		return
	}
	rec.UserText, rec.UserChars, rec.UserTruncated = headText(in.UserText, traceHistoryCap)
	if len(in.Model) <= 96 && toolNameRe.MatchString(in.Model) {
		rec.Model = in.Model
	}
	if u := in.Usage; u != nil && u.Input >= 0 && u.Output >= 0 && (u.Input > 0 || u.Output > 0) {
		rec.Usage = &turnUsage{InputTokens: u.Input, OutputTokens: u.Output}
	}
	budget := turnStepsText
	for _, st := range in.Steps {
		if len(rec.Steps) >= turnStepsMax {
			break
		}
		ms := st.Ms
		if ms < 0 || ms > 24*3600*1000 {
			ms = 0
		}
		switch st.Kind {
		case "assistant":
			if st.Text == "" || budget <= 0 {
				continue
			}
			text, chars, truncated := headText(st.Text, min(turnStepText, budget))
			if text == "" {
				continue
			}
			budget -= len(text)
			rec.Steps = append(rec.Steps, turnStep{Kind: "assistant", Text: text, Chars: chars, Truncated: truncated})
		case "tool":
			name := "tool"
			if toolNameRe.MatchString(st.Name) {
				name = st.Name
			}
			rec.Steps = append(rec.Steps, turnStep{Kind: "tool", Name: name, Ms: ms, OK: st.OK})
		case "search":
			s := turnStep{Kind: "search", Ms: ms, OK: st.OK}
			if st.Call != "" && len(st.Call) <= 256 {
				s.CallID = traceHash(in.Session + "|" + st.Call)
			}
			rec.Steps = append(rec.Steps, s)
		}
	}
	queueTurnEnd(ops, hc, rec)
}
