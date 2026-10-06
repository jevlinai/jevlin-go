package main

// Codex's hooks (issue #19): `hook [-config file] codex <Event>`, the event in
// Codex's own spelling.
//
// Codex reads hooks from ~/.codex/hooks.json, in Claude Code's shape, and
// sends a payload whose keys are Claude Code's: nothing in it names Codex. So
// the caller is named by the command line, the way Cursor's and Hermes' are:
// `agents install` writes Codex's entries into Codex's file, and the word
// `codex` in them is how this binary knows who called. The payload is read
// only for contradiction (codexEntryStandsDown).
//
//	PreToolUse    the bridge: this installation's exact rendered search gets
//	              the trace envelope and an allow; anything else, silence.
//	SessionStart  seed the window, start a flush.
//	PreCompact,   bump the window, once per compaction.
//	PostCompact
//	Stop          queue the turn end, when the installation opted in and a
//	              search of ours was served in the turn (codex_turn_end.go);
//	              start a flush.
//
// Fail-open, as every hook here: any doubt is silence and exit 0.

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"runtime"
	"strings"
)

// codexEntryEvent names the event a Codex entry point is installed under. ok
// is false for anything else, including an event this client never installs.
// codexHooks is the authority on what is installed; a test holds the two
// together.
func codexEntryEvent(args []string) (event string, ok bool) {
	if len(args) < 2 || args[0] != "codex" {
		return "", false
	}
	switch args[1] {
	case "PreToolUse", "SessionStart", "PreCompact", "PostCompact", "Stop":
		return args[1], true
	}
	return "", false
}

