package main

// The turn end: what the assistant concluded, sent once when a turn finishes.
//
// A search's trace carries what the assistant wrote BEFORE it searched. The
// answer is written after the last search, when no request is being made, so
// the router never saw it. With `[miner] turn_end = true` — off by default —
// the host's end-of-turn hook reports the turn's final message to the
// router's POST /v1/turns, under the same hashed session and turn ids the
// turn's searches carried.
//
// Privacy shape. This is the second channel that carries model-written text
// off the machine (hard invariant 2 names the first, the trace), and it is
// narrower than it sounds:
//
//   - opt-in, per installation; JEVLIN_TRACE=off also turns it off;
//   - only for a turn in which a search was SERVED: `jevlin search` marks the
//     turn after the router answers. A search the model proposed and the
//     user refused, or one that failed, marks nothing, and a turn with no
//     mark sends nothing;
//   - only to a router the config NAMES (miner.router_url). The provider
//     upstream that stands in for it when it is absent is never sent this;
//   - the same scrub and the same 32 KiB tail cap as trace history, applied
//     before anything is written or sent;
//   - only the assistant's last message — never the prompt, never a tool's
//     input or output.
//
// Shape. The hook does no network I/O: it writes the prepared record to an
// owner-only file under the sessions directory and starts a detached
// `jevlin turn-end`, exactly as it starts a flush. The text is never an
// argument. The child sends once and deletes the file whatever happens; a
// lost turn end is a missing convenience, never a retry loop.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
	"github.com/jevlinai/jevlin-go/pkg/redact"
)

const (
	turnEndTimeout = 10 * time.Second
	// turnEndMaxBytes bounds the queued record the sender reads back: its
	// texts are each capped well below it, so a larger file is not one this
	// client queued.
	turnEndMaxBytes = 1 << 20
	turnEndSuffix   = ".turn-end.json"
	// A mark that a search was served in a turn: an empty file named after
	// the hashed turn id. Written by `search`, taken by the turn end.
	turnSearchedSuffix = ".searched"
	// A turn that is interrupted never ends, and its mark is never taken.
	// Marks older than this are swept when the next one is written.
	turnSearchedMaxAge = 24 * time.Hour
	// The router's own vocabulary (POST /v1/turns).
	turnCompleted   = "completed"
	turnInterrupted = "interrupted"
	turnFailed      = "failed"
)

// turnEndRecord is the request body of POST /v1/turns, and the spool file.
type turnEndRecord struct {
	SessionID       string `json:"session_id"`
	TurnID          string `json:"turn_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Harness         string `json:"harness,omitempty"`
	Status          string `json:"status,omitempty"`
	FinalText       string `json:"final_text,omitempty"`
	FinalChars      int    `json:"final_chars,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	// The turn's conversation (turn_detail.go). Absent when it could not be
	// read whole, and for hosts this client has no transcript reader for.
	UserText      string     `json:"user_text,omitempty"`
	UserChars     int        `json:"user_chars,omitempty"`
	UserTruncated bool       `json:"user_truncated,omitempty"`
	Model         string     `json:"model,omitempty"`
	Usage         *turnUsage `json:"usage,omitempty"`
	Steps         []turnStep `json:"steps,omitempty"`
}

// prepareFinalText scrubs the assistant's final message and keeps its tail,
// the same order trace history is prepared in: scrub the complete text, then
// cut. A message too large to scrub whole is omitted whole.
func prepareFinalText(text string) (prepared string, chars int, truncated bool) {
	chars = utf8.RuneCountInString(text)
	if len(text) > hookTailBytes {
		return "", chars, true
	}
	// Scrubbed once, then cut: the tail, on a rune boundary, as
	// prepareTraceText does for history.
	prepared = redact.TraceText(text)
	if len(prepared) > traceHistoryCap {
		start := len(prepared) - traceHistoryCap
		for start < len(prepared) && !utf8.RuneStart(prepared[start]) {
			start++
		}
		return prepared[start:], chars, true
	}
	return prepared, chars, false
}

// turnEndEnabled: the installation opted in, and tracing is not switched off.
func turnEndEnabled(ops hookOps, hc hookContext) bool {
	if !hc.turnEnd || hc.sessionsDir == "" {
		return false
	}
	switch strings.ToLower(ops.getenv("JEVLIN_TRACE")) {
	case "off", "0", "false":
		return false
	}
	return true
}

