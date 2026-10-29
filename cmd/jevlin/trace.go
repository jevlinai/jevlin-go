package main

// The trace envelope: lineage that rides INSIDE a /v1/search request so the
// search router can group one task's searches. Ported from Telem's design
// (trajectory threading via a bridge parameter), adapted to this proxy.
//
// Privacy shape, stated once: hard invariant 2 forbids the PROXY retaining
// or logging prompt/completion content of proxied traffic. This envelope is
// content the CLIENT (the mcp command, at the participant's direction)
// chooses to send upstream in its own request body — the same category as
// the query itself. The proxy forwards it byte-for-byte, never logs it, and
// the observer reads response metadata only. The participant's controls:
// JEVLIN_TRACE=off disables it entirely, and raw host session ids are
// hashed before they leave the machine — the router sees stable identifiers,
// never the host's own ids.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/pkg/redact"
)

const (
	traceVersion = 1
	// The visible-text cap: what may ride as history, after truncation.
	traceHistoryCap = 32 << 10
	// The whole-envelope cap (compact JSON). Above it, history is dropped
	// first; an envelope that is still too big is dropped whole. A trace
	// must never be the reason a search fails.
	traceEnvelopeCap = 48 << 10
)

type traceHistory struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type traceEnvelope struct {
	V         int    `json:"v"`
	Harness   string `json:"harness,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// ParentSessionID is set only on a subagent's envelope: the session id
	// the orchestrator's own searches carry. A subagent's session id is a
	// hash that cannot be mapped back to its parent's, so without this the
	// router can see that two lanes shared a turn but not which one spawned
	// the other. Hashed like every other id; it names no agent and no task.
	ParentSessionID string          `json:"parent_session_id,omitempty"`
	TurnID          string          `json:"turn_id,omitempty"`
	CallID          string          `json:"call_id,omitempty"`
	Window          string          `json:"window,omitempty"`
	Seq             int             `json:"seq,omitempty"`
	History         []traceHistory  `json:"history,omitempty"`
	HostMeta        json.RawMessage `json:"host_meta,omitempty"`
}

// traceHash derives a stable identifier using domain-separated SHA-256
// with a public prefix, truncated to 16 bytes and encoded as hex.
func traceHash(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("tokendrop-trace-v1|" + raw))
	return hex.EncodeToString(sum[:16])
}

// traceKeyedHash derives a stable identifier from guessable local facts
// (hostname, parent pid). Unlike traceHash it is keyed with the
// installation's secret trace key, so whoever receives it cannot recover
// the inputs by enumerating them. Empty when there is no key.
func traceKeyedHash(key []byte, raw string) string {
	if len(key) == 0 || raw == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("jevlin-trace-local-v1|" + raw))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// traceRandomID is the per-process fallback session identity for hosts with
// no hook channel: one id per mcp process, which is one agent session.
func traceRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// encodeTraceBridge / decodeTraceBridge carry an envelope through the one
// channel a hook has: a string parameter on the tool call.
func encodeTraceBridge(env *traceEnvelope) (string, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeTraceBridge(s string) *traceEnvelope {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	var env traceEnvelope
	if err := json.Unmarshal(b, &env); err != nil || env.V != traceVersion {
		return nil
	}
	return &env
}

// capTrace enforces the size discipline: scrub history text, THEN truncate
// it (keeping the END — the words nearest the search are the ones that
// explain it), then drop history, then drop the envelope. Returns nil when
// nothing may ride.
//
// Complete source text must reach preparation before any destructive cap.
// TraceText preserves ordinary trajectory prose; see its package contract.
func capTrace(env *traceEnvelope) *traceEnvelope {
	if env == nil {
		return nil
	}
	for i := range env.History {
		env.History[i].Text = prepareTraceText(env.History[i].Text)
	}
	if b, err := json.Marshal(env); err == nil && len(b) <= traceEnvelopeCap {
		return env
	}
	env.History = nil
	if b, err := json.Marshal(env); err == nil && len(b) <= traceEnvelopeCap {
		return env
	}
	return nil
}

// capTraceForCommandLine trims env until the command carrying its encoded
// bridge fits the host's command line: history text is cut from the FRONT —
// keeping the end, the words nearest the search, as capTrace's own
// truncation does — then history is dropped, and nil means even the bare
// envelope cannot ride and the command must go out with no bridge at all,
// because a trace must never be the reason a search fails. The text was
// scrubbed in capTrace before it got here, so this cut comes after the
// scrub, never before it. On an OS with no command-line limit it changes
// nothing. The same rule the JavaScript adapters apply through
// bridgeBudgetFor and traceBridge's budget in agent_trace_common.js.
//
// len(cmd) counts bytes where Windows counts UTF-16 characters; bytes are
// never fewer, so the error is in the safe direction.
func capTraceForCommandLine(env *traceEnvelope, sh shellKind, cmd string) *traceEnvelope {
	limit := hostCommandCharLimit()
	if env == nil || limit == 0 {
		return env
	}
	// base64url writes 3 bytes as 4 characters, so the JSON may take 3/4 of
	// what the limit leaves after the command and the bridge's syntax —
	// floor(budget*3/4), the same formula traceBridge applies in the JS
	// adapters. The two do not always trim to the same place: encoding/json
	// writes <, > and & as six-byte escapes where JSON.stringify writes one
	// byte, so a history holding them is cut shorter here. Each side measures
	// the JSON it actually sends, so neither overruns the limit.
	return capTraceToFit(env, (limit-bridgeSyntaxOverhead(sh)-len(cmd))*3/4)
}

// capTraceToFit trims env.History on a copy of its own: the entries a caller
// handed in may be shared, as the lineage file's are in hook.go, and a cut
// made in them would cut that copy too.
func capTraceToFit(env *traceEnvelope, jsonLimit int) *traceEnvelope {
	env.History = append([]traceHistory(nil), env.History...)
	b, err := json.Marshal(env)
	if err != nil {
		return nil
	}
	for len(b) > jsonLimit && len(env.History) > 0 {
		over := len(b) - jsonLimit
		if text := env.History[0].Text; len(text) <= over {
			env.History = env.History[1:]
			if len(env.History) == 0 {
				env.History = nil
			}
		} else {
			// Walk forward off any continuation byte so the cut lands on a
			// rune boundary, as prepareTraceText's tail cut does. A cut text
			// byte removes at least one serialized byte, so one pass per
			// entry is enough and the loop always ends.
			start := over
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
			env.History[0].Text = text[start:]
		}
		if b, err = json.Marshal(env); err != nil {
			return nil
		}
	}
	if len(b) > jsonLimit {
		env.History = nil
		if b, err = json.Marshal(env); err != nil || len(b) > jsonLimit {
			return nil
		}
	}
	return env
}

// traceScrubTestHook, when set, runs on every text prepareTraceText is about
// to scrub. Tests set it to panic, standing in for a scrubber bug, to hold
// search and the hooks to failing open when one happens (#48).
var traceScrubTestHook func(text string)

// prepareTraceText accepts complete bounded entries and retains a UTF-8 tail
// only after scrubbing. Oversize entries are omitted without slicing them.
func prepareTraceText(text string) string {
	if len(text) > hookTailBytes {
		return ""
	}
	if traceScrubTestHook != nil {
		traceScrubTestHook(text)
	}
	text = redact.TraceText(text)
	if len(text) > traceHistoryCap {
		start := len(text) - traceHistoryCap
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
		text = text[start:]
	}
	return text
}

// traceOff is the one definition of "the participant switched tracing off"
// (JEVLIN_TRACE=off, 0 or false). Everything that follows from it follows
// from here: no envelope rides the search, no turn end is sent, and — since
// the trajectory is what mining pays for and what the free tier is served
// on — mining is off too: nothing is enrolled, nothing is spooled, the
// search is billed at list and earns nothing.
func traceOff(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("JEVLIN_TRACE"))) {
	case "off", "0", "false":
		return true
	}
	return false
}
