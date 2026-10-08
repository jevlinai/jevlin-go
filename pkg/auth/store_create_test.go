package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useRestrictRecorder replaces restrictCreatedStateDir for one test. Each call
// is recorded with what the directory held at that moment, and fail, if set,
// is what the call returns after running onCall.
type restrictRecorder struct {
	calls  []string
	held   [][]string
	fail   error
	onCall func(dir string)
}

func useRestrictRecorder(t *testing.T) *restrictRecorder {
	t.Helper()
	r := &restrictRecorder{}
	saved := restrictCreatedStateDir
	restrictCreatedStateDir = func(dir string) error {
		r.calls = append(r.calls, dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("the directory OpenStore asked to restrict cannot be listed: %v", err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		r.held = append(r.held, names)
		if r.onCall != nil {
			r.onCall(dir)
		}
		return r.fail
	}
	t.Cleanup(func() { restrictCreatedStateDir = saved })
	return r
}

// OpenStore restricts a state directory it creates, once, with the directory
// there and nothing in it. This is the wiring: on POSIX the real restriction
// does nothing, so without the recorder removing the call would change no
// result on this OS.
func TestOpenStoreRestrictsTheDirectoryItCreates(t *testing.T) {
	r := useRestrictRecorder(t)
	dir := filepath.Join(t.TempDir(), "state")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("OpenStore returned no store")
	}
	if len(r.calls) != 1 || r.calls[0] != dir {
		t.Fatalf("restricted %q, want exactly [%q]", r.calls, dir)
	}
	if len(r.held[0]) != 0 {
		t.Errorf("the directory held %q when it was restricted, want it empty: a file written before the list is set is under the parent's", r.held[0])
	}
}

// A directory that exists is not OpenStore's to restrict: it may be setup's
// <home>\state, or one a sandbox's setup has deliberately granted, and a
// restriction would take that entry away. And an open that creates nothing
// restricts nothing.
func TestOpenStoreLeavesADirectoryThatExistsAlone(t *testing.T) {
	r := useRestrictRecorder(t)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStoreExisting(dir); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "never-made")
	if _, err := OpenStoreExisting(missing); err == nil {
		t.Fatal("OpenStoreExisting opened a directory that does not exist")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenStoreExisting created %s (%v)", missing, err)
	}
	if len(r.calls) != 0 {
		t.Errorf("restricted %q, want nothing: none of these opens created a directory", r.calls)
	}
}

// A restriction that fails must not become a directory the next run takes as
// it finds it. The directory is removed, the error says why, and the next
// OpenStore creates and restricts again.
func TestAFailedRestrictionLeavesNoDirectoryBehind(t *testing.T) {
	r := useRestrictRecorder(t)
	r.fail = errors.New("injected: access denied")
	dir := filepath.Join(t.TempDir(), "state")

	s, err := OpenStore(dir)
	if err == nil || !strings.Contains(err.Error(), "restrict state dir") || !strings.Contains(err.Error(), "injected: access denied") {
		t.Fatalf("OpenStore = %v, %v; want an error naming the restriction and its cause", s, err)
	}
	if s != nil {
		t.Error("OpenStore returned a store with its error")
	}
	if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the directory whose restriction failed is still there (%v): the next OpenStore would open it unrestricted", statErr)
	}

	r.fail = nil
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Errorf("restricted %d times, want 2: the second run must restrict the directory it creates", len(r.calls))
	}
}

// If the directory cannot be taken away, that is said too, rather than only
// the first error: the participant is left with a directory to deal with.
func TestAFailedRestrictionThatCannotBeUndoneSaysSo(t *testing.T) {
	r := useRestrictRecorder(t)
	r.fail = errors.New("injected: access denied")
	// Something appears in the directory before it is removed, so the removal
	// of the (no longer empty) directory fails.
	r.onCall = func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "arrived"), []byte("x"), 0o600); err != nil { // #nosec G703 -- test-owned scratch root
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "state")
	_, err := OpenStore(dir)
	if err == nil || !strings.Contains(err.Error(), "injected: access denied") || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("OpenStore = %v, want an error carrying both the cause and that the removal failed", err)
	}
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Errorf("a directory that was not empty was removed anyway: %v", statErr)
	}
}
