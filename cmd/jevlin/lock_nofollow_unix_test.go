//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO planted at the lock path is refused rather than opened: without
// O_NONBLOCK the read-only fallback would block forever on it.
func TestLockRefusesAFIFO(t *testing.T) {
	for name, open := range lockOpeners {
		t.Run(name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "flush.lock")
			if err := syscall.Mkfifo(lock, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			if held, err := open(lock); !errors.Is(err, errLockNotRegular) || held {
				t.Fatalf("held %v err %v, want a refusal with errLockNotRegular", held, err)
			}
		})
	}
}

// The flush lock's read-only fallback refuses a FIFO too.
func TestFlushLockReadOnlyFallbackRefusesAFIFO(t *testing.T) {
	if why := sandboxEmulationUnavailable(); why != "" {
		t.Skip(why)
	}
	lock := filepath.Join(t.TempDir(), "flush.lock")
	if err := syscall.Mkfifo(lock, 0o400); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	f, held, mode, err := tryFlushLock(lock)
	if held {
		_ = unlockFile(f)
	}
	if !errors.Is(err, errLockNotRegular) || held || mode != flushLockReadOnly {
		t.Fatalf("held %v mode %v err %v, want a read-only refusal with errLockNotRegular", held, mode, err)
	}
}

// The read-only fallback on a symlink is refused, not followed.
func TestFlushLockReadOnlyFallbackRefusesASymlink(t *testing.T) {
	if why := sandboxEmulationUnavailable(); why != "" {
		t.Skip(why)
	}
	target := filepath.Join(t.TempDir(), "user-file")
	if err := os.WriteFile(target, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(t.TempDir(), "flush.lock")
	symlinkOrSkip(t, target, lock)
	f, held, _, err := tryFlushLock(lock)
	if held {
		_ = unlockFile(f)
	}
	if !errors.Is(err, errLockNotRegular) || held {
		t.Fatalf("held %v err %v, want a refusal with errLockNotRegular", held, err)
	}
}
