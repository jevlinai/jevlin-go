package main

// The sessions and state directories are writable roots of Codex's sandbox
// (pkg/fsx/confined.go): a sandboxed command can leave a symlink, a hard link
// to a file outside them, or a FIFO at any name, and the hook, the flush and
// the turn end run outside the sandbox. These tests plant each of those at
// the exact name a writer uses and hold the writer to two outcomes: the file
// outside is byte-identical afterwards, and nothing waits.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
