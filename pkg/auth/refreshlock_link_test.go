package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// refresh.token.lock lives in the state dir, a writable root of Codex's
// sandbox. A symlink a sandboxed command left at the name is refused rather
// than followed to a file outside; a hard link is locked, which writes
// nothing. The triage of #49-#69 found this lock missed by all of them.
func TestRefreshLockNeverFollowsOrWritesALinkAtItsName(t *testing.T) {
	setup := func(t *testing.T) (lock, canary string) {
		t.Helper()
		base := t.TempDir()
		state := filepath.Join(base, "state")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		canary = filepath.Join(base, "outside.txt")
		if err := os.WriteFile(canary, []byte("the participant's own file"), 0o600); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(state, refreshLockFile), canary
	}
	intact := func(t *testing.T, canary string) {
		t.Helper()
		got, err := os.ReadFile(canary) // #nosec G304 -- the test's own file
		if err != nil || string(got) != "the participant's own file" {
			t.Fatalf("the file outside the state dir changed: %q, %v", got, err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		lock, canary := setup(t)
		if err := os.Symlink(canary, lock); err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
			}
			t.Skipf("cannot make a symlink here: %v", err)
		}
		f, ok, err := tryLockRefreshFile(lock)
		if f != nil {
			_ = f.Close()
		}
		if ok || err == nil {
			t.Fatalf("refresh.token.lock as a symlink out of the state dir was taken (ok=%t, err=%v)", ok, err)
		}
		intact(t, canary)
	})
	t.Run("hard link", func(t *testing.T) {
		lock, canary := setup(t)
		if err := os.Link(canary, lock); err != nil {
			t.Fatal(err)
		}
		if f, _, _ := tryLockRefreshFile(lock); f != nil {
			_ = f.Close()
		}
		intact(t, canary)
	})
}
