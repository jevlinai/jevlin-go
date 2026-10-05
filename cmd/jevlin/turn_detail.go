package main

// The turn's conversation, for a turn that searched.
//
// With `[miner] turn_end = true`, the turn end carries more than the final
// answer: what the user asked, what the assistant wrote along the way, which
// tools it called, and where its searches fell — read from the host's own
// transcript when the turn ends, the way a tracing plugin reads it.
//
// What is taken, and what is not:
//
//   - the user's message and the assistant's visible text, scrubbed and
//     capped before anything is written;
//   - for every tool call, its NAME, how long it took and whether it
//     succeeded. Never its input and never its output. A tool's input is a
//     file path, a shell command, a diff; its output is the file, the
//     command's output, the page. None of that is this client's to send, and
//     the record has no field it could go in;
//   - for a search of ours, the call id its trace carried, so the router can
//     put that search's own record in its place in the turn;
//   - the model and the turn's token counts;
//   - nothing the model reasoned privately (thinking blocks are skipped), and
//     nothing from a subagent's own transcript: a subagent appears as the one
//     tool call that started it.
//
// A turn whose start is not in the part of the transcript read is sent
// without its conversation — final answer only — rather than with a guess at
// where it began.

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/pkg/redact"
)

const (
	// turnDetailTail is how much of the transcript's end is read. Larger than
	// the lineage hook's tail: tool results are in the transcript too, and a
	// turn with a few file reads in it is long.
	turnDetailTail = 4 << 20
	// turnStepsMax bounds the steps of one turn; turnStepText and
	// turnStepsText bound one assistant message and all of them together.
	turnStepsMax  = 300
	turnStepText  = 16 << 10
	turnStepsText = 96 << 10
)

// turnStep is one entry of a turn end's `steps`. The router's contract
// (POST /v1/turns) names three kinds; this file produces all three.
type turnStep struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Chars     int    `json:"chars,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Name      string `json:"name,omitempty"`
	Ms        int64  `json:"ms,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}

type turnUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type turnDetail struct {
	UserText      string
	UserChars     int
	UserTruncated bool
	Model         string
	Usage         *turnUsage
	Steps         []turnStep
}

// A tool's name as it may be sent: an identifier. Anything else — a name
// with a space in it is not a name — is sent as the word "tool".
var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