func turnSearchedPath(dir, turnID string) string {
	return filepath.Join(dir, traceHash("turn-searched|"+turnID)+turnSearchedSuffix)
}

// turnEndedPath is the mark that a turn's end was queued, turnSubagentPath
// the mark that a subagent's search was served in the turn, and
// sessionSearchedPath the mark that a subagent of the session searched. All
// carry the turn marks' suffix, so the same sweep removes them, and no name
// can be another's: an id is a hash, with no "|" in it. That is
// checked, not assumed (hashedIDRe, before any mark is written or read for
// an id): a turn id arrives in a trace, which any command can write, and
// "ended|<turn>" taken as a turn id would name that turn's ended mark.
func turnEndedPath(dir, turnID string) string {
	return turnSearchedPath(dir, "ended|"+turnID)
}

func turnSubagentPath(dir, turnID string) string {
	return turnSearchedPath(dir, "subagent-turn|"+turnID)
}

func sessionSearchedPath(dir, sessionID string) string {
	return turnSearchedPath(dir, "subagent|"+sessionID)
}

// markTurnSearched records that a search was served in the trace's turn. It
// is the evidence the turn end asks for, and it is written only AFTER the
// router answered: the lineage hook stamps a turn before the command runs,
// which says a search was proposed, not that one happened. Best effort, and
// nothing at all unless the installation opted in.
//
// A turn ends once. A search that lands in a turn whose end was already
// queued leaves no mark: a background subagent's search can carry the turn
// that launched it long after that turn's Stop, and a second end for the
// same turn would not be the turn's end.
//
// A subagent's search also marks the turn as a subagent's
// (turnSubagentPath) and its session (sessionSearchedPath): it is the one
// way a search lands in a turn other than the one now running. Claude Code
// gives a subagent the turn that launched it, and a background subagent
// outlives that turn. The next Stop in the session that finds the session's
// mark looks for such turns, and reports only a turn that carries the
// subagent's mark (claudeTurnEnd).
//
// Only a Claude Code subagent's search marks anything (issue #70, case 3).
// Codex and opencode give a subagent a turn of its own — Codex its own
// thread's, opencode its child session's — and no turn end of ours is ever
// sent for a subagent's turn: Codex runs no Stop of ours for a subagent's
// thread, and opencode's plugin reports only a session with no parent. Their
// marks were never taken. A host this client does not know to share a turn
// with its subagent is treated the same way, so a mark is never left that
// nothing will take.
func markTurnSearched(ops hookOps, m config.Miner, trace *traceEnvelope) {
	if !m.TurnEnd || m.SessionsDir == "" || trace == nil || !hashedIDRe.MatchString(trace.TurnID) {
		return
	}
	if trace.ParentSessionID != "" && (trace.Harness != "claude-code" || !hashedIDRe.MatchString(trace.ParentSessionID)) {
		return
	}
	if err := ops.mkdirAll(m.SessionsDir, 0o700); err != nil {
		return
	}
	if _, err := ops.readFile(turnEndedPath(m.SessionsDir, trace.TurnID)); err == nil {
		return
	}
	_ = ops.writeFile(turnSearchedPath(m.SessionsDir, trace.TurnID), nil, 0o600)
	if trace.ParentSessionID != "" {
		// Written after the turn's mark, each, so a Stop that finds one
		// finds those before it; and the session's written afresh, so the
		// sweep counts from the latest search rather than the first.
		_ = ops.writeFile(turnSubagentPath(m.SessionsDir, trace.TurnID), nil, 0o600)
		path := sessionSearchedPath(m.SessionsDir, trace.ParentSessionID)
		_ = ops.remove(path)
		_ = ops.writeFile(path, nil, 0o600)
	}
	sweepTurnMarks(m.SessionsDir, ops.now())
}

