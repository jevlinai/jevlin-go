package main

// Who makes the trace key, and what a search that cannot write the state
// directory does with it. A search with no hook sends an id keyed by
// state_dir/trace.key. Setup and connect run outside any sandbox and already
// write that directory, so they make the key; a search inside a sandbox that
// denies writes there can then only read it, and keeps one session id. Without
// that, such a search sends a different id every time, which is what the
// session id existed to prevent.

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// The hostname and parent pid fixedSearchOps stands in for.
const fixtureHostPpid = "fictional-host|4242"

// setupWithoutConnect runs setup with connect replaced by a connect that does
// nothing, so whatever lands in the state directory is setup's own.
func (s *setupSandbox) setupWithoutConnect(args ...string) (code int, stdout, stderr string) {
	s.t.Helper()
	var out, errOut bytes.Buffer
	d := s.deps(&ttyReader{}, &out, &errOut, false)
	d.connect = func([]string, io.Reader, io.Writer, io.Writer, func(string) string) int {
		s.connectCalls++
		return exitOK
	}
	code = setupMain(d, append([]string{"-yes", "-no-agents", "-no-profile"}, args...))
	return code, out.String(), errOut.String()
}

func requireTraceKey(t *testing.T, stateDir string) []byte {
	t.Helper()
	path := auth.TraceKeyPath(stateDir)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("no trace key: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is %v, want a regular file", path, info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("%s is %v, want 0600", path, info.Mode().Perm())
	}
	key, err := os.ReadFile(path) // #nosec G304 -- a path in this test's own sandbox
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("trace key is %d bytes, want 32", len(key))
	}
	return key
}

// Setup makes the key itself, not through connect: connect is replaced here,
// and a second run leaves the key as it found it.
func TestSetupMakesTheTraceKeyWithoutConnect(t *testing.T) {
	s := newSetupSandbox(t)
	state := filepath.Join(s.home, "state")

	code, out, errOut := s.setupWithoutConnect()
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	if s.connectCalls != 1 {
		t.Fatalf("connect ran %d times; the fixture means to replace it once", s.connectCalls)
	}
	first := requireTraceKey(t, state)

	code, out, errOut = s.setupWithoutConnect()
	if code != exitOK {
		t.Fatalf("second setup exited %d\n%s\n%s", code, out, errOut)
	}
	if again := requireTraceKey(t, state); !bytes.Equal(first, again) {
		t.Error("a second setup replaced the trace key")
	}
}

// Setup reports a change when it makes the key, so a rerun on an installation
// that predates the key does not claim everything was already in place. That
// holds only because setup makes it before connect does: connect finding a
// key setup had just made would leave setup believing it changed nothing.
func TestSetupDoesNotSayNothingChangedWhenItMakesTheTraceKey(t *testing.T) {
	const inPlace = "Everything setup looks after was already in place; nothing was changed."
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	key := auth.TraceKeyPath(filepath.Join(s.home, "state"))
	if code, out, errOut := s.run(tty("n"), true, "-yes"); code != exitOK {
		t.Fatalf("first setup exited %d\n%s\n%s", code, out, errOut)
	}
	if _, out, _ := s.run(tty(), true, "-yes"); !strings.Contains(out, inPlace) {
		t.Fatalf("a rerun with the key in place should have changed nothing:\n%s", out)
	}

	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := s.run(tty(), true, "-yes")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	requireTraceKey(t, filepath.Join(s.home, "state"))
	if strings.Contains(out, inPlace) {
		t.Errorf("setup made the trace key and still said it changed nothing:\n%s", out)
	}
}

// A dry run says where the key would go and makes nothing.
func TestSetupDryRunNamesTheTraceKeyAndMakesNone(t *testing.T) {
	s := newSetupSandbox(t)
	code, out, errOut := s.setupWithoutConnect("-dry-run")
	if code != exitOK {
		t.Fatalf("dry run exited %d\n%s\n%s", code, out, errOut)
	}
	want := "(dry run) would create " + auth.TraceKeyPath(filepath.Join(s.home, "state"))
	if !strings.Contains(out, want) {
		t.Errorf("dry run output missing %q:\n%s", want, out)
	}
	if lexists(auth.TraceKeyPath(filepath.Join(s.home, "state"))) {
		t.Error("a dry run made the trace key")
	}
}

