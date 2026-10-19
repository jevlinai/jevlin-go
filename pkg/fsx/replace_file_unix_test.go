//go:build !windows

package fsx

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO at the name is refused at once. Opening one for writing waits for a
// reader, so the check that the name is a regular file has to come before
// ReplaceFile opens anything to ask whether it may be written.
func TestReplaceFileDoesNotWaitOnAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ReplaceFile(path, []byte("{}"), 0o600) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was replaced")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReplaceFile waited on a FIFO")
	}
}
