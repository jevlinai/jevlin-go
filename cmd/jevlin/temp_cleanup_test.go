package main

// A failed rename cleans up after itself, and old leftovers are swept (dropin-miner#100).
//
// Three writers go through replaceViaTemp — the lineage files, the window
// state, the flush stamp — so each is driven through its own entry point:
// a guard on the helper alone would not notice a call site that stopped
// using it.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func tempsIn(files map[string][]byte) []string {
	var out []string
	for p := range files {
		if strings.HasSuffix(p, ".tmp") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func TestAFailedLineageRenameLeavesNoTempAndTheFileAsItWas(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	path := lineagePath("/sessions", "/home/u/project")
	if err := saveLineage(ops, path, &lineageFile{Harness: "cursor", SessionID: "s", Seq: 3}, ops.now()); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), fs.files[path]...)

	fs.forceRenameErr = errors.New("The process cannot access the file because it is being used by another process.")
	err := updateLineage(ops, path, ops.now(), func(l *lineageFile) { l.Seq++ })

	if err == nil {
		t.Fatal("the rename failed and the write reported success")
	}
	if left := tempsIn(fs.files); len(left) != 0 {
		t.Errorf("a failed rename left its temporary file behind: %v", left)
	}
	if !bytes.Equal(fs.files[path], before) {
		t.Errorf("the lineage file changed under a failed write:\n got %s\nwant %s", fs.files[path], before)
	}
}

func TestAFailedWindowStateRenameLeavesNoTemp(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	hc := hookContext{sessionsDir: "/sessions"}
	hookWindow(ops, hc, "session-start", []byte(`{"session_id":"s"}`))
	state := filepath.Join("/sessions", hookStateFile)
	before := append([]byte(nil), fs.files[state]...)
	if len(before) == 0 {
		t.Fatal("the control wrote no window state")
	}

	fs.forceRenameErr = errors.New("sharing violation")
	hookWindow(ops, hc, "pre-compact", []byte(`{"session_id":"s"}`))

	if left := tempsIn(fs.files); len(left) != 0 {
		t.Errorf("a failed rename left its temporary file behind: %v", left)
	}
	if !bytes.Equal(fs.files[state], before) {
		t.Errorf("the window state changed under a failed write")
	}
}

// A write that fails halfway has made a temporary file too.
func TestAFailedTempWriteIsRemovedAsWell(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	ops.createNew = func(p string, b []byte, _ os.FileMode) error {
		fs.files[p] = b[:len(b)/2]
		return errors.New("disk full")
	}
	if err := saveLineage(ops, lineagePath("/sessions", "/w"), &lineageFile{SessionID: "s"}, ops.now()); err == nil {
		t.Fatal("the write failed and was reported as success")
	}
	if len(fs.files) != 0 {
		t.Errorf("a half-written temporary file stayed: %v", keys(fs.files))
	}
}

// The flush stamp writes through the operating system directly, so it is
// driven on a real disk. A rename onto a non-empty directory fails on every
// OS this client ships for, which is the forced error.
func TestAFailedFlushStampRenameLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flush.json")
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFlushStamp(path, flushStamp{TargetEpoch: 9}); err == nil {
		t.Fatal("renaming onto a non-empty directory succeeded; the forced error did not happen and this test proves nothing")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("a failed rename left its temporary file behind: %v", left)
	}
}

// The same on a real disk for the lineage writer, so os.Remove and the real
// directory listing are what ran, not the fake's.
func TestOnARealDiskAFailedLineageRenameLeavesNoTempAndOldOnesAreSwept(t *testing.T) {
	dir := t.TempDir()
	ops := realHookOps()
	now := time.Now()
	old := now.Add(-lineageMaxAge - time.Hour)

	staleForeign := filepath.Join(dir, strings.Repeat("a", 32)+".json.999999.tmp")
	freshForeign := filepath.Join(dir, strings.Repeat("b", 32)+".json.999998.tmp")
	for _, p := range []string{staleForeign, freshForeign} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(staleForeign, old, old); err != nil {
		t.Fatal(err)
	}

	path := lineagePath(dir, "/w")
	if err := saveLineage(ops, path, &lineageFile{SessionID: "s"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleForeign); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a temporary file older than a session was not swept (stat: %v)", err)
	}
	if _, err := os.Stat(freshForeign); err != nil {
		t.Errorf("a fresh temporary file — another process's write in flight — was swept: %v", err)
	}

	blocked := lineagePath(dir, "/blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveLineage(ops, blocked, &lineageFile{SessionID: "s"}, now); err == nil {
		t.Fatal("the forced rename error did not happen")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) != 1 || left[0] != freshForeign {
		t.Errorf("temporary files after a failed rename: %v, want only the fresh foreign one", left)
	}
}