// headText scrubs text and keeps its START. A question is understood from
// its beginning; an answer, which prepareTraceText keeps the end of, from
// its conclusion.
func headText(text string, limit int) (out string, chars int, truncated bool) {
	chars = utf8.RuneCountInString(text)
	if len(text) > hookTailBytes {
		return "", chars, true
	}
	out = redact.TraceText(text)
	if len(out) <= limit {
		return out, chars, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(out[end]) {
		end--
	}
	return out[:end], chars, true
}

// claudeTurnDetail reads the ending turn out of Claude Code's transcript.
// nil means the turn could not be found whole, and nothing of it is sent.
func claudeTurnDetail(ops hookOps, p claudeStopPayload) *turnDetail {
	if p.TranscriptPath == "" || ops.readTail == nil {
		return nil
	}
	tail, err := ops.readTail(p.TranscriptPath, turnDetailTail)
	if err != nil {
		return nil
	}
	type entry struct {
		Type            string          `json:"type"`
		PromptID        string          `json:"promptId"`
		Timestamp       string          `json:"timestamp"`
		IsSidechain     bool            `json:"isSidechain"`
		ToolUseResult   json.RawMessage `json:"toolUseResult"`
		SourceToolUseID string          `json:"sourceToolUseID"`
		Message         struct {
			ID      string          `json:"id"`
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *struct {
				Input       int64 `json:"input_tokens"`
				Output      int64 `json:"output_tokens"`
				CacheRead   int64 `json:"cache_read_input_tokens"`
				CacheCreate int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	type block struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
		Input     struct {
			Command string `json:"command"`
		} `json:"input"`
	}
	var entries []entry
	for _, line := range bytes.Split(tail, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue // the first line of a tail is usually a partial record
		}
		var e entry
		if json.Unmarshal(line, &e) == nil && !e.IsSidechain {
			entries = append(entries, e)
		}
	}
	blocksOf := func(e entry) []block {
		var bs []block
		if len(e.Message.Content) == 0 || e.Message.Content[0] != '[' {
			return nil
		}
		_ = json.Unmarshal(e.Message.Content, &bs)
		return bs
	}
	// The same rule the lineage hook floors its scan with (see
	// currentAssistantText): a `user` record the host wrote in response to a
	// tool call is not the start of a turn.
	isUserTurn := func(e entry) bool {
		if e.Type != "user" || len(e.ToolUseResult) > 0 || e.SourceToolUseID != "" {
			return false
		}
		if len(e.Message.Content) > 0 && e.Message.Content[0] == '"' {
			return true
		}
		for _, b := range blocksOf(e) {
			if b.Type != "tool_result" {
				return true
			}
		}
		return false
	}
	// The turn that is ending: the last user turn, and — where the transcript
	// names prompts — the one this Stop names. A transcript whose last user
	// turn belongs to another prompt is not this turn's, and is not read.
	start := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if isUserTurn(entries[i]) {
			start = i
			break
		}
	}
	if start < 0 || (entries[start].PromptID != "" && p.PromptID != "" && entries[start].PromptID != p.PromptID) {
		return nil
	}

	d := &turnDetail{}
	userRaw := ""
	if c := entries[start].Message.Content; len(c) > 0 && c[0] == '"' {
		_ = json.Unmarshal(c, &userRaw)
	} else {
		for _, b := range blocksOf(entries[start]) {
			if b.Type == "text" && b.Text != "" {
				if userRaw != "" {
					userRaw += "\n"
				}
				userRaw += b.Text
			}
		}
	}
	d.UserText, d.UserChars, d.UserTruncated = headText(userRaw, traceHistoryCap)

	when := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	type pending struct {
		step  int
		begun time.Time
	}
	open := map[string]pending{}
	seenMsg := map[string]bool{}
	usage := turnUsage{}
	textBudget := turnStepsText
	for _, e := range entries[start+1:] {
		switch {
		case e.Type == "assistant" || e.Message.Role == "assistant":
			if e.Message.Model != "" {
				d.Model = e.Message.Model
			}
			// One model reply is written as several records, one per block,
			// each repeating the reply's usage. Counted once.
			if u := e.Message.Usage; u != nil && e.Message.ID != "" && !seenMsg[e.Message.ID] {
				seenMsg[e.Message.ID] = true
				usage.InputTokens += u.Input + u.CacheRead + u.CacheCreate
				usage.OutputTokens += u.Output
			}
			for _, b := range blocksOf(e) {
				if len(d.Steps) >= turnStepsMax {
					break
				}
				switch b.Type {
				case "text":
					if b.Text == "" || textBudget <= 0 {
						continue
					}
					text, chars, truncated := headText(b.Text, min(turnStepText, textBudget))
					if text == "" {
						continue
					}
					textBudget -= len(text)
					d.Steps = append(d.Steps, turnStep{Kind: "assistant", Text: text, Chars: chars, Truncated: truncated})
				case "tool_use":
					st := turnStep{Kind: "tool", Name: "tool"}
					// The command is looked at for one thing: whether it is our
					// search. It is not kept.
					if (b.Name == "Bash" || b.Name == "PowerShell") && isSearchCommand(b.Input.Command) {
						st = turnStep{Kind: "search", CallID: traceHash(p.SessionID + "|" + b.ID)}
					} else if toolNameRe.MatchString(b.Name) {
						st.Name = b.Name
					}
					if b.ID != "" {
						open[b.ID] = pending{step: len(d.Steps), begun: when(e.Timestamp)}
					}
					d.Steps = append(d.Steps, st)
				}
			}
		case e.Type == "user":
			for _, b := range blocksOf(e) {
				pd, ok := open[b.ToolUseID]
				if b.Type != "tool_result" || !ok {
					continue
				}
				delete(open, b.ToolUseID)
				succeeded := !b.IsError
				d.Steps[pd.step].OK = &succeeded
				if end := when(e.Timestamp); !pd.begun.IsZero() && end.After(pd.begun) {
					d.Steps[pd.step].Ms = end.Sub(pd.begun).Milliseconds()
				}
			}
		}
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		d.Usage = &usage
	}
	return d
}

// withoutClosingText drops the assistant text after the last tool call: that
// is the final answer, which the turn end already carries on its own.
func withoutClosingText(steps []turnStep) []turnStep {
	n := len(steps)
	for n > 0 && steps[n-1].Kind == "assistant" {
		n--
	}
	if n == 0 {
		return steps // no tool call at all: the text is the turn, keep it
	}
	return steps[:n]
}
