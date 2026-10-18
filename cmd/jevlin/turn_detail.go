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
	"strings"
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
	// lastText is the turn's last assistant text, unscrubbed: the final
	// message of an earlier turn, whose own Stop has long gone, scrubbed by
	// prepareFinalText as the payload's would be.
	lastText string
}

// claudeTurn is one turn of a Claude Code transcript: its prompt id, and
// how to read its conversation out of the part of the transcript read.
// Reading is the cost — every message in the turn is scrubbed — so it is
// done only for a turn that is reported.
type claudeTurn struct {
	PromptID string
	read     func() *turnDetail
}

// detail reads the turn. nil means the turn is not in the transcript whole,
// and nothing of it is sent.
func (t claudeTurn) detail() *turnDetail {
	if t.read == nil {
		return nil
	}
	return t.read()
}

// claudeTurnRead, when set, is told the prompt id of every turn read out of
// a transcript, as the read begins. A test seam: a turn read that nothing
// reports is the cost to rule out, and nothing else observes it.
var claudeTurnRead func(promptID string)

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

// claudeTranscriptTurns parses the end of Claude Code's transcript once and
// finds the turn that is ending in it; with withEarlier, also every earlier
// turn of the session in the part read, each from the record that starts it
// up to the start of the next. An earlier turn is one a background
// subagent's searches can still land in after the turn's own Stop (issue
// #70, case 1), and claudeTurnEnd reports it late. Nothing is scrubbed
// here: a turn is read only when its detail is asked for.
func claudeTranscriptTurns(ops hookOps, p claudeStopPayload, withEarlier bool) (current claudeTurn, earlier []claudeTurn) {
	current.PromptID = p.PromptID
	if p.TranscriptPath == "" || ops.readTail == nil {
		return current, nil
	}
	tail, err := ops.readTail(p.TranscriptPath, turnDetailTail)
	if err != nil {
		return current, nil
	}
	type entry struct {
		Type        string `json:"type"`
		PromptID    string `json:"promptId"`
		Timestamp   string `json:"timestamp"`
		IsSidechain bool   `json:"isSidechain"`
		startMarks
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
	detailOf := func(start, end int) *turnDetail {
		if claudeTurnRead != nil {
			claudeTurnRead(entries[start].PromptID)
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
		// The turn end promises the router "the user's message" (invariant 2),
		// and a `user` record Claude Code wrote is not one, even where it starts
		// the turn (issue #70). participantWrote decides; such a turn is still
		// reported, with its searches, and with no user text. A turn whose start
		// is the summary written when the conversation is compacted loses the
		// participant's message from before the compaction; this does not try to
		// recover it.
		if !participantWrote(entries[start].startMarks, userRaw) {
			userRaw = ""
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
		for _, e := range entries[start+1 : end] {
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
					if b.Type == "text" && b.Text != "" {
						d.lastText = b.Text
					}
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

	// The turn that is ending: the last user turn, and — where the transcript
	// names prompts — the one this Stop names. A transcript whose last user
	// turn belongs to another prompt is not this turn's, and is not read.
	// Found from the end, as before there were earlier turns to look for: a
	// Stop that looks for none does no more than that.
	if !withEarlier {
		for i := len(entries) - 1; i >= 0; i-- {
			if isUserTurn(entries[i]) {
				if entries[i].PromptID == "" || p.PromptID == "" || entries[i].PromptID == p.PromptID {
					current.read = func() *turnDetail { return detailOf(i, len(entries)) }
				}
				break
			}
		}
		return current, nil
	}
	var starts []int
	for i, e := range entries {
		if isUserTurn(e) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return current, nil
	}
	if last := starts[len(starts)-1]; entries[last].PromptID == "" || p.PromptID == "" || entries[last].PromptID == p.PromptID {
		current.read = func() *turnDetail { return detailOf(last, len(entries)) }
	}
	// Every earlier turn that names its prompt: from its first start to the
	// next start of another prompt. A turn with no prompt id cannot be
	// joined to a search's mark, and is not read.
	for k, at := range starts {
		id := entries[at].PromptID
		if id == "" || id == p.PromptID || (k > 0 && entries[starts[k-1]].PromptID == id) {
			continue
		}
		end := len(entries)
		for _, next := range starts[k+1:] {
			if entries[next].PromptID != id {
				end = next
				break
			}
		}
		earlier = append(earlier, claudeTurn{PromptID: id, read: func() *turnDetail { return detailOf(at, end) }})
	}
	return current, earlier
}

// startMarks are the fields of a Claude Code `user` record that say who
// wrote it. Origin is kept raw: the whole record is decoded at once, and a
// field of the wrong type would fail that decode and drop the record, so an
// earlier user record would become the turn's start. Its shape is judged in
// participantWrote instead.
type startMarks struct {
	Origin                    json.RawMessage `json:"origin"`
	IsMeta                    bool            `json:"isMeta"`
	IsCompactSummary          bool            `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool            `json:"isVisibleInTranscriptOnly"`
}

// participantWrote: is the text of the record that starts a turn the
// participant's own, to be sent as the user's message? Only when all three
// signals say so, and each can only withhold:
//
//   - origin is absent, or an object whose kind is exactly "human". A typed
//     message carries {"kind":"human"}; a headless `claude -p` prompt carries
//     no origin at all, which is why "origin.kind is human" cannot be the
//     rule alone. Every other kind (task-notification, peer, ...) and every
//     other shape is the harness;
//   - none of the flags Claude Code sets on a record it wrote: isMeta (a
//     continuation, a loop tick, another session's message),
//     isCompactSummary and isVisibleInTranscriptOnly (the compaction
//     summary);
//   - the text does not open with a harnessUserTags envelope.
func participantWrote(m startMarks, text string) bool {
	if len(m.Origin) > 0 {
		var origin map[string]any
		if json.Unmarshal(m.Origin, &origin) != nil {
			return false
		}
		if kind, _ := origin["kind"].(string); kind != "human" {
			return false
		}
	}
	if m.IsMeta || m.IsCompactSummary || m.IsVisibleInTranscriptOnly {
		return false
	}
	return !harnessWroteUserText(text)
}

// harnessUserTags are the envelopes Claude Code wraps around a message it
// writes into the session as a `user` record without a flag that says so.
// Each was seen starting an answered turn in a capture: a background task's
// result, the output of a `!` command, and a local command's output. A
// closed list, grown on evidence: a participant's own message can begin
// with "<", and <bash-input>, <command-name> and <local-command-caveat>
// were never seen starting an answered turn, so they are not here.
var harnessUserTags = []string{"<task-notification>", "<bash-stdout>", "<local-command-stdout>"}

func harnessWroteUserText(text string) bool {
	text = strings.TrimSpace(text)
	for _, tag := range harnessUserTags {
		if strings.HasPrefix(text, tag) {
			return true
		}
	}
	return false
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
