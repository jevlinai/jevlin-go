package auth

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	failAt string // if set, only a call for this directory fails
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
		if r.failAt != "" && dir != r.failAt {
			return nil
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

// MkdirAll can make several levels, and each inherits its parent's list: a
// D:\jevlin made on the way to D:\jevlin\state would keep D:\'s entries, and
// whoever those admit could rename the restricted state dir away and plant
// their own. Every level the call made is restricted, outermost first, and the
// leaf is still the one with nothing in it.
func TestOpenStoreRestrictsEveryDirectoryItCreates(t *testing.T) {
	r := useRestrictRecorder(t)
	root := t.TempDir()
	a, b, dir := filepath.Join(root, "a"), filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "state")
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	if want := []string{a, b, dir}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("restricted %q, want %q: every level this call made, outermost first", r.calls, want)
	}
	if len(r.held[2]) != 0 {
		t.Errorf("the state dir held %q when it was restricted, want it empty", r.held[2])
	}
}

// What was already there is not OpenStore's to restrict: a level that exists
// may be a parent the participant chose, or setup's own.
func TestOpenStoreRestrictsOnlyTheLevelsItMade(t *testing.T) {
	r := useRestrictRecorder(t)
	root := t.TempDir()
	a, b, dir := filepath.Join(root, "a"), filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "state")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	if want := []string{b, dir}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("restricted %q, want %q: not %s, which was already there", r.calls, want, a)
	}
}

// A level that cannot be restricted takes with it everything this call made,
// the ones already restricted too, so the next run starts from nothing and
// restricts all of them again; what existed before stays.
func TestAFailedRestrictionOfAnAncestorUndoesEveryLevelItMade(t *testing.T) {
	r := useRestrictRecorder(t)
	root := t.TempDir()
	existing, b, dir := filepath.Join(root, "a"), filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "state")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	r.fail, r.failAt = errors.New("injected: access denied"), b

	_, err := OpenStore(dir)
	if err == nil || !strings.Contains(err.Error(), b) || !strings.Contains(err.Error(), "injected: access denied") {
		t.Fatalf("OpenStore = %v, want an error naming %s and its cause", err, b)
	}
	for _, gone := range []string{b, dir} {
		if _, statErr := os.Lstat(gone); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s is still there (%v): the next OpenStore would take it as it finds it", gone, statErr)
		}
	}
	if _, statErr := os.Lstat(existing); statErr != nil {
		t.Errorf("a directory that was there before was removed: %v", statErr)
	}

	r.fail, r.calls = nil, nil
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	if want := []string{b, dir}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("the second run restricted %q, want %q", r.calls, want)
	}
}

// MkdirAll that gets part of the way leaves levels no later call would
// restrict, since they exist by then. A component the filesystem refuses
// makes it stop after the one before it.
func TestAMkdirAllThatStopsHalfwayLeavesNothingItMade(t *testing.T) {
	r := useRestrictRecorder(t)
	root := t.TempDir()
	a := filepath.Join(root, "a")
	dir := filepath.Join(a, strings.Repeat("x", 300), "state")
	_, err := OpenStore(dir)
	if err == nil {
		t.Skip("this filesystem accepts a 300-byte name, so MkdirAll cannot be made to stop halfway")
	}
	if !strings.Contains(err.Error(), "create state dir") {
		t.Fatalf("OpenStore = %v, want the failure to create the directory", err)
	}
	if _, statErr := os.Lstat(a); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("%s was made on the way and is still there (%v): no later call would restrict it", a, statErr)
	}
	if len(r.calls) != 0 {
		t.Errorf("restricted %q after the directory could not be made", r.calls)
	}
}

// CredentialFiles is what doctor asks Windows about, by name. This test calls
// the five writers of the credential files and holds the list to what they
// produce, so a rename in one place cannot leave the report looking at a file
// that no longer exists. It does not find a writer it does not call: a file
// added to the store is caught by TestEveryFileTheStoreNamesIsACredentialOrARecord,
// which reads every call from the source.
func TestCredentialFilesAreTheFilesTheStoreWritesForCredentials(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.DPoPKey(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefreshToken("t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParticipationSecret(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureTraceKey(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgentRegistration(AgentRegistration{AgentID: "a", Status: "unclaimed"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	for _, e := range entries {
		written[e.Name()] = true
	}
	listed := map[string]bool{}
	for _, name := range CredentialFiles() {
		listed[name] = true
		if !written[name] {
			t.Errorf("CredentialFiles names %s, which the store did not write; wrote %v", name, written)
		}
	}
	for name := range written {
		if !listed[name] {
			t.Errorf("the store wrote %s for a credential and CredentialFiles does not name it", name)
		}
	}
}
