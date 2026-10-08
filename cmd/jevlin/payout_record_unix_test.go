//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A resume that Codex's sandbox started runs where the state directory is
// writable and the jevlin home is not. A payout.json an older version left
// that is this installation's own wallet address is still declared (the
// wallet is evidence the sandbox cannot forge) but cannot be recorded: that
// fails quietly, the state directory's copy stays, and the next foreground
// connect records it and removes the copy.
func TestAnAdoptionTheSandboxCannotWriteWaitsForTheNextForegroundRun(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	walletDir, err := defaultWalletDir()
	if err != nil {
		t.Fatal(err)
	}
	writeWalletFixture(t, walletDir)
	own := walletFixtureAddress(t)
	legacy := plantStatePayout(t, stateDir, own)
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
		t.Fatal("the resume removed the address it could not record")
	}
	if got := as.declaredAddress(); got != own {
		t.Fatalf("the resume declared %q, want the installation's own wallet %q", got, own)
	}

	if err := os.Chmod(home, 0o700); err != nil { // #nosec G302 -- a directory, writable again for the foreground run
		t.Fatal(err)
	}
	if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != own {
		t.Fatalf("the foreground run did not record it: %q ok=%v err=%v", got, ok, err)
	}
	if lexists(legacy) {
		t.Fatal("the foreground run left the recorded address in the state directory")
	}
}
