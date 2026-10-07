package main

// The sessions and state directories are writable roots of Codex's sandbox
// (pkg/fsx/confined.go): a sandboxed command can leave a symlink, a hard link
// to a file outside them, or a FIFO at any name, and the hook, the flush and
// the turn end run outside the sandbox. These tests plant each of those at
// the exact name a writer uses and hold the writer to two outcomes: the file
// outside is byte-identical afterwards, and nothing waits.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

const canaryText = "the participant's own file"

// rootAndCanary is a stand-in writable root and a file outside it.
func rootAndCanary(t *testing.T) (dir, canary string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "sessions")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canary = filepath.Join(base, "outside.txt")
	if err := os.WriteFile(canary, []byte(canaryText), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, canary
}

func requireCanaryIntact(t *testing.T, canary string) {
	t.Helper()
	got, err := os.ReadFile(canary) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatalf("canary: %v", err)
	}
	if string(got) != canaryText {
		t.Fatalf("a write went through a planted link to the file outside the root: %q", got)
	}
}

// plantedLinks are what a sandboxed command can leave at a name and what
// this process must not write through.
func plantedLinks() map[string]func(canary, name string) error {
	return map[string]func(canary, name string) error{
		"hard link": func(canary, name string) error { return os.Link(canary, name) },
		"symlink":   func(canary, name string) error { return os.Symlink(canary, name) },
	}
}

// fixedSuffixOps is the real hook operations with the temporary name made
// predictable, so a test can plant at the exact name a writer will use.
func fixedSuffixOps() hookOps {
	ops := realHookOps()
	ops.tempSuffix = func() string { return "feedfacefeedface" }
	ops.spawnFlush = func(string) error { return nil }
	ops.spawnTurnEnd = func(string, string) error { return nil }
	return ops
}

func plantOrSkip(t *testing.T, kind string, plant func(canary, name string) error, canary, name string) {
	t.Helper()
	if err := plant(canary, name); err != nil {
		skipPermissionTest(t, "cannot plant a "+kind+" here: "+err.Error())
	}
}

// replaceViaTemp writes the lineage files, the window state and the flush
// stamp. Its temporary name is now unpredictable, but even at the exact name
// it never opens what is there.
func TestReplaceViaTempNeverWritesThroughALinkAtItsTemporaryName(t *testing.T) {
	for kind, plant := range plantedLinks() {
		t.Run(kind, func(t *testing.T) {
			dir, canary := rootAndCanary(t)
			ops := fixedSuffixOps()
			path := filepath.Join(dir, "0123456789abcdef0123456789abcdef.json")
			plantOrSkip(t, kind, plant, canary, tempNameFor(ops, path))
			err := replaceViaTemp(ops, path, []byte(`{"v":1}`), time.Now(), true)
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("replaceViaTemp over a %s at its temporary name: %v, want fs.ErrExist", kind, err)
			}
			requireCanaryIntact(t, canary)
		})
	}
}

// The name it chooses is not the one earlier versions used, which a
// sandboxed command could compute from the pid.
func TestReplaceViaTempUsesAnUnpredictableName(t *testing.T) {
	ops := realHookOps()
	a, b := tempNameFor(ops, "/x/f.json"), tempNameFor(ops, "/x/f.json")
	if a == b {
		t.Fatalf("two temporary names for one file are the same: %s", a)
	}
	if m := tempNameRe.FindStringSubmatch(filepath.Base(a)); m == nil || m[1] != "f.json" {
		t.Fatalf("the sweep would not recognize %s", a)
	}
	// Different is not enough: a counter differs too, and a sandboxed
	// command can guess a counter's next value. The random part is 128 bits.
	if !regexp.MustCompile(`\.\d+-[0-9a-f]{32}\.tmp$`).MatchString(a) {
		t.Fatalf("the temporary name %s does not carry 32 random hex digits", a)
	}
}

