package main

// What `search` and the hooks pass between themselves through the
// filesystem, since no process outlives a tool call.
//
//   sidecar   one JSON file per workspace (per conversation, for Cursor)
//             holding the lineage the hooks learned (hashed session/turn/
//             call ids, the window generation, the assistant text just
//             before the search). `search` reads it when no bridge arrived
//             in its environment, so a host that cannot rewrite a shell
//             command still threads its searches.
//   root      the installation's own directory, the parent of the state
//             directory: credentials.json, the registration journal, the
//             claim record and the release check's stamp live there, where
//             no sandbox writes.
//
// None of this is a daemon. Between tool calls nothing runs.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

const (
	lineageVersion = 1
	// lineageMaxAge is how long a workspace's lineage stays believable. A
	// sidecar older than this describes a conversation that is over; a
	// search after it belongs to a new one, so `search` falls back to its
	// per-shell session identity rather than thread into the old lane.
	lineageMaxAge = 12 * time.Hour
	// stampMaxBytes bounds what is read back from the state directory, a
	// writable root of Codex's sandbox: a stamp is a few dozen bytes, so a
	// larger file at its name is not one this client wrote.
	stampMaxBytes = 64 << 10
	// lineageWalkUp bounds how many parent directories `search` climbs
	// looking for the workspace a hook wrote for. Hooks key on the project
	// root; agents run commands from subdirectories of it.
	lineageWalkUp = 8
)

// loadConfig resolves the config exactly as describeConfigSource does
// (ruling D-R1: -config, JEVLIN_CONFIG, the installation's own config,
// then defaults) and returns the full config. pkg/config is not changed: the
// resolved path, once found, is handed to config.Load as an explicit
// -config, which is exactly what makes it required to exist — a guarantee
// this function has already checked for the soft-discovered installation
// step before choosing it. A -config naming the installation's own default
// config before setup has written it reads as no -config at all
// (unwrittenDefaultConfig).
func loadConfig(cfgPath string, getenv func(string) string) (*config.Config, string, error) {
	if unwrittenDefaultConfig(cfgPath, getenv) {
		cfgPath = ""
	}
	src := describeConfigSource(cfgPath, getenv)
	args := []string{}
	if src != "" {
		args = []string{"-config", src}
	}
	cfg, _, err := config.Load(args, getenv)
	if err != nil {
		return nil, src, err
	}
	return cfg, src, nil
}

func randomSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	}
	return hex.EncodeToString(b[:])
}

// ── sidecar ─────────────────────────────────────────────────────────────

