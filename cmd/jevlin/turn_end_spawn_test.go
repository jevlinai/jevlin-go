package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The turn end is two processes: the Stop hook, which queues a file and
// starts the detached sender, and `turn-end`, which posts the file to the
// router and removes it. turn_end_test.go drives the hook over fake
// hookOps whose spawnTurnEnd is the test's own closure, so startTurnEnd —
// os.Executable, then spawnDetached with the sender's command line — was
// reached by no Go test, and a startTurnEnd that returned nil without
// spawning stayed green in all of them. The tests here go through the real
// entry, cmdHook with realHookOps, over a config loaded from disk, and
// stand at spawnTurnEndSender, the one seam below startTurnEnd.

// turnEndInstallation is one installation on the real disk: a format-1
// config naming a loopback router, with the turn end on or off and the
// daily release check off, so the real startBackground that follows a Stop
// spawns nothing of its own.
type turnEndInstallation struct {
	cfg, sessions string
	router        *turnEndRouter
}

// turnEndRouter records every request it receives.
type turnEndRouter struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []turnEndRequest
}

type turnEndRequest struct {
	method, path, auth string
	body               []byte
}

func (r *turnEndRouter) received() []turnEndRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turnEndRequest(nil), r.reqs...)
}

func newTurnEndInstallation(t *testing.T, enabled bool) *turnEndInstallation {
	t.Helper()
	router := &turnEndRouter{}
	router.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		router.mu.Lock()
		router.reqs = append(router.reqs, turnEndRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		router.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(router.srv.Close)

	root := t.TempDir()
	in := &turnEndInstallation{cfg: filepath.Join(root, "jevlin.toml"), sessions: filepath.Join(root, "sessions"), router: router}
	// The state directory exists, as setup leaves it; the sessions directory
	// is the hook's to make.
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	toml := `schema = 1

[search]
state_dir = "` + filepath.ToSlash(filepath.Join(root, "state")) + `"
sessions_dir = "` + filepath.ToSlash(in.sessions) + `"
turn_end = ` + map[bool]string{true: "true", false: "false"}[enabled] + `

[router]
url = "` + router.srv.URL + `"

[update]
mode = "off"
`
	if err := os.WriteFile(in.cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return in
}

// spawnAsk is one call the hook made for the detached sender: the command
// line, and whether the queued file was on disk at that moment.
type spawnAsk struct {
	exe    string
	args   []string
	onDisk bool
	body   []byte
}

// watchSpawns stands at spawnTurnEndSender for the rest of the test and
// returns where the calls land. The sender itself is not started: the file
// is left where the hook queued it, for the second half to take.
func watchSpawns(t *testing.T) *[]spawnAsk {
	t.Helper()
	var asks []spawnAsk
	saved := spawnTurnEndSender
	spawnTurnEndSender = func(exe string, args []string) error {
		ask := spawnAsk{exe: exe, args: append([]string(nil), args...)}
		if n := len(args); n > 0 {
			if b, err := os.ReadFile(args[n-1]); err == nil { // #nosec G304 -- the file the hook under test just queued, under t.TempDir
				ask.onDisk, ask.body = true, b
			}
		}
		asks = append(asks, ask)
		return nil
	}
	t.Cleanup(func() { spawnTurnEndSender = saved })
	return &asks
}

// stopHook runs the real Stop entry — cmdHook, realHookOps, the config
// loaded from disk — over a Claude Code Stop payload.
func stopHook(t *testing.T, in *turnEndInstallation, payload map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := cmdHook([]string{"-config", in.cfg, "stop"}, bytes.NewReader(b), &out, &errOut, noEnv); code != exitOK {
		t.Fatalf("hook stop exited %d\n%s", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("the Stop hook wrote to stdout: %q", out.String())
	}
}

// served leaves what `search` leaves after the router answered a search in
// the turn, through the real hook ops on the real sessions directory.
func (in *turnEndInstallation) served(t *testing.T, turn string) {
	t.Helper()
	markTurnSearched(realHookOps(), searchBlockOf(t, in.cfg), &traceEnvelope{TurnID: turn})
}

// queuedTurnEnds is every file under the sessions directory the sender
// would accept: in that directory, with the turn end's suffix.
func (in *turnEndInstallation) queuedTurnEnds(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(in.sessions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), turnEndSuffix) {
			files = append(files, filepath.Join(in.sessions, e.Name()))
		}
	}
	return files
}

// TestTheStopHookStartsTheDetachedSenderForAServedTurn: the real Stop
// entry, for a turn in which a search was served, asks for exactly one
// detached sender — this binary, with `turn-end -config <cfg> -file <file>`
// and nothing else — and the file it names is on disk, in the sessions
// directory under the sender's suffix, holding the turn's record, when it
// asks.
func TestTheStopHookStartsTheDetachedSenderForAServedTurn(t *testing.T) {
	in := newTurnEndInstallation(t, true)
	turn := traceHash("sess|p1")
	in.served(t, turn)
	asks := watchSpawns(t)

	stopHook(t, in, claudeStop(nil))

	if len(*asks) != 1 {
		t.Fatalf("the hook asked for %d detached sender(s), want 1: %+v", len(*asks), *asks)
	}
	ask := (*asks)[0]
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if ask.exe != exe {
		t.Errorf("the sender is %q, want this binary %q", ask.exe, exe)
	}
	if len(ask.args) != 5 {
		t.Fatalf("the sender's command line is %q, want turn-end -config <cfg> -file <file>", ask.args)
	}
	file := ask.args[4]
	if want := []string{"turn-end", "-config", in.cfg, "-file", file}; !reflect.DeepEqual(ask.args, want) {
		t.Errorf("the sender's command line is %q, want %q", ask.args, want)
	}
	if filepath.Dir(file) != filepath.Clean(in.sessions) || !strings.HasSuffix(file, turnEndSuffix) {
		t.Errorf("the queued file is %q, want one under %s ending in %s", file, in.sessions, turnEndSuffix)
	}
	if !ask.onDisk {
		t.Fatalf("the queued file %s was not on disk when the sender was asked for", file)
	}
	var rec turnEndRecord
	if err := json.Unmarshal(ask.body, &rec); err != nil {
		t.Fatalf("the queued file: %v", err)
	}
	if rec.SessionID != traceHash("sess") || rec.TurnID != turn || rec.Harness != "claude-code" || rec.Status != turnCompleted || rec.FinalText != "The MVP was Yang Eui-ji." {
		t.Errorf("the queued record = %+v", rec)
	}
	// The hook queued that file and no other, and the mark is taken: the
	// same Stop again asks for nothing.
	if files := in.queuedTurnEnds(t); !reflect.DeepEqual(files, []string{file}) {
		t.Errorf("queued files = %q, want exactly %q", files, file)
	}
	stopHook(t, in, claudeStop(nil))
	if len(*asks) != 1 {
		t.Errorf("a turn ended twice: %d sender(s) asked for", len(*asks))
	}
}

// TestTheStopHookStartsNoSenderUnlessTheInstallationOptedIn: the same
// served turn under `turn_end = false` queues nothing and asks for no
// sender; and under `turn_end = true` a turn in which no search was served
// does the same. Both through the real entry, because the fake ops the other
// tests use never reach what is being held here.
func TestTheStopHookStartsNoSenderUnlessTheInstallationOptedIn(t *testing.T) {
	t.Run("turn_end = false", func(t *testing.T) {
		in := newTurnEndInstallation(t, false)
		in.served(t, traceHash("sess|p1"))
		asks := watchSpawns(t)
		stopHook(t, in, claudeStop(nil))
		if len(*asks) != 0 {
			t.Errorf("an installation that did not opt in asked for a sender: %+v", *asks)
		}
		if files := in.queuedTurnEnds(t); len(files) != 0 {
			t.Errorf("an installation that did not opt in queued %q", files)
		}
	})
	t.Run("no search served", func(t *testing.T) {
		in := newTurnEndInstallation(t, true)
		asks := watchSpawns(t)
		stopHook(t, in, claudeStop(nil))
		if len(*asks) != 0 {
			t.Errorf("a turn that did not search asked for a sender: %+v", *asks)
		}
		if files := in.queuedTurnEnds(t); len(files) != 0 {
			t.Errorf("a turn that did not search queued %q", files)
		}
	})
}

// TestTheDetachedSenderPostsTheQueuedTurnEndOnceAndRemovesIt is the second
// half, on the file the first half queued: `turn-end -config <cfg> -file
// <file>`, as the hook would start it, sends that record to the configured
// router exactly once, under the stored key, and the file is gone; run
// again on the same command line it sends nothing. env_key_test.go and
// key_binding_test.go drive cmdTurnEnd too, on a hand-written queued file,
// for where the key may go; neither counts the requests or looks for the
// file afterwards.
func TestTheDetachedSenderPostsTheQueuedTurnEndOnceAndRemovesIt(t *testing.T) {
	in := newTurnEndInstallation(t, true)
	turn := traceHash("sess|p1")
	in.served(t, turn)
	asks := watchSpawns(t)
	stopHook(t, in, claudeStop(nil))
	if len(*asks) != 1 || len((*asks)[0].args) != 5 {
		t.Fatalf("the hook asked for %+v, want one sender", *asks)
	}
	storeKey(t, in.cfg, credentials{Router: origin(t, in.router.srv.URL)})

	// The sender's own command line, minus the subcommand the dispatcher
	// takes off.
	args := (*asks)[0].args[1:]
	file := args[len(args)-1]
	if code := cmdTurnEnd(args, noEnv); code != exitOK {
		t.Fatalf("turn-end exited %d", code)
	}
	reqs := in.router.received()
	if len(reqs) != 1 {
		t.Fatalf("the router received %d request(s), want 1", len(reqs))
	}
	if reqs[0].method != http.MethodPost || reqs[0].path != "/v1/turns" || reqs[0].auth != "Bearer "+boundKey {
		t.Errorf("request = %s %s with %q", reqs[0].method, reqs[0].path, reqs[0].auth)
	}
	var queued, sent turnEndRecord
	if err := json.Unmarshal((*asks)[0].body, &queued); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reqs[0].body, &sent); err != nil {
		t.Fatalf("the router received %q: %v", reqs[0].body, err)
	}
	if !reflect.DeepEqual(sent, queued) || sent.TurnID != turn {
		t.Errorf("the router received %+v, the hook queued %+v", sent, queued)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after the send, %s: %v, want gone", file, err)
	}
	if files := in.queuedTurnEnds(t); len(files) != 0 {
		t.Errorf("left queued: %q", files)
	}

	// The same command line again: the file is gone, and so is the send.
	if code := cmdTurnEnd(args, noEnv); code != exitOK {
		t.Fatalf("turn-end exited %d", code)
	}
	if n := len(in.router.received()); n != 1 {
		t.Errorf("the router received %d request(s) after a second run, want still 1", n)
	}
}
