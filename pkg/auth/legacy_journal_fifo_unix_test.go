//go:build !windows

package auth

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// connect discards a state-dir journal before anything else it does; a FIFO
// a sandboxed command left at the name must cost that name, never a wait.
func TestLegacyJournalDiscardNeverWaitsOnAFIFO(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(s.dir, registrationPendingFile)
	if err := syscall.Mkfifo(legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var discarded bool
	var derr error
	go func() { defer close(done); discarded, derr = s.DiscardLegacyPendingRegistration() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DiscardLegacyPendingRegistration waited on a FIFO at the legacy journal's name")
	}
	if derr != nil || !discarded {
		t.Fatalf("discard = %t, %v; want true, nil", discarded, derr)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("the FIFO survived the discard: %v", err)
	}
}