func TestMarkTurnSearchedNeverWritesThroughALinkAtTheMark(t *testing.T) {
	for kind, plant := range plantedLinks() {
		t.Run(kind, func(t *testing.T) {
			dir, canary := rootAndCanary(t)
			ops := fixedSuffixOps()
			plantOrSkip(t, kind, plant, canary, turnSearchedPath(dir, "turn-1"))
			markTurnSearched(ops, config.Miner{TurnEnd: true, SessionsDir: dir}, &traceEnvelope{TurnID: "turn-1"})
			requireCanaryIntact(t, canary)
		})
	}
}

// The queued turn end is written where a sandboxed command can neither
// compute the name nor write through one it guessed. A link at the name
// earlier versions used (computable from the lineage file) no longer stops
// the turn end; one at the exact name, which the fixed suffix lets the test
// know, stops it without being written through.
func TestQueueTurnEndNeverWritesThroughALinkAtItsName(t *testing.T) {
	for kind, plant := range plantedLinks() {
		hash := traceHash("turn-end|s|t")
		for _, tc := range []struct {
			name    string
			planted string
			queued  bool
		}{
			{"at the name earlier versions used", hash + turnEndSuffix, true},
			{"at the exact name", hash + ".feedfacefeedface" + turnEndSuffix, false},
		} {
			t.Run(kind+" "+tc.name, func(t *testing.T) {
				dir, canary := rootAndCanary(t)
				ops := fixedSuffixOps()
				var spawned string
				ops.spawnTurnEnd = func(_, file string) error { spawned = file; return nil }
				plantOrSkip(t, kind, plant, canary, filepath.Join(dir, tc.planted))
				queueTurnEnd(ops, hookContext{sessionsDir: dir}, turnEndRecord{SessionID: "s", TurnID: "t"})
				requireCanaryIntact(t, canary)
				switch {
				case tc.queued && spawned == "":
					t.Fatal("a link at a computable name stopped the turn end from being queued")
				case !tc.queued && spawned != "":
					t.Fatalf("a sender was started for %s, which this process did not write", spawned)
				}
			})
		}
	}
}

// With no sessions directory and no plugin root the window state is not
// kept, rather than kept in a temporary directory every account shares.
func TestWindowStateIsNeverKeptInTheSharedTemporaryDirectory(t *testing.T) {
	ops := realHookOps()
	ops.getenv = func(k string) string {
		if k == "TMPDIR" {
			return t.TempDir()
		}
		return ""
	}
	if p := hookStatePath(ops, hookContext{}); p != "" {
		t.Fatalf("window state path with no sessions directory: %q, want none", p)
	}
	if id := hookWindowID(ops, hookContext{}, "s"); id != "none" {
		t.Fatalf("window id with no state kept: %q, want none", id)
	}
}

// connect.lock lives in the state dir. A symlink a sandboxed command left
// there is refused rather than followed to a file outside; a hard link is
// locked, which writes nothing.
func TestConnectLockNeverFollowsOrWritesALinkAtItsName(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir, canary := rootAndCanary(t)
		plantOrSkip(t, "symlink", plantedLinks()["symlink"], canary, connectLockPath(dir))
		f, ok, err := tryLockFile(connectLockPath(dir))
		if f != nil {
			_ = f.Close()
		}
		if ok || err == nil {
			t.Fatalf("connect.lock as a symlink out of the state dir was taken (ok=%t, err=%v)", ok, err)
		}
		requireCanaryIntact(t, canary)
	})
	t.Run("hard link", func(t *testing.T) {
		dir, canary := rootAndCanary(t)
		plantOrSkip(t, "hard link", plantedLinks()["hard link"], canary, connectLockPath(dir))
		if f, _, _ := tryLockFile(connectLockPath(dir)); f != nil {
			_ = f.Close()
		}
		requireCanaryIntact(t, canary)
	})
}

