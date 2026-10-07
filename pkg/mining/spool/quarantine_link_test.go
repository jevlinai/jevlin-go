package spool

import (
	"os"
	"path/filepath"
	"strings"
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

// The spool's default is <state_dir>/spool, inside the state dir, which is
// itself a writable root: a sandboxed command can rename the spool aside and
// leave a link, or another directory, at its name after the flush opened it.
// Every later operation must refuse the replacement.
func TestASpoolReplacedAfterItOpenedIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(t *testing.T, spoolDir, outside string)
	}{
		{"by a link out", func(t *testing.T, spoolDir, outside string) { symlinkOrFail(t, outside, spoolDir) }},
		{"by another directory", func(t *testing.T, spoolDir, _ string) {
			if err := os.Mkdir(spoolDir, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			spoolDir := filepath.Join(state, "spool")
			s, err := Open(spoolDir)
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Rename(spoolDir, spoolDir+".aside"); err != nil {
				t.Fatal(err)
			}
			tc.replace(t, spoolDir, outside)
			if err := s.Enqueue(record(t, 1)); err == nil {
				t.Fatal("Enqueue wrote into a spool directory replaced after it opened")
			}
			if _, err := s.Pending(); err == nil {
				t.Fatal("Pending listed a spool directory replaced after it opened")
			}
			requireEmpty(t, outside)
			if entries, _ := os.ReadDir(spoolDir); len(entries) != 0 {
				t.Fatalf("the replacement directory was written: %v", entries)
			}
		})
	}
}

// The quarantine is listed through the root: one replaced by a link to a
// directory of .json files is not counted as quarantined evidence.
func TestAQuarantineThatLinksOutIsNeverListed(t *testing.T) {
	s := newSpool(t)
	outside := t.TempDir()
	for _, n := range []string{"a.json", "b.json", "c.json"} {
		if err := os.WriteFile(filepath.Join(outside, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(s.quarantine); err != nil {
		t.Fatal(err)
	}
	symlinkOrFail(t, outside, s.quarantine)
	if n, err := s.CountQuarantined(); err == nil && n != 0 {
		t.Fatalf("CountQuarantined counted %d files outside the spool", n)
	}
}

// A spool_dir that is itself a link is refused, with a message that says
// what to do: following it would let a link left at a nested spool's name
// redirect the spool before it ever opened.
func TestASpoolDirThatIsALinkIsRefusedWithWhatToDo(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "spool")
	symlinkOrFail(t, real, link)
	_, err := Open(link)
	if err == nil {
		t.Fatal("a spool_dir that is a link was opened")
	}
	if !strings.Contains(err.Error(), "name the real directory") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
}