// sidecar is a workspace's lineage as the hooks last saw it. Every
// identifier is already hashed; the raw host ids never reach disk.
type lineageFile struct {
	V         int    `json:"v"`
	Harness   string `json:"harness,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// ParentSessionID mirrors traceEnvelope's: set while the file holds a
	// subagent's lane, empty otherwise.
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	TurnID          string         `json:"turn_id,omitempty"`
	CallID          string         `json:"call_id,omitempty"`
	Window          string         `json:"window,omitempty"`
	Seq             int            `json:"seq,omitempty"`
	History         []traceHistory `json:"history,omitempty"`
	// AnswerTurnID is the turn the assistant text in History was written in,
	// when a host's hook knows it (Cursor's afterAgentResponse). The turn end
	// sends that text only under this turn, never under a later one.
	AnswerTurnID string `json:"answer_turn_id,omitempty"`
	// The rest of a turn, for a host whose hooks each carry a piece of it
	// (Cursor). Written only when the installation opted in to the turn end,
	// already scrubbed and capped, and cleared when the turn ends. UserText is
	// what the user asked in UserTurnID; TurnSearches are the call ids of the
	// searches made in SearchesTurnID, in order.
	UserText       string    `json:"user_text,omitempty"`
	UserChars      int       `json:"user_chars,omitempty"`
	UserTruncated  bool      `json:"user_truncated,omitempty"`
	UserTurnID     string    `json:"user_turn_id,omitempty"`
	TurnSearches   []string  `json:"turn_searches,omitempty"`
	SearchesTurnID string    `json:"searches_turn_id,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// lineagePath keys a sidecar on the workspace root, hashed like every
// other identifier so a listing of the sessions directory reveals no
// project paths.
func lineagePath(dir, workspace string) string {
	return filepath.Join(dir, traceHash("workspace|"+filepath.Clean(workspace))+".json")
}

// conversationLineagePath keys a Cursor conversation's sidecar on its
// workspace AND its conversation. Keyed by workspace alone, two
// conversations open on one project — two chat tabs, a chat beside a
// background agent — shared one file, took turns overwriting its session and
// advanced one counter between them. Cursor's sessionStart exports this path
// as the session's JEVLIN_LINEAGE, and a Cursor search now carries
// that variable on its own command, so the file is always found by its path
// and never by the walk: lineageForCwd keys by directory alone and is left as
// it is for the hosts that need it. The name has lineagePath's shape, so the
// sweep and the age rule treat it as every other lineage file.
func conversationLineagePath(dir, workspace, conversation string) string {
	return filepath.Join(dir, traceHash("workspace|"+filepath.Clean(workspace)+"|conversation|"+conversation)+".json")
}

func loadLineage(ops hookOps, path string) (*lineageFile, bool) {
	data, err := ops.readFile(path)
	if err != nil {
		return nil, false
	}
	var sc lineageFile
	if err := json.Unmarshal(data, &sc); err != nil || sc.V != lineageVersion {
		return nil, false
	}
	return &sc, true
}

// saveLineage writes atomically (temp + rename) and 0600, preparing
// complete history entries before they reach the workspace sidecar.
func saveLineage(ops hookOps, path string, sc *lineageFile, now time.Time) error {
	sc.V = lineageVersion
	sc.UpdatedAt = now
	for i := range sc.History {
		sc.History[i].Text = prepareTraceText(sc.History[i].Text)
	}
	if err := ops.mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return replaceViaTemp(ops, path, data, now, true)
}

// ── temp, then rename ───────────────────────────────────────────────────

// tempEntry is one "*.tmp" file in a directory, as the sweep needs it.
type tempEntry struct {
	name    string
	modTime time.Time
}

func listTempFiles(dir string) ([]tempEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []tempEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone between the listing and the stat: somebody renamed it
		}
		out = append(out, tempEntry{name: e.Name(), modTime: info.ModTime()})
	}
	return out, nil
}

var (
	// tempNameRe is the one shape these writers produce,
	// "<file>.<pid>-<random>.tmp" (tempNameFor), and the "<file>.<pid>.tmp"
	// earlier versions wrote, which the sweep still collects.
	tempNameRe = regexp.MustCompile(`^(.+)\.(\d+)(?:-[0-9a-f]+)?\.tmp$`)
	// lineageNameRe is a lineage file's name: lineagePath's 32 hex digits.
	lineageNameRe = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)
)

// replaceViaTemp writes data to a new "<path>.<pid>-<random>.tmp"
// (tempNameFor), created exclusively, and renames it over path, so a reader
// never sees a half-written file and no link at the temporary name is written
// through. It is the one writer behind the
// lineage files and the window state.
//
// A failed write or rename removes the temporary file before returning.
// On Windows a rename fails while the target is momentarily held — seen on
// the 0.2.10 release check — and each failure used to leave one
// "<hash>.json.<pid>.tmp" in the sessions directory for good. What was at
// path is untouched by a failure, as before.
func replaceViaTemp(ops hookOps, path string, data []byte, now time.Time, lineageDir bool) error {
	sweepStaleTemps(ops, path, now, lineageDir)
	tmp := tempNameFor(ops, path)
	if err := ops.writeFile(tmp, data, 0o600); err != nil {
		removeTemp(ops, tmp) // a write that failed halfway leaves one too
		return err
	}
	if err := ops.rename(tmp, path); err != nil {
		removeTemp(ops, tmp)
		return err
	}
	return nil
}