// The resume stamp used "<path>.<pid>.tmp", a name a sandboxed command could
// compute for the next connect -resume; fsx now stages it under a random
// name it creates exclusively.
func TestResumeStampNeverWritesThroughALinkAtItsOldTemporaryName(t *testing.T) {
	for kind, plant := range plantedLinks() {
		t.Run(kind, func(t *testing.T) {
			dir, canary := rootAndCanary(t)
			path := resumeStampPath(dir)
			plantOrSkip(t, kind, plant, canary, fmt.Sprintf("%s.%d.tmp", path, os.Getpid()))
			if err := writeResumeStamp(path, resumeStamp{LastAttempt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			requireCanaryIntact(t, canary)
			if st := readResumeStamp(path); st.V != 1 {
				t.Fatalf("the stamp was not written: %+v", st)
			}
		})
	}
}

// flush.lock sits in the installation's own directory, which a layout that
// nests it in a writable root would expose (agents install refuses that
// layout, and this holds either way): a link at the name is refused, and a
// dangling one does not create its target.
func TestFlushLockNeverFollowsALinkAtItsName(t *testing.T) {
	dir, canary := rootAndCanary(t)
	path := filepath.Join(dir, "flush.lock")
	plantOrSkip(t, "symlink", plantedLinks()["symlink"], canary+"-absent", path)
	f, held, _, err := tryFlushLock(path)
	if f != nil {
		_ = f.Close()
	}
	if held || err == nil {
		t.Fatalf("flush.lock as a link was taken (held=%t, err=%v)", held, err)
	}
	if _, err := os.Lstat(canary + "-absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the link's target was created: %v", err)
	}
	requireCanaryIntact(t, canary)
}

// readCredentials checks the name, then reads: a link swapped in between,
// at exactly that moment, must not be read.
func TestReadCredentialsReadsTheFileItChecked(t *testing.T) {
	dir, _ := rootAndCanary(t)
	outside := filepath.Join(filepath.Dir(dir), "outside-credentials.json")
	if err := os.WriteFile(outside, []byte(`{"v":1,"api_key":"sr-not-this-installations"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, credentialsFile)
	if err := os.WriteFile(path, []byte(`{"v":1,"api_key":"sr-ours"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialsCheckedHook = func(p string) {
		_ = os.Remove(p) // #nosec G703 -- the test's own temporary file
		if err := os.Symlink(outside, p); err != nil {
			t.Errorf("swap: %v", err)
		}
	}
	t.Cleanup(func() { credentialsCheckedHook = nil })
	c, err := readCredentials(path)
	if err == nil {
		t.Fatalf("read the credentials of a link swapped in after the checks: key %q", c.APIKey)
	}
}

// overHookBound is a file size just past hookFileMaxBytes as this client sets
// it. It is a literal, not the constant plus one, so a change that raises the
// bound shows here as a failure instead of growing the test with it.
const overHookBound = 17 << 20

// A window state the hook cannot read (here, past its bound) is left as it
// is: rewriting it from a fresh state would erase every other session's
// window. And a lineage file past the hook's bound is not read at all.
func TestTheHookNeverRewritesAWindowStateItCouldNotRead(t *testing.T) {
	dir, _ := rootAndCanary(t)
	ops := fixedSuffixOps()
	path := filepath.Join(dir, hookStateFile)
	big := make([]byte, overHookBound)
	for i := range big {
		big[i] = ' '
	}
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	hookWindow(ops, hookContext{sessionsDir: dir}, "session-start", []byte(`{"session_id":"s"}`))
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(len(big)) {
		t.Fatalf("the unreadable window state was rewritten: size %v, %v", info.Size(), err)
	}
}

func TestALineageFilePastTheHooksBoundIsNotRead(t *testing.T) {
	dir, _ := rootAndCanary(t)
	path := lineagePath(dir, "/w")
	doc := `{"v":1,"session_id":"s","history":[{"role":"assistant","text":"` + strings.Repeat("x", overHookBound) + `"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadLineage(fixedSuffixOps(), path); ok {
		t.Fatal("a lineage file past hookFileMaxBytes was read")
	}
}
