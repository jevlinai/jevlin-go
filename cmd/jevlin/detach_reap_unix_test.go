//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// zombieChildren counts the exited, unreaped children of this process.
func zombieChildren(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,stat=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	me := strconv.Itoa(os.Getpid())
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == me && strings.HasPrefix(f[2], "Z") {
			n++
		}
	}
	return n
}

// A detached child is reaped by the process that started it, so a parent
// that keeps running, as `jevlin mcp` does for as long as a host holds it,
// gathers no zombies however many searches start a resume.
func TestADetachedChildIsReapedWhileItsParentRuns(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true(1) on this system")
	}
	if n := zombieChildren(t); n != 0 {
		t.Fatalf("%d zombie children before the test began", n)
	}
	for range 3 {
		if err := spawnDetached(trueBin, nil); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := zombieChildren(t)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d detached children exited and were never reaped", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
