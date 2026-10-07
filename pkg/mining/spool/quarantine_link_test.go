package spool

import (
	"os"
	"path/filepath"
	"testing"
)

// The spool directory is a writable root of Codex's sandbox when mining is
// on, and its quarantine is the one directory below it. A sandboxed command
// can replace the quarantine with a link to a directory outside, and plant a
// record of its own (its name and its bytes) for the next flush, run outside
// the sandbox, to move there. Nothing may reach the outside directory.

func symlinkOrFail(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
		}
		t.Skipf("cannot make a symlink here: %v", err)
	}
}

func requireEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the spool put something in the directory outside it: %v", entries)
	}
}

func TestOpenRefusesAQuarantineThatLinksOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	outside := t.TempDir()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrFail(t, outside, filepath.Join(dir, "quarantine"))
	if _, err := Open(dir); err == nil {
		t.Fatal("the spool opened with its quarantine linked out of it")
	}
	requireEmpty(t, outside)
}

// The quarantine is replaced after the spool opened, and a planted record
// that does not parse sends Pending to quarantine it.
func TestAPlantedRecordIsNeverMovedThroughAQuarantineThatLinksOut(t *testing.T) {
	s := newSpool(t)
	outside := t.TempDir()
	planted := filepath.Join(s.dir, "1-1-planted.json")
	if err := os.WriteFile(planted, []byte("not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.quarantine); err != nil {
		t.Fatal(err)
	}
	symlinkOrFail(t, outside, s.quarantine)
	if _, err := s.Pending(); err == nil {
		t.Fatal("Pending moved a record through a quarantine linked out of the spool")
	}
	requireEmpty(t, outside)
	if _, err := os.Stat(planted); err != nil {
		t.Fatalf("the planted record left the spool: %v", err)
	}
}
