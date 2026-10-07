//go:build !windows

package spool

import (
	"syscall"
	"testing"
	"time"
)

// A FIFO left at the quarantine's name must cost doctor's count, never hang
// it: the listing's open does not wait.
func TestCountQuarantinedNeverWaitsOnAFIFO(t *testing.T) {
	s := newSpool(t)
	if err := syscall.Rmdir(s.quarantine); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(s.quarantine, 0o600); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenExisting(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = ro.CountQuarantined() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CountQuarantined waited on a FIFO at the quarantine's name")
	}
}