// codexEntryStandsDown reports whether a payload that reached a Codex entry
// point contradicts Codex having sent it. Stricter than the Claude Code rule
// on purpose: no installed base depends on a silent payload here, and every
// payload Codex was seen to send names its event.
//
//   - runByAnotherHost: `cursor_version`, or an event name that is not, byte
//     for byte, the one this entry is installed under.
//   - Not a JSON object, or no `hook_event_name` at all.
//   - A `transcript_path` that is a non-empty string and not one of Codex's
//     rollout files. Null or absent is not evidence: the desktop app sent a
//     null one, and so does `codex exec --ephemeral`. The directory is not
//     read, because CODEX_HOME moves it.
//
// `tool_use_id` is not read: it exists only on tool events, and its prefix
// is one version's naming.
func codexEntryStandsDown(payload []byte, event string) bool {
	if runByAnotherHost(payload, event) {
		return true
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(payload, &keys) != nil || keys == nil {
		return true
	}
	if _, named := keys["hook_event_name"]; !named {
		return true
	}
	if raw, present := keys["transcript_path"]; present {
		var transcript *string
		if json.Unmarshal(raw, &transcript) != nil {
			return true
		}
		if transcript != nil && *transcript != "" && !isCodexRollout(*transcript) {
			return true
		}
	}
	return false
}

// isCodexRollout: the file is one of Codex's session transcripts,
// rollout-<time>-<thread id>.jsonl, whatever directory holds it.
func isCodexRollout(p string) bool {
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl")
}

// hookCodex handles one Codex event on this OS.
func hookCodex(ops hookOps, hc hookContext, event string, payload []byte, stdout io.Writer) {
	hookCodexOn(ops, hc, runtime.GOOS, event, payload, stdout)
}

// hookCodexOn is hookCodex for a given OS, so every OS's answer is exercised
// on every runner. On an OS where nothing has established what runs a Codex
// hook (Windows), the install writes none, and an entry found there anyway
// was not written for it: it does nothing.
func hookCodexOn(ops hookOps, hc hookContext, goos, event string, payload []byte, stdout io.Writer) {
	if runners, err := declaredShells(codexTarget{}, goos, channelHook); err != nil || len(runners) == 0 {
		return
	}
	flush := func() {
		if ops.spawnFlush != nil {
			_ = ops.spawnFlush(hc.cfgPath)
		}
	}
	switch event {
	case "PreToolUse":
		codexLineage(ops, hc, goos, payload, stdout)
	case "SessionStart":
		// Codex starts a session again after a compaction (source
		// "compact"); hookWindow keeps an existing session's generation, so
		// only the first one seeds it.
		hookWindow(ops, hc, "session-start", payload)
		flush()
	case "PreCompact":
		hookWindow(ops, hc, "pre-compact", payload)
	case "PostCompact":
		hookWindow(ops, hc, "post-compact", payload)
	case "Stop":
		// The turn is over. Its turn end is queued first — only when the
		// installation opted in and a search of ours was served in this turn
		// — and then the flush starts, as Claude Code's Stop does.
		codexTurnEnd(ops, hc, payload)
		flush()
	}
}

// codexPayload is what codexLineage reads from a PreToolUse payload. Codex
// names its turn `turn_id`; a subagent's payload carries the parent's
// `session_id` and its own `agent_id`.
type codexPayload struct {
	SessionID string         `json:"session_id"`
	TurnID    string         `json:"turn_id"`
	ToolUseID string         `json:"tool_use_id"`
	AgentID   string         `json:"agent_id"`
	ToolName  string         `json:"tool_name"`
	Cwd       string         `json:"cwd"`
	ToolInput map[string]any `json:"tool_input"`
}

// codexLineage is Codex's PreToolUse. It answers for exactly one command:
// this installation's own rendered search, byte for byte what its skill
// teaches, naming its binary and its config. That command gets the trace
// envelope in front of it, in the syntax of the shell Codex runs it in, and
// an allow. Every other command gets silence — no rewrite, no decision, no
// file — which is stricter than Claude Code's hook on purpose: an answer
// without a decision is not established on Codex, and an allow for anything
// but our own command is not this client's to give.
func codexLineage(ops hookOps, hc hookContext, goos string, payload []byte, stdout io.Writer) {
	var p codexPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.SessionID == "" || p.ToolInput == nil {
		return
	}
	// The entry's matcher is "*", so this runs in front of every tool call:
	// spawning an agent, waiting for one. Only the shell tool can be our
	// search, and anything else leaves before anything is read or written.
	if p.ToolName != "Bash" {
		return
	}
	command, isShell := p.ToolInput["command"].(string)
	if !isShell || command == "" {
		return
	}
	shells, err := declaredShells(codexTarget{}, goos, channelTool)
	if err != nil || len(shells) != 1 {
		return
	}
	sh := shells[0]
	if !isSearchForm(recognizeRenderedForm(command, ops.executable, hc.cfgPath, []shellKind{sh})) {
		return
	}

	env := &traceEnvelope{
		V:         traceVersion,
		Harness:   orString(ops.getenv("JEVLIN_HARNESS"), "codex"),
		SessionID: traceHash(p.SessionID),
		Window:    hookWindowID(ops, hc, p.SessionID),
	}
	if p.TurnID != "" {
		env.TurnID = traceHash(p.SessionID + "|" + p.TurnID)
	}
	if p.ToolUseID != "" {
		env.CallID = traceHash(p.SessionID + "|" + p.ToolUseID)
	}
	if p.AgentID != "" {
		// A subagent's payload carries the PARENT's session id and its own
		// agent id, which is itself a Codex thread id. Its lane is that id;
		// its parent is the id the orchestrator's own searches carry.
		env.ParentSessionID = env.SessionID
		env.SessionID = traceHash(p.AgentID)
	}
	// No history: Codex's transcript format is not this client's to read,
	// and a host that exposes less sends less (invariant 16).
	env = capTrace(env)
	if env == nil {
		return
	}

	bridge, err := encodeTraceBridge(env)
	if err != nil {
		return
	}
	// A search the bridge cannot be put on — a binary path the placement
	// rule cannot read past, a query that names the bridge — gets silence,
	// and silence means no file either.
	rewritten, ok := withTraceBridge(sh, bridge, command)
	if !ok {
		return
	}

	// The lineage file, as Claude Code's hook writes it, and before the
	// answer, so the two never disagree about which call is current. A Codex
	// search carrying no bridge never reads it (searchTrace's walk needs a
	// harness in the environment), so it is kept for parity and claimed as
	// nothing more.
	if hc.sessionsDir != "" && p.Cwd != "" {
		_ = updateLineage(ops, lineagePath(hc.sessionsDir, p.Cwd), ops.now(), func(l *lineageFile) {
			l.Harness, l.SessionID, l.TurnID, l.CallID, l.Window = env.Harness, env.SessionID, env.TurnID, env.CallID, env.Window
			l.ParentSessionID = env.ParentSessionID
			l.Seq++
			l.History = nil
		})
	}
	updated := make(map[string]any, len(p.ToolInput))
	for k, v := range p.ToolInput {
		updated[k] = v
	}
	updated["command"] = rewritten
	// The one shape a live run established: the decision and the input
	// together. No reason string — whether Codex's schema takes a key it
	// does not name is not established.
	out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":      "PreToolUse",
		"permissionDecision": "allow",
		"updatedInput":       updated,
	}})
	if err != nil {
		return
	}
	fmt.Fprintln(stdout, string(out))
}
