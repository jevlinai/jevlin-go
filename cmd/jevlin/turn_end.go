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
	"github.com/jevlinai/jevlin-go/pkg/redact"
)

const (
	turnEndTimeout = 10 * time.Second
	turnEndSuffix  = ".turn-end.json"
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
}

// prepareFinalText scrubs the assistant's final message and keeps its tail,
// the same order trace history is prepared in: scrub the complete text, then
// cut. A message too large to scrub whole is omitted whole.
func prepareFinalText(text string) (prepared string, chars int, truncated bool) {
	chars = utf8.RuneCountInString(text)
	if len(text) > hookTailBytes {
		return "", chars, true
	}
	scrubbed := redact.TraceText(text)
	prepared = prepareTraceText(text)
	return prepared, chars, len(prepared) < len(scrubbed)
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

// markTurnSearched records that a search was served in the trace's turn. It
// is the evidence the turn end asks for, and it is written only AFTER the
// router answered: the lineage hook stamps a turn before the command runs,
// which says a search was proposed, not that one happened. Best effort, and
// nothing at all unless the installation opted in.
func markTurnSearched(ops hookOps, m config.Miner, trace *traceEnvelope) {
	if !m.TurnEnd || m.SessionsDir == "" || trace == nil || trace.TurnID == "" {
		return
	}
	if err := ops.mkdirAll(m.SessionsDir, 0o700); err != nil {
		return
	}
	_ = ops.writeFile(turnSearchedPath(m.SessionsDir, trace.TurnID), nil, 0o600)
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
// removes the mark: a turn ends once.
func takeTurnSearched(ops hookOps, dir, turnID string) bool {
	path := turnSearchedPath(dir, turnID)
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
	path := filepath.Join(hc.sessionsDir, traceHash("turn-end|"+rec.SessionID+"|"+rec.TurnID)+turnEndSuffix)
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
//   - the turn has no final message to send. Its mark is taken all the same:
//     the turn has ended.
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
	turn := traceHash(p.SessionID + "|" + p.PromptID)
	if !takeTurnSearched(ops, hc.sessionsDir, turn) {
		return
	}
	text, chars, truncated := prepareFinalText(p.LastAssistantMessage)
	if text == "" {
		return
	}
	queueTurnEnd(ops, hc, turnEndRecord{
		SessionID:  traceHash(p.SessionID),
		TurnID:     turn,
		Harness:    orString(ops.getenv("JEVLIN_HARNESS"), "claude-code"),
		Status:     turnCompleted,
		FinalText:  text,
		FinalChars: chars,
		Truncated:  truncated,
	})
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
		if l.AnswerTurnID != turn || len(l.History) != 1 || l.History[0].Role != "assistant" {
			return
		}
		rec.FinalText, rec.FinalChars, rec.Truncated = prepareFinalText(l.History[0].Text)
		if rec.FinalText == "" {
			return
		}
	case "aborted":
		rec.Status = turnInterrupted
	case "error":
		rec.Status = turnFailed
	default:
		return
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
	body, err := os.ReadFile(path) // #nosec G304 -- confined to the sessions directory and the turn-end suffix just above
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
