//go:build !windows

package fsx

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test when f has not returned by the deadline: a read that
// waits on a FIFO would otherwise hang the whole package's tests.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return within 5s: it waited on what was planted at the name", what)
	}
}

// A FIFO at a name this process reads would block the read until a writer
// came. Every reader here returns at once and refuses it.
func TestNoReadOrLockWaitsOnAFIFO(t *testing.T) {
	root, _ := writableRoot(t)
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	within(t, "ReadRegular", func() {
		if _, err := ReadRegular(fifo, 1<<20); !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadRegular on a FIFO: %v, want ErrNotRegular", err)
		}
	})
	within(t, "OpenLock", func() {
		if f, err := OpenLock(fifo); err == nil {
			_ = f.Close()
			t.Error("OpenLock took a FIFO")
		}
	})
	r, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	within(t, "Root.ReadDir", func() {
		if _, err := r.ReadDir("fifo"); err == nil {
			t.Error("Root.ReadDir listed a FIFO")
		}
	})
	within(t, "Root.ReadRegular", func() {
		if _, err := r.ReadRegular("fifo", 1<<20); !errors.Is(err, ErrNotRegular) {
			t.Errorf("Root.ReadRegular on a FIFO: %v, want ErrNotRegular", err)
		}
	})
}