// What the sweep may take, enumerated. Every row is one file beside the one
// being written; only the first kind goes.
func TestTheSweepTakesOnlyOldForeignTempsOfItsOwnKind(t *testing.T) {
	const dir = "/sessions"
	hexA, hexB := strings.Repeat("a", 32)+".json", strings.Repeat("b", 32)+".json"
	type row struct {
		name  string
		age   time.Duration
		swept bool
	}
	lineageRows := []row{
		{hexA + ".7.tmp", lineageMaxAge + time.Minute, true},
		{hexA + ".7.tmp", lineageMaxAge - time.Minute, false},  // fresh: possibly a write in flight
		{hexB + ".42.tmp", lineageMaxAge + time.Minute, false}, // this process's own pid
		{hexB + ".8.tmp", -time.Hour, false},                   // modified in the future: a clock moved, not an old file
		// Further in the future than lineageMaxAge. The row above cannot tell
		// "not old" from "within twelve hours either way": a sweep that took
		// the DISTANCE from now would leave that one and take this one.
		{hexB + ".9.tmp", -13 * time.Hour, false},
		{"resume.json.7.tmp", lineageMaxAge + time.Minute, false}, // the right shape, not a lineage file
		{hexA + ".tmp", lineageMaxAge + time.Minute, false},       // no pid: not a name these writers produce
		{hexA + ".7.tmp.bak", lineageMaxAge + time.Minute, false},
		// The shape these writers now produce, "<file>.<pid>-<random>.tmp".
		{hexA + ".7-0a1b2c3d4e5f.tmp", lineageMaxAge + time.Minute, true},
		{hexA + ".7-0a1b2c3d4e5f.tmp", lineageMaxAge - time.Minute, false},
		{hexB + ".42-0a1b2c3d4e5f.tmp", lineageMaxAge + time.Minute, false},
		{hexA + ".7-NOTHEX.tmp", lineageMaxAge + time.Minute, false},
	}
	run := func(t *testing.T, rows []row, write func(hookOps)) {
		for _, r := range rows {
			fs, ops := newFakeHookOps(nil)
			p := filepath.Join(dir, r.name)
			fs.files[p] = []byte("{}")
			fs.mtimes[p] = ops.now().Add(-r.age)
			write(ops)
			if _, still := fs.files[p]; still == r.swept {
				t.Errorf("%s aged %s: swept=%v, want %v", r.name, r.age, !still, r.swept)
			}
		}
	}
	t.Run("a lineage write", func(t *testing.T) {
		run(t, lineageRows, func(ops hookOps) {
			if err := saveLineage(ops, lineagePath(dir, "/w"), &lineageFile{SessionID: "s"}, ops.now()); err != nil {
				t.Fatal(err)
			}
		})
	})
	// The window state can live outside the sessions directory (the plugin
	// root, TMPDIR), so it sweeps only its own leftovers — never a lineage
	// file's, and never anything else that happens to end in .tmp.
	t.Run("a window-state write", func(t *testing.T) {
		run(t, []row{
			{hookStateFile + ".7.tmp", lineageMaxAge + time.Minute, true},
			{hookStateFile + ".7.tmp", lineageMaxAge - time.Minute, false},
			{hookStateFile + ".7-0a1b2c3d4e5f.tmp", lineageMaxAge + time.Minute, true},
			{hookStateFile + ".42-0a1b2c3d4e5f.tmp", lineageMaxAge + time.Minute, false},
			{hexA + ".7.tmp", lineageMaxAge + time.Minute, false},
			{"somebody-elses.7.tmp", lineageMaxAge + time.Minute, false},
		}, func(ops hookOps) {
			hookWindow(ops, hookContext{sessionsDir: dir}, "session-start", []byte(`{"session_id":"s"}`))
		})
	})
}

