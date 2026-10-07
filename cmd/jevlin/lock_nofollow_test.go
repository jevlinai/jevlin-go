package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// lockOpeners are the two lock helpers, each reduced to "did it take the lock".
var lockOpeners = map[string]func(string) (bool, error){
	"tryLockFile": func(path string) (bool, error) {
		f, held, err := tryLockFile(path)
		if held {
			_ = unlockFile(f)
		}
		return held, err
	},
	"tryFlushLock": func(path string) (bool, error) {
		f, held, _, err := tryFlushLock(path)
		if held {
			_ = unlockFile(f)
		}
		return held, err
	},
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
}

// A lock path planted as a symlink to a file that does not exist must not
// create that file: the open neither follows the link nor takes a lock.
func TestLockRefusesASymlinkToAMissingTarget(t *testing.T) {
	for name, open := range lockOpeners {
		t.Run(name, func(t *testing.T) {
			stateDir, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "created-by-follow")
			lock := filepath.Join(stateDir, "connect.lock")
			symlinkOrSkip(t, target, lock)

			held, err := open(lock)
			if !errors.Is(err, errLockNotRegular) {
				t.Fatalf("err = %v, want errLockNotRegular", err)
			}
			if held {
				t.Fatal("took a lock through a symlink")
			}
			if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the link target was created (lstat err %v)", err)
			}
		})
	}
}

// A lock path planted as a symlink to an existing file must not lock that
// file: afterwards the target is still free to lock directly.
func TestLockRefusesASymlinkToAnExistingFile(t *testing.T) {
	for name, open := range lockOpeners {
		t.Run(name, func(t *testing.T) {
			stateDir, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "user-file")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(stateDir, "connect.lock")
			symlinkOrSkip(t, target, lock)

			held, err := open(lock)
			if !errors.Is(err, errLockNotRegular) || held {
				t.Fatalf("held %v err %v, want a refusal with errLockNotRegular", held, err)
			}
			if b, err := os.ReadFile(target); err != nil || string(b) != "keep" { // #nosec G304 -- test temp dir
				t.Fatalf("target changed: %q %v", b, err)
			}
		})
	}
}

// A directory at the lock path is refused on both helpers.
func TestLockRefusesADirectory(t *testing.T) {
	for name, open := range lockOpeners {
		t.Run(name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "connect.lock")
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			if held, err := open(lock); err == nil || held {
				t.Fatalf("held %v err %v, want a refusal", held, err)
			}
		})
	}
}

// A regular file, existing or absent, still locks.
func TestLockStillTakesARegularFile(t *testing.T) {
	for name, open := range lockOpeners {
		t.Run(name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "connect.lock")
			for _, attempt := range []string{"absent", "existing"} {
				if held, err := open(lock); err != nil || !held {
					t.Fatalf("%s: held %v err %v", attempt, held, err)
				}
			}
		})
	}
}