// tempNameFor is the temporary name replaceViaTemp writes before its rename.
// The directory is a writable root of Codex's sandbox (pkg/fsx/confined.go),
// so the name is unpredictable as well as exclusive: a sandboxed command
// cannot leave a link at a name it cannot know, and ops.writeFile creates
// the file with O_EXCL whatever it guessed. The pid stays in the name for
// sweepStaleTemps.
func tempNameFor(ops hookOps, path string) string {
	if ops.tempSuffix != nil {
		if s := ops.tempSuffix(); s != "" {
			return fmt.Sprintf("%s.%d-%s.tmp", path, ops.pid, s)
		}
	}
	return fmt.Sprintf("%s.%d.tmp", path, ops.pid)
}

func removeTemp(ops hookOps, tmp string) {
	if ops.remove != nil {
		_ = ops.remove(tmp)
	}
}

// sweepStaleTemps removes temporary files that a crashed writer, or a failed
// removal, left beside path. Best effort and silent: it runs in front of a
// search, and nothing it fails to do matters to that search.
//
// What it may remove is narrow on every axis:
//
//   - the NAME is "<file>.<pid>-<random>.tmp", or the "<file>.<pid>.tmp"
//     earlier versions wrote, where <file> is the file being written or —
//     for a lineage write only — any lineage file's name. The sessions
//     directory is this code's own; the window state can live in a plugin
//     root, so it sweeps only its own.
//   - the PID is not this process's.
//   - the AGE is more than lineageMaxAge. That is the lineage code's own
//     answer to "how long can a session plausibly last": past it the session
//     that wrote the file is, by this code's existing rule, over.
//
// It cannot delete a file another live process is about to rename. A writer
// holds its temporary file for the span between two adjacent calls in
// replaceViaTemp — a write and a rename, microseconds apart — and the sweep
// takes only files last modified more than twelve hours ago. To lose its file
// a writer would have to be stopped between those two calls for half a day (a
// machine suspended at that instant). Even then nothing is corrupted: its
// rename fails, it reports the error its caller already tolerates, and the
// file at path is whatever the last successful writer left — the "lose at
// most one update, never the file" that updateLineage already promises. A
// modification time in the future is not old, so a clock set backwards sweeps
// nothing.
func sweepStaleTemps(ops hookOps, path string, now time.Time, lineageDir bool) {
	if ops.listTemps == nil || ops.remove == nil {
		return
	}
	dir, file := filepath.Dir(path), filepath.Base(path)
	entries, err := ops.listTemps(dir)
	if err != nil {
		return
	}
	own := strconv.Itoa(ops.pid)
	for _, e := range entries {
		m := tempNameRe.FindStringSubmatch(e.name)
		if m == nil || m[2] == own {
			continue
		}
		if m[1] != file && (!lineageDir || !lineageNameRe.MatchString(m[1])) {
			continue
		}
		if now.Sub(e.modTime) <= lineageMaxAge {
			continue
		}
		_ = ops.remove(filepath.Join(dir, e.name))
	}
}

// updateLineage applies one change under read-modify-write. Two hooks
// racing on one workspace lose at most one update, never the file.
func updateLineage(ops hookOps, path string, now time.Time, apply func(*lineageFile)) error {
	sc, ok := loadLineage(ops, path)
	if !ok {
		sc = &lineageFile{}
	}
	apply(sc)
	return saveLineage(ops, path, sc, now)
}

// sameHarness reports whether two harness names are the same name, exactly.
//
// The vocabulary is closed and lowercase — claude-code, cursor, opencode, pi,
// hermes, cli — and every value is written by this client's own hooks, so no
// case or spacing variant arises from anything this client produces. One
// could only come from a participant setting JEVLIN_HARNESS by hand, and
// that same value is what the search then sends to the router as its label:
// treating "Cursor" as "cursor" here would adopt the session and relabel it,
// putting one session under two harness spellings downstream. That is the
// split-session hazard, not a tolerance worth having.
//
// samePath's case-insensitive branch is not a precedent for this one. It has
// an authority behind it — on Windows the filesystem itself says two
// spellings name one file — and nothing says the same about a harness name.
// A loose comparison here would be a rule copied without its argument.
//
// An empty name matches nothing: a sidecar recording no harness cannot be
// shown to belong to anyone.
func sameHarness(a, b string) bool {
	return a != "" && a == b
}

