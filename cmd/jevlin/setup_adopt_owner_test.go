package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ownedByAnotherAccount makes the adoption owner check report every path at
// or under one of roots as another account's, the way it reads an object a
// second local user created; everything else is checked for real.
func ownedByAnotherAccount(t *testing.T, roots ...string) {
	t.Helper()
	orig := adoptionOwnerCheck
	adoptionOwnerCheck = func(path string, info fs.FileInfo) error {
		for _, r := range roots {
			if within(path, r) {
				return fmt.Errorf("%s is owned by another account", path)
			}
		}
		return orig(path, info)
	}
	t.Cleanup(func() { adoptionOwnerCheck = orig })
}

// The real check: what this process creates is its own, and a system
// directory is not.
func TestOwnedByCurrentUserReadsTheRealOwner(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "mine")
	if err := os.WriteFile(mine, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{mine, filepath.Dir(mine)} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := ownedByCurrentUser(path, info); err != nil {
			t.Errorf("%s, created by this process: %v", path, err)
		}
	}

	foreign := "/"
	if runtime.GOOS == "windows" {
		foreign = filepath.Join(os.Getenv("SystemRoot"), "System32") // owned by TrustedInstaller
	} else if os.Getuid() == 0 {
		t.Skip("running as root: / is this user's")
	}
	info, err := os.Lstat(foreign) // #nosec G703 -- a fixed system directory
	if err != nil {
		t.Fatal(err)
	}
	if err := ownedByCurrentUser(foreign, info); err == nil {
		t.Errorf("%s was accepted as the current user's", foreign)
	}
}

// A sibling another account owns is never offered, however new it is; the
// participant's own set-aside copy still is.
func TestTheSetAsideScanSkipsASiblingAnotherAccountOwns(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".jevlin")
	mine := filepath.Join(root, ".jevlin.bak-1")
	planted := filepath.Join(root, ".jevlin-old")
	writeInstallation(t, mine, "pending")
	writeInstallation(t, planted, "identity")
	newer := time.Now().Add(time.Hour)
	if err := os.Chtimes(planted, newer, newer); err != nil {
		t.Fatal(err)
	}
	if got := setAsideInstallation(home); got != planted {
		t.Fatalf("fixture: the newest sibling is not the one offered: got %q", got)
	}

	ownedByAnotherAccount(t, planted)
	if got := setAsideInstallation(home); got != mine {
		t.Errorf("got %q, want the participant's own %q", got, mine)
	}
	if err := os.RemoveAll(mine); err != nil {
		t.Fatal(err)
	}
	if got := setAsideInstallation(home); got != "" {
		t.Errorf("a sibling another account owns was offered: %q", got)
	}
}

// An object inside an accepted installation that another account owns fails
// the identity transaction: everything is rolled back, the source is exactly
// as it was, and connect does not run.
func TestAdoptionRefusesCustodyAnotherAccountOwns(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("state", "trace.key"),
		"state",
		credentialsFile,
		registrationJournalFile,
		claimRecordFile,
	} {
		t.Run(rel, func(t *testing.T) {
			s := newSetupSandbox(t)
			sibling := s.home + ".bak"
			writeInstallation(t, sibling, "identity", "pending", "claim", "sessions")
			ownedByAnotherAccount(t, filepath.Join(sibling, rel))
			before := snapshotTree(t, sibling)

			code, out, errOut := s.run(tty("y"), true, "-no-agents", "-no-profile")
			s.assertAdoptionStopped(t, code, out, errOut, identityBundle, sibling)
			if !strings.Contains(errOut, "owned by another account") {
				t.Errorf("the failure does not name the owner:\n%s", errOut)
			}
			s.assertCustodyAtSource(t, sibling, before)
			if lexists(filepath.Join(s.home, "sessions", "s-old.json")) {
				t.Error("the sessions were merged after the identity was refused")
			}
		})
	}
}

// A session file another account owns stays where it is; the participant's
// own records still merge.
func TestAdoptionLeavesARecordAnotherAccountOwns(t *testing.T) {
	s := newSetupSandbox(t)
	sibling := s.home + ".bak"
	writeInstallation(t, sibling, "identity", "sessions")
	planted := filepath.Join(sibling, "sessions", "s-planted.json")
	if err := os.WriteFile(planted, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ownedByAnotherAccount(t, planted)

	_, out, errOut := s.run(tty("y"), true, "-no-agents", "-no-profile")
	if !strings.Contains(out, "adopted sessions (1 moved, 1 not a regular file or directory this user owns") {
		t.Fatalf("the sessions merge did not refuse the planted record:\n%s\nstderr:\n%s", out, errOut)
	}
	if !lexists(planted) || lexists(filepath.Join(s.home, "sessions", "s-planted.json")) {
		t.Error("the planted record moved")
	}
	if !lexists(filepath.Join(s.home, "sessions", "s-old.json")) {
		t.Error("the participant's own record was not adopted")
	}
}

// The source is checked again when adoption runs: a directory that changed
// hands after it was offered moves nothing.
func TestAdoptionRefusesASourceAnotherAccountOwns(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, ".jevlin.bak"), filepath.Join(root, ".jevlin")
	writeInstallation(t, src, "identity", "sessions", "config")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the directory itself: its contents are the participant's, so
	// nothing but the check on the source catches it.
	orig := adoptionOwnerCheck
	adoptionOwnerCheck = func(path string, info fs.FileInfo) error {
		if path == src {
			return fmt.Errorf("%s is owned by another account", path)
		}
		return orig(path, info)
	}
	t.Cleanup(func() { adoptionOwnerCheck = orig })
	before := snapshotTree(t, root)
	a := &adoption{src: src, dst: dst, now: fixedSetupClock(), out: io.Discard, restrict: restrictToOwner}
	if err := a.run(); err == nil || !strings.Contains(err.Error(), "owned by another account") {
		t.Fatalf("run() = %v, want the owner refusal", err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) || len(a.moved) != 0 {
		t.Errorf("adoption moved something from a source another account owns: %v", a.moved)
	}
}
