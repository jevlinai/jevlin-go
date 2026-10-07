package fsx

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// writableRoot is a directory standing in for a sandbox's writable root, and
// canary a file outside it that nothing here may change.
func writableRoot(t *testing.T) (root, canary string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	canary = filepath.Join(base, "canary")
	if err := os.WriteFile(canary, []byte("the participant's file"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, canary
}

func requireCanary(t *testing.T, canary string) {
	t.Helper()
	got, err := os.ReadFile(canary) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatalf("canary: %v", err)
	}
	if string(got) != "the participant's file" {
		t.Fatalf("the file outside the root was changed: %q", got)
	}
}

// symlinkOrSkip makes a symlink, and on a system that will not let this
// account make one, skips — except under CI=true, where a guard that did not
// run must not read as one that passed.
func symlinkOrSkip(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
		}
		t.Skipf("cannot make a symlink here: %v", err)
	}
}

func TestCreateNewWritesANewFile(t *testing.T) {
	root, _ := writableRoot(t)
	path := filepath.Join(root, "new")
	if err := CreateNew(path, []byte("ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- the test's own file
	if err != nil || string(got) != "ours" {
		t.Fatalf("read back %q, %v", got, err)
	}
}

// Whatever a sandboxed command left at the name, CreateNew opens none of it
// for writing: it fails with fs.ErrExist and the file outside is untouched.
func TestCreateNewNeverWritesThroughWhatIsAtTheName(t *testing.T) {
	plants := map[string]func(t *testing.T, canary, name string){
		"a regular file": func(t *testing.T, _, name string) {
			if err := os.WriteFile(name, []byte("planted"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a symlink out of the root": func(t *testing.T, canary, name string) { symlinkOrSkip(t, canary, name) },
		"a dangling symlink": func(t *testing.T, canary, name string) {
			symlinkOrSkip(t, canary+"-absent", name)
		},
		"a hard link to a file outside the root": func(t *testing.T, canary, name string) {
			if err := os.Link(canary, name); err != nil {
				t.Fatal(err)
			}
		},
	}
	for what, plant := range plants {
		t.Run(what, func(t *testing.T) {
			root, canary := writableRoot(t)
			name := filepath.Join(root, "x.tmp")
			plant(t, canary, name)
			err := CreateNew(name, []byte("ours"), 0o600)
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("CreateNew over %s: %v, want fs.ErrExist", what, err)
			}
			requireCanary(t, canary)
			if _, err := os.Stat(canary + "-absent"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a dangling link's target was created: %v", err)
			}
		})
	}
}

func TestReadRegularReadsAndHoldsItsBound(t *testing.T) {
	root, _ := writableRoot(t)
	path := filepath.Join(root, "r")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRegular(path, 5); err != nil || string(got) != "12345" {
		t.Fatalf("at the bound: %q, %v", got, err)
	}
	if _, err := ReadRegular(path, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the bound: %v, want ErrTooLarge", err)
	}
	if _, err := ReadRegular(root, 1<<20); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a directory: %v, want ErrNotRegular", err)
	}
}

func TestOpenLockCreatesAndRefusesALink(t *testing.T) {
	root, canary := writableRoot(t)
	path := filepath.Join(root, "x.lock")
	f, err := OpenLock(path)
	if err != nil {
		t.Fatalf("absent lock: %v", err)
	}
	_ = f.Close()

	linked := filepath.Join(root, "linked.lock")
	symlinkOrSkip(t, canary, linked)
	if f, err := OpenLock(linked); err == nil {
		_ = f.Close()
		t.Fatal("a symlink at the lock's name was opened")
	}
	requireCanary(t, canary)
}

// A hard link at a lock's name is opened and locked, but nothing is written:
// O_TRUNC is never set.
func TestOpenLockNeverWritesAHardLinkedFile(t *testing.T) {
	root, canary := writableRoot(t)
	path := filepath.Join(root, "x.lock")
	if err := os.Link(canary, path); err != nil {
		t.Fatal(err)
	}
	f, err := OpenLock(path)
	if err == nil {
		_ = f.Close()
	}
	requireCanary(t, canary)
}

func TestOpenRootRefusesASymlinkedRoot(t *testing.T) {
	root, _ := writableRoot(t)
	elsewhere := t.TempDir()
	link := filepath.Join(filepath.Dir(root), "linked-root")
	symlinkOrSkip(t, elsewhere, link)
	if r, err := OpenRoot(link); err == nil {
		_ = r.Close()
		t.Fatal("a root spelled through a symlink was opened")
	}
	r, err := OpenRoot(root)
	if err != nil {
		t.Fatalf("a real root: %v", err)
	}
	_ = r.Close()
}

// The spool's quarantine is the one directory below a root: a sandboxed
// command can replace it with a link out of the root, and a move into it
// must then fail rather than land outside.
func TestRootRefusesASubdirectoryReplacedByALinkOut(t *testing.T) {
	root, _ := writableRoot(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rec.json"), []byte("record"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "quarantine"))

	r, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err := r.MkdirAll("quarantine", 0o700); err == nil {
		t.Error("MkdirAll accepted a subdirectory that is a link out of the root")
	}
	if _, err := r.ReadDir("quarantine"); err == nil {
		t.Error("ReadDir listed a directory outside the root")
	}
	if err := r.MoveDurable("rec.json", filepath.Join("quarantine", "rec.json")); err == nil {
		t.Error("MoveDurable moved a record through a link out of the root")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("something reached the directory outside the root: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "rec.json")); err != nil {
		t.Fatalf("the record left its place: %v", err)
	}
}

func TestRootMovesIntoARealSubdirectory(t *testing.T) {
	root, _ := writableRoot(t)
	if err := os.WriteFile(filepath.Join(root, "rec.json"), []byte("record"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err := r.MkdirAll("quarantine", 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join("quarantine", "rec.json")
	if err := r.MoveDurable("rec.json", dest); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadRegular(dest, 64)
	if err != nil || !bytes.Equal(got, []byte("record")) {
		t.Fatalf("read back %q, %v", got, err)
	}
	entries, err := r.ReadDir("quarantine")
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine lists %v, %v", entries, err)
	}
}