// lineageForCwd finds the sidecar governing a directory: the directory
// itself, then its parents, a bounded number of hops. A stale sidecar is
// treated as absent.
//
// It answers nothing unless the search can say WHOSE session it is making
// and the sidecar agrees. harness is the searching host's own name, as
// JEVLIN_HARNESS gives it.
//
// Both halves of that follow from what this walk is for. Every host that
// carries its lineage in a variable — the bridge, or JEVLIN_LINEAGE
// naming the file outright — is already served before this is reached; the
// walk exists only to find a session's own sidecar again when that variable
// was lost, which is what happens when the search runs from a subshell. So a
// search arriving here with NOTHING naming a session is not one that lost its
// variable: it is one from a host that never had a lineage channel, or from a
// person at a terminal. Looking up the tree on its behalf cannot find its
// session, because its session wrote no sidecar — it can only find somebody
// else's. That is precisely what happened: a Cursor CLI search in a
// subdirectory, and a plain search from a terminal in the same tree, both
// reached the router as harness=claude-code carrying a Claude Code session's
// id and its assistant text, and advanced that session's seq.
//
// A mismatch stops the walk rather than climbing past it. A nearer sidecar
// belonging to someone else says this directory is theirs, and claiming a
// more distant one because its name matches would be a guess; the safe
// direction is an honest per-shell identity rather than a confident wrong
// one. Stopping is also what a stale sidecar already does.
//
// session is the hashed id of the session the shell was started in, as
// JEVLIN_SESSION gives it, and it may be empty. The harness tells
// hosts apart and nothing more: two sessions of ONE host in nested
// workspaces — a monorepo open at its root and again at a package — are the
// same name, and the nearest file won whichever of them was searching.
//
// When the session is known, it is the test of ownership, and a sidecar
// recording another session is not this search's whatever its harness says.
// Such a file is climbed past rather than stopped at, and that does not
// contradict the paragraph above: what made a more distant file a guess was
// that only a NAME matched. A file holding this session's own id is not a
// guess — it is the file this session's hooks wrote — and an agent that
// changed into somebody else's workspace is still making its own search.
// The harness must still agree, and a stale file of this session's own still
// answers nothing.
//
// When the session is not known, the rule is exactly the one above. That is
// a decision: every shell started before this variable existed, and every
// host that exports a harness and no session, is served as it was.
func lineageForCwd(ops hookOps, dir, cwd, harness, session string, now time.Time) (*lineageFile, string) {
	if dir == "" || cwd == "" || harness == "" {
		return nil, ""
	}
	at := filepath.Clean(cwd)
	for i := 0; i <= lineageWalkUp; i++ {
		path := lineagePath(dir, at)
		if sc, ok := loadLineage(ops, path); ok && (session == "" || sc.SessionID == session) {
			if now.Sub(sc.UpdatedAt) > lineageMaxAge {
				return nil, ""
			}
			if !sameHarness(sc.Harness, harness) {
				return nil, ""
			}
			return sc, path
		}
		parent := filepath.Dir(at)
		if parent == at {
			break
		}
		at = parent
	}
	return nil, ""
}

// envelope renders the sidecar as the trace `search` will send. The
// sequence counter is the sidecar's, bumped by the caller that saves it.
func (sc *lineageFile) envelope() *traceEnvelope {
	if sc == nil || sc.SessionID == "" {
		return nil
	}
	env := &traceEnvelope{
		V:               traceVersion,
		Harness:         sc.Harness,
		SessionID:       sc.SessionID,
		ParentSessionID: sc.ParentSessionID,
		TurnID:          sc.TurnID,
		CallID:          sc.CallID,
		Window:          sc.Window,
		Seq:             sc.Seq,
	}
	if len(sc.History) > 0 {
		env.History = append([]traceHistory(nil), sc.History...)
	}
	return env
}

// ── the installation's own directory ───────────────────────────────────

// installRoot is the installation's own directory: the parent of the state
// directory, which with the standard layout is ~/.jevlin. credentials.json,
// the registration journal, the claim record and the release check's stamp
// live there, outside every directory a sandboxed agent may write.
func installRoot(m config.Search) string { return filepath.Dir(m.StateDir) }