// The key serves a stable id and nothing else, so a key setup cannot make is
// said, and setup goes on to connect, which stops on the problems that matter.
func TestSetupSaysSoAndGoesOnWhenItCannotMakeTheTraceKey(t *testing.T) {
	s := newSetupSandbox(t)
	state := filepath.Join(s.home, "state")
	// A directory where the key belongs is a key setup refuses to replace.
	if err := os.MkdirAll(auth.TraceKeyPath(state), 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := s.setupWithoutConnect()
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	if s.connectCalls != 1 {
		t.Errorf("connect ran %d times, want 1: a trace key must not stop setup", s.connectCalls)
	}
	if !strings.Contains(errOut, "could not make the trace key") || !strings.Contains(errOut, auth.TraceKeyPath(state)) {
		t.Errorf("setup did not say which key it could not make:\n%s", errOut)
	}
	if info, err := os.Lstat(auth.TraceKeyPath(state)); err != nil || !info.IsDir() {
		t.Errorf("setup replaced what it refused: %v %v", info, err)
	}
}

// Connect makes the key too, for an installation that was not set up through
// setup, in the foreground and in the detached resume a search spawns.
func TestConnectMakesTheTraceKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"foreground", nil},
		{"resume", []string{"-resume"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withShortConnectTimings(t)
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			if code, out, errOut := runConnect(t, cfgPath, nil, tc.args...); code != exitOK {
				t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
			}
			first := requireTraceKey(t, stateDir)
			if code, out, errOut := runConnect(t, cfgPath, nil, tc.args...); code != exitOK {
				t.Fatalf("second connect exited %d\n%s\n%s", code, out, errOut)
			}
			if again := requireTraceKey(t, stateDir); !bytes.Equal(first, again) {
				t.Error("a second connect replaced the trace key")
			}
		})
	}
}

// stateDirWritesDenied is the self-proof of the sandbox fixture: a file cannot
// be made in the directory, which is the condition a sandboxed search meets.
func stateDirWritesDenied(state string) error {
	probe := filepath.Join(state, ".sandbox-probe")
	if err := os.WriteFile(probe, nil, 0o600); err == nil { // #nosec G703 -- a path in this test's own sandbox
		_ = os.Remove(probe) // #nosec G703 -- the same path
		return errors.New("a write to the state directory succeeded")
	} else if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}

// The continuity this exists for: the key setup made is what a search reads
// when it cannot write the state directory, so every search from one shell
// sends the same id, the keyed hash of host|ppid. The same directory without
// the key is the contrast, and the proof that the fixture denies creation: a
// search there sends a different id each time.
func TestASearchThatCannotWriteTheStateDirKeepsTheSessionIDSetupGave(t *testing.T) {
	requireSandboxEmulation(t)
	for _, tc := range []struct {
		name   string
		keyed  bool
		stable bool
	}{
		{"setup made the key", true, true},
		{"no key, as before setup made one", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setup's config takes only an https router, so the search is
			// answered by a transport instead of a loopback server.
			var sent []http.Header
			useTransport(t, &recordingTransport{fn: func(_ int, req *http.Request) (*http.Response, error) {
				sent = append(sent, req.Header.Clone())
				return fakeResponse(http.StatusOK, routerBody, nil), nil
			}})
			s := newSetupSandbox(t)
			if code, out, errOut := s.setupWithoutConnect(); code != exitOK {
				t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
			}
			state := filepath.Join(s.home, "state")
			key := requireTraceKey(t, state)
			if !tc.keyed {
				if err := os.Remove(auth.TraceKeyPath(state)); err != nil {
					t.Fatal(err)
				}
			}
			files := []string{}
			if tc.keyed {
				files = append(files, auth.TraceKeyPath(state))
			}
			denyWritesKeepReads(t, state, files...)
			if err := stateDirWritesDenied(state); err != nil {
				t.Fatalf("the fixture does not emulate a sandbox that denies the state directory: %v", err)
			}

			ids := make([]string, 2)
			for i := range ids {
				h := fixedSearchOps(s.root)
				h.ops.traceKey = realSearchOps().traceKey
				code, out, errOut := runSearch(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, "-config", s.cfgPath(), "-no-flush", "q")
				if code != exitOK {
					t.Fatalf("search %d exited %d\n%s\n%s", i, code, out, errOut)
				}
				if len(sent) != i+1 {
					t.Fatalf("after search %d the router has been sent %d requests", i, len(sent))
				}
				if ids[i] = sent[i].Get("X-Session-Id"); ids[i] == "" {
					t.Fatalf("search %d sent no X-Session-Id", i)
				}
			}
			if tc.stable {
				want := traceKeyedHash(key, fixtureHostPpid)
				if ids[0] != want || ids[1] != want {
					t.Errorf("session ids %q and %q, want both %q (the keyed hash of host|ppid)", ids[0], ids[1], want)
				}
				return
			}
			if ids[0] == ids[1] {
				t.Errorf("with no key and no way to make one, both searches sent %q; the fallback is a one-off id", ids[0])
			}
		})
	}
}