// A sweep that cannot list or cannot remove costs nothing: the write goes
// through. It runs in front of a search.
func TestASweepThatFailsDoesNotFailTheWrite(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	ops.listTemps = func(string) ([]tempEntry, error) { return nil, errors.New("permission denied") }
	path := lineagePath("/sessions", "/w")
	if err := saveLineage(ops, path, &lineageFile{SessionID: "s"}, ops.now()); err != nil {
		t.Fatalf("the write failed because the sweep could not list: %v", err)
	}
	if _, ok := fs.files[path]; !ok {
		t.Error("nothing was written")
	}
}

// The temporary names sit in directories a sandboxed command can write (the
// state and sessions directories are Codex writable roots). A link planted at
// the name an older release would have used — "<file>.<pid>.tmp" — or at any
// name, must never be written through.
func plantLink(t *testing.T, link string) (target string) {
	t.Helper()
	target = filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	return target
}

func assertUntouched(t *testing.T, target string) {
	t.Helper()
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" { // #nosec G304 -- a test temp file
		t.Errorf("the file behind a planted link was written: %q, %v", b, err)
	}
}

func TestTempWritersNeverWriteThroughALinkAtThePredictableName(t *testing.T) {
	pid := os.Getpid()
	t.Run("flush stamp", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flush.json")
		target := plantLink(t, fmt.Sprintf("%s.%d.tmp", path, pid))
		if err := writeFlushStamp(path, flushStamp{TargetEpoch: 9}); err != nil {
			t.Fatal(err)
		}
		assertUntouched(t, target)
		if b, _ := os.ReadFile(path); !bytes.Contains(b, []byte(`"target_epoch":9`)) { // #nosec G304 -- a test temp file
			t.Errorf("the stamp was not written: %s", b)
		}
	})
	t.Run("lineage file", func(t *testing.T) {
		path := lineagePath(t.TempDir(), "/w")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		target := plantLink(t, fmt.Sprintf("%s.%d.tmp", path, pid))
		if err := saveLineage(realHookOps(), path, &lineageFile{SessionID: "s"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		assertUntouched(t, target)
	})
	t.Run("window state", func(t *testing.T) {
		dir := t.TempDir()
		target := plantLink(t, fmt.Sprintf("%s.%d.tmp", filepath.Join(dir, hookStateFile), pid))
		hookWindow(realHookOps(), hookContext{sessionsDir: dir}, "session-start", []byte(`{"session_id":"s"}`))
		assertUntouched(t, target)
		if _, err := os.Stat(filepath.Join(dir, hookStateFile)); err != nil {
			t.Errorf("the window state was not written: %v", err)
		}
	})
	t.Run("resume stamp", func(t *testing.T) {
		path := resumeStampPath(t.TempDir())
		target := plantLink(t, fmt.Sprintf("%s.%d.tmp", path, pid))
		if err := writeResumeStamp(path, resumeStamp{LastAttempt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		assertUntouched(t, target)
		if readResumeStamp(path).V != 1 {
			t.Error("the resume stamp was not written")
		}
	})
}

func TestCreateExclusiveRefusesALink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "x.tmp")
	target := plantLink(t, link)
	if err := createExclusive(link, []byte("overwritten"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("createExclusive over a link: %v, want fs.ErrExist", err)
	}
	assertUntouched(t, target)
}

// With no sessions directory and no plugin root, window state is not kept at
// all — never in the system temporary directory, which on Linux is /tmp,
// shared by every local user.
func TestWindowStateIsNeverKeptInTheSystemTempDir(t *testing.T) {
	fs, ops := newFakeHookOps(map[string]string{"TMPDIR": "/tmp"})
	if p := hookStatePath(ops, hookContext{}); p != "" {
		t.Fatalf("hookStatePath with nothing configured = %q, want none", p)
	}
	fs.files[filepath.Join(os.TempDir(), "jevlin-"+hookStateFile)] = []byte(`{"version":1,"sessions":{"s":{"generation":7}}}`)
	fs.files[filepath.Join("/tmp", "jevlin-"+hookStateFile)] = []byte(`{"version":1,"sessions":{"s":{"generation":7}}}`)
	before := len(fs.files)
	hookWindow(ops, hookContext{}, "pre-compact", []byte(`{"session_id":"s"}`))
	if len(fs.files) != before {
		t.Errorf("window state was written with nowhere private to keep it: %v", keys(fs.files))
	}
	if got := hookWindowID(ops, hookContext{}, "s"); got != "none" {
		t.Errorf("hookWindowID read a shared temp file: %q", got)
	}
}
