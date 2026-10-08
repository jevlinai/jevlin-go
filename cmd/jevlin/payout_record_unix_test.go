//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A resume that Codex's sandbox started runs where the state directory is
// writable and the jevlin home is not. Its adoption of an address already in
// force fails quietly, leaves everything as it was, and the next foreground
// connect adopts.
func TestAnAdoptionTheSandboxCannotWriteWaitsForTheNextForegroundRun(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	legacy := plantStatePayout(t, stateDir, participantAddress)
	as.setActiveAddress(participantAddress)
	home := filepath.Dir(stateDir)
	if err := os.Chmod(home, 0o500); err != nil { // #nosec G302 -- deliberately unwritable, standing for the sandbox
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) }) // #nosec G302 -- restore so t.TempDir cleanup can remove it
	if f, err := os.CreateTemp(home, "probe"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		skipPermissionTest(t, "a read-only directory is writable here (running as root?)")
	}

	code, out, errOut := runConnect(t, cfgPath, nil, "-resume")
	if code != exitOK {
		t.Fatalf("the resume exited %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(legacy) {
		t.Fatal("the resume removed the address it could not adopt")
	}
	if n := as.declarationAttempts(); n != 0 {
		t.Fatalf("the resume declared (%d attempts)", n)
	}

	if err := os.Chmod(home, 0o700); err != nil { // #nosec G302 -- a directory, writable again for the foreground run
		t.Fatal(err)
	}
	if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != participantAddress {
		t.Fatalf("the foreground run did not adopt: %q ok=%v err=%v", got, ok, err)
	}
}
