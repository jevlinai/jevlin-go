package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// A registration_pending.json in the state dir, a writable root of Codex's
// sandbox, is from an older release or planted. The discard removes the name
// and never what it points at: a symlink or a hard link a sandboxed command
// left there to a participant's file outside costs the link, never the file.
func TestLegacyJournalDiscardRemovesOnlyTheName(t *testing.T) {
	setup := func(t *testing.T) (store *Store, legacy, canary string) {
		t.Helper()
		base := t.TempDir()
		state := filepath.Join(base, "state")
		s, err := OpenStore(state)
		if err != nil {
			t.Fatal(err)
		}
		canary = filepath.Join(base, "outside.json")
		if err := os.WriteFile(canary, []byte("the participant's own file"), 0o600); err != nil {
			t.Fatal(err)
		}
		return s, filepath.Join(state, registrationPendingFile), canary
	}
	check := func(t *testing.T, s *Store, legacy, canary string) {
		t.Helper()
		discarded, err := s.DiscardLegacyPendingRegistration()
		if err != nil || !discarded {
			t.Fatalf("discard = %t, %v; want true, nil", discarded, err)
		}
		if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
			t.Fatalf("the name in the state dir survived the discard: %v", err)
		}
		got, err := os.ReadFile(canary) // #nosec G304 -- the test's own file
		if err != nil || string(got) != "the participant's own file" {
			t.Fatalf("the file outside the state dir changed: %q, %v", got, err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		s, legacy, canary := setup(t)
		if err := os.Symlink(canary, legacy); err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
			}
			t.Skipf("cannot make a symlink here: %v", err)
		}
		check(t, s, legacy, canary)
	})
	t.Run("hard link", func(t *testing.T) {
		s, legacy, canary := setup(t)
		if err := os.Link(canary, legacy); err != nil {
			t.Fatal(err)
		}
		check(t, s, legacy, canary)
	})
}
