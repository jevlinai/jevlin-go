//go:build !windows

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// returnsWithin fails the test when f has not returned by the deadline. A
// read that waits on a FIFO a sandboxed command left at the name would
// otherwise hang the search it runs in front of (hard invariant 1).
func returnsWithin(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s waited on a FIFO at the name it reads", what)
	}
}

func TestNoHookReadWaitsOnAFIFO(t *testing.T) {
	dir, _ := rootAndCanary(t)
	ops := fixedSuffixOps()
	lineage := lineagePath(dir, "/w")
	for _, name := range []string{lineage, filepath.Join(dir, hookStateFile), turnSearchedPath(dir, "turn-1")} {
		if err := syscall.Mkfifo(name, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	returnsWithin(t, "loadLineage", func() {
		if _, ok := loadLineage(ops, lineage); ok {
			t.Error("a FIFO was read as a lineage file")
		}
	})
	returnsWithin(t, "hookWindowID", func() { _ = hookWindowID(ops, hookContext{sessionsDir: dir}, "s") })
	returnsWithin(t, "takeTurnSearched", func() {
		if takeTurnSearched(ops, dir, "turn-1") {
			t.Error("a FIFO was taken as a turn mark")
		}
	})
}