// sweepTurnMarks removes marks no turn end came for.
func sweepTurnMarks(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), turnSearchedSuffix) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > turnSearchedMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// takeTurnSearched reports whether a search was served in the turn, and
// removes the mark, and a subagent's mark on the turn with it: a turn ends
// once, and the ended mark it leaves keeps a later search from marking the
// turn again.
//
// The ended mark is written before the mark is removed, so a search that
// lands while the mark is taken finds the turn ended and marks nothing. A
// search that looked for the ended mark just before it was written can
// still leave its mark just after; a mark found beside the ended mark is
// that one, and it is removed without a second end.
func takeTurnSearched(ops hookOps, dir, turnID string) bool {
	if !hashedIDRe.MatchString(turnID) {
		return false
	}
	path := turnSearchedPath(dir, turnID)
	if _, err := ops.readFile(path); err != nil {
		return false
	}
	ended := turnEndedPath(dir, turnID)
	_, err := ops.readFile(ended)
	first := err != nil
	if first {
		_ = ops.writeFile(ended, nil, 0o600)
	}
	_ = ops.remove(path)
	_ = ops.remove(turnSubagentPath(dir, turnID))
	return first
}

// takeSubagentTurn takes the mark of a turn that is not the one ending, and
// only when a subagent's search left it: only a subagent's search lands in a
// turn after that turn's Stop. A mark a main agent left on an earlier turn
// is one whose Stop never came — the turn was interrupted — and reporting it
// now would report it as completed, with an answer it may never have given.
// It is left for the sweep, as it always was.
func takeSubagentTurn(ops hookOps, dir, turnID string) bool {
	if !hashedIDRe.MatchString(turnID) {
		return false
	}
	if _, err := ops.readFile(turnSubagentPath(dir, turnID)); err != nil {
		return false
	}
	return takeTurnSearched(ops, dir, turnID)
}

// takeSessionSearched reports whether a subagent of the session searched
// since the last Stop that looked, and removes the mark. It is removed before
// the turns are looked at, so a search that lands while they are leaves it
// again for the next Stop.
func takeSessionSearched(ops hookOps, dir, sessionID string) bool {
	if !hashedIDRe.MatchString(sessionID) {
		return false
	}
	path := sessionSearchedPath(dir, sessionID)
	if _, err := ops.readFile(path); err != nil {
		return false
	}
	_ = ops.remove(path)
	return true
}

// queueTurnEnd writes the record where the detached sender will find it and
// starts the sender. Any failure is silence: the hook has a turn to end.
func queueTurnEnd(ops hookOps, hc hookContext, rec turnEndRecord) {
	if rec.SessionID == "" || rec.TurnID == "" || ops.spawnTurnEnd == nil {
		return
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := ops.mkdirAll(hc.sessionsDir, 0o700); err != nil {
		return
	}
	// The name is unpredictable as well as exclusive (ops.writeFile is
	// CreateNew): the sessions directory is a writable root of Codex's
	// sandbox, and a name a sandboxed command can compute is one it can
	// occupy first. cmdTurnEnd checks only the directory and the suffix.
	name := traceHash("turn-end|" + rec.SessionID + "|" + rec.TurnID)
	if ops.tempSuffix != nil {
		if r := ops.tempSuffix(); r != "" {
			name += "." + r
		}
	}
	path := filepath.Join(hc.sessionsDir, name+turnEndSuffix)
	if err := ops.writeFile(path, body, 0o600); err != nil {
		return
	}
	if err := ops.spawnTurnEnd(hc.cfgPath, path); err != nil {
		_ = ops.remove(path)
	}
}

// ── Claude Code: the Stop hook ──────────────────────────────────────────

type claudeStopPayload struct {
	SessionID            string `json:"session_id"`
	PromptID             string `json:"prompt_id"`
	HookEventName        string `json:"hook_event_name"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
	TranscriptPath       string `json:"transcript_path"`
}

// claudeTurnEnd reports a Claude Code turn that searched. The final message
// is the payload's own `last_assistant_message` — the transcript can lag the
// hook, and this field cannot.
//
// Nothing is sent when:
//   - a stop hook is already continuing the turn (`stop_hook_active`): this
//     Stop is not the turn's end, and the real one follows;
//   - the payload names no prompt (`prompt_id`, Claude Code 2.1.196+): there
//     is no turn id to join on, and a guessed one would attach the answer to
//     another turn's searches;
//   - no search of ours was served in this turn: `search` left no mark for it;
//
// A turn that searched and has no message to send — none in the payload, or
// one too large to scrub whole — is still reported, as completed and without
// text: the router keeps how a turn ended either way.
//
// An earlier turn can have searched since its own Stop (issue #70, case 1):
// a background subagent searches under the turn that launched it, and that
// turn's Stop came first and found nothing. When a subagent of the session
// has searched, this Stop reports every earlier turn of the transcript that
// a subagent's search marked (takeSubagentTurn), late, before its own: the
// participant's message, the steps, and the turn's last assistant text from
// the transcript, scrubbed as the payload's would be.
func claudeTurnEnd(ops hookOps, hc hookContext, payload []byte) {
	if !turnEndEnabled(ops, hc) {
		return
	}
	var p claudeStopPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if p.HookEventName != "Stop" || p.StopHookActive || p.SessionID == "" || p.PromptID == "" {
		return
	}
	session := traceHash(p.SessionID)
	turn := traceHash(p.SessionID + "|" + p.PromptID)
	searched := takeTurnSearched(ops, hc.sessionsDir, turn)
	late := takeSessionSearched(ops, hc.sessionsDir, session)
	if !searched && !late {
		return
	}
	// The transcript is parsed once, and a turn in it is read — scrubbed —
	// only when it is reported. Earlier turns are looked for only when a
	// subagent of the session searched.
	current, earlier := claudeTranscriptTurns(ops, p, late)
	harness := orString(ops.getenv("JEVLIN_HARNESS"), "claude-code")
	for _, t := range earlier {
		id := traceHash(p.SessionID + "|" + t.PromptID)
		if !takeSubagentTurn(ops, hc.sessionsDir, id) {
			continue
		}
		rec := turnEndRecord{SessionID: session, TurnID: id, Harness: harness, Status: turnCompleted}
		if d := t.detail(); d != nil {
			withClaudeTurn(&rec, d.lastText, d)
		}
		queueTurnEnd(ops, hc, rec)
	}
	if searched {
		rec := turnEndRecord{SessionID: session, TurnID: turn, Harness: harness, Status: turnCompleted}
		withClaudeTurn(&rec, p.LastAssistantMessage, current.detail())
		queueTurnEnd(ops, hc, rec)
	}
}

// withClaudeTurn fills a Claude Code turn end with its final message and,
// where the transcript held the turn whole, its conversation.
func withClaudeTurn(rec *turnEndRecord, final string, d *turnDetail) {
	if text, chars, truncated := prepareFinalText(final); text != "" {
		rec.FinalText, rec.FinalChars, rec.Truncated = text, chars, truncated
	}
	if d == nil {
		return
	}
	rec.UserText, rec.UserChars, rec.UserTruncated = d.UserText, d.UserChars, d.UserTruncated
	rec.Model, rec.Usage = d.Model, d.Usage
	rec.Steps = d.Steps
	if rec.FinalText != "" {
		rec.Steps = withoutClosingText(rec.Steps)
	}
}

// ── Cursor: the stop hook ───────────────────────────────────────────────

// cursorTurnEnd reports a Cursor turn that searched. Cursor's `stop` carries
// a status and no text; the text is what `afterAgentResponse` last left in
// the conversation's lineage file — and only when that hook stamped it with
// THIS turn. Without the stamp, a turn whose reply never reached the file
// would be reported with the previous turn's answer. A turn that was aborted
// or failed is reported without text: what the file holds then is not an
// answer.
func cursorTurnEnd(ops hookOps, hc hookContext, p cursorPayload, l *lineageFile) {
	if !turnEndEnabled(ops, hc) || l == nil || p.ConversationID == "" || p.GenerationID == "" {
		return
	}
	turn := traceHash(p.ConversationID + "|" + p.GenerationID)
	if !takeTurnSearched(ops, hc.sessionsDir, turn) {
		return // no search of ours was served in this turn
	}
	rec := turnEndRecord{SessionID: traceHash(p.ConversationID), TurnID: turn, Harness: "cursor"}
	switch p.Status {
	case "", "completed":
		rec.Status = turnCompleted
		// The reply is sent only when it is this turn's. Without one the
		// turn is still reported, as completed and without text.
		if l.AnswerTurnID == turn && len(l.History) == 1 && l.History[0].Role == "assistant" {
			if text, chars, truncated := prepareFinalText(l.History[0].Text); text != "" {
				rec.FinalText, rec.FinalChars, rec.Truncated = text, chars, truncated
			}
		}
	case "aborted":
		rec.Status = turnInterrupted
	case "error":
		rec.Status = turnFailed
	default:
		return
	}
	// The rest of the turn, each piece only if it was stamped with this turn.
	if l.UserTurnID == turn && l.UserText != "" {
		rec.UserText, rec.UserChars, rec.UserTruncated = l.UserText, l.UserChars, l.UserTruncated
	}
	if l.SearchesTurnID == turn {
		for _, call := range l.TurnSearches {
			rec.Steps = append(rec.Steps, turnStep{Kind: "search", CallID: call})
		}
	}
	if toolNameRe.MatchString(p.Model) {
		rec.Model = p.Model
	}
	if in, out := p.InputTokens+p.CacheReadTokens+p.CacheWriteTokens, p.OutputTokens; in > 0 || out > 0 {
		rec.Usage = &turnUsage{InputTokens: in, OutputTokens: out}
	}
	queueTurnEnd(ops, hc, rec)
}

// ── the detached sender ─────────────────────────────────────────────────

func startTurnEnd(cfgPath, file string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"turn-end"}
	if cfgPath != "" {
		args = append(args, "-config", cfgPath)
	}
	return spawnDetached(exe, append(args, "-file", file))
}

// cmdTurnEnd sends one queued turn end. It is started by a hook, detached,
// with nobody reading its output: it prints nothing and always exits 0.
func cmdTurnEnd(args []string, getenv func(string) string) int {
	fs := newFlagSet("turn-end", io.Discard)
	cfgPath := fs.String("config", "", "path to TOML config file")
	file := fs.String("file", "", "the queued turn end")
	if err := fs.Parse(args); err != nil || *file == "" || fs.NArg() != 0 {
		return exitOK
	}
	cfg, _, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		return exitOK
	}
	// Only a file this installation's hook queued: in its sessions directory,
	// under the name queueTurnEnd gives it. The path is an argument, and an
	// argument is not a reason to read, send and delete an arbitrary file.
	path := filepath.Clean(*file)
	if cfg.Miner.SessionsDir == "" || filepath.Dir(path) != filepath.Clean(cfg.Miner.SessionsDir) || !strings.HasSuffix(path, turnEndSuffix) {
		return exitOK
	}
	body, err := fsx.ReadRegular(path, turnEndMaxBytes)
	// Deleted before the send, whatever comes next: one attempt, and no file
	// of assistant text left behind by a send that hung.
	_ = os.Remove(path)
	// Only to a router the config names. With no miner.router_url, RouterURL
	// is the provider upstream standing in for it, and the assistant's answer
	// is not that provider's to receive.
	if err != nil || !cfg.Miner.TurnEnd || !cfg.Miner.RouterConfigured || cfg.Miner.RouterURL == nil {
		return exitOK
	}
	var rec turnEndRecord
	if err := json.Unmarshal(body, &rec); err != nil || rec.SessionID == "" || rec.TurnID == "" {
		return exitOK
	}
	key, _, err := resolveAPIKey(getenv, cfg.Miner)
	if err != nil || key == "" {
		return exitOK
	}
	client := &http.Client{Timeout: turnEndTimeout, CheckRedirect: auth.SameOriginRedirects, Transport: loginProbeTransport}
	_ = sendTurnEnd(client, strings.TrimRight(cfg.Miner.RouterURL.String(), "/"), key, rec)
	return exitOK
}

// sendTurnEnd posts one turn end. The status code is returned for tests; no
// caller acts on it — a router that predates the endpoint answers 404, and
// that is as final as a 202.
func sendTurnEnd(client *http.Client, routerURL, key string, rec turnEndRecord) int {
	body, err := json.Marshal(rec)
	if err != nil {
		return 0
	}
	req, err := http.NewRequest(http.MethodPost, routerURL+"/v1/turns", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", searchUserAgent+"/"+strings.TrimPrefix(buildVersion(), "v"))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode
}
