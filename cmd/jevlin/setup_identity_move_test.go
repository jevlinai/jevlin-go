package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// The key moves before the config changes: a move that fails leaves the
// earlier config, and the key, where they were, so the next run plans the
// move again rather than finding a format-1 file with no key beside its
// state directory and registering a second agent.
func TestAFailedIdentityMoveLeavesTheEarlierConfigInPlace(t *testing.T) {
	s := newSetupSandbox(t)
	earlierInstallationElsewhere(t, s, s.env[config.EnvRouterURL], true)
	before := string(s.readFile(s.cfgPath()))
	real := moveIdentity
	moveIdentity = func(moves [][2]string) error {
		return &os.LinkError{Op: "rename", Old: moves[0][0], New: moves[0][1], Err: errors.New("refused")}
	}
	code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents")
	moveIdentity = real
	if code == exitOK {
		t.Fatalf("setup went on after the key could not move:\n%s", out)
	}
	if string(s.readFile(s.cfgPath())) != before || lexists(s.cfgPath()+".old") || !lexists(filepath.Join(s.home, credentialsFile)) {
		t.Fatalf("a failed move changed the config or the key:\n%s", errOut)
	}
	// The next run moves it and keeps the registration.
	if code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents"); code != exitOK {
		t.Fatalf("the rerun: exit %d\n%s\n%s", code, out, errOut)
	}
	if register, _, _ := s.platform.counts(); register != 0 {
		t.Errorf("the rerun registered a new agent")
	}
}

// Across filesystems a rename fails; the file is copied owner-only, synced,
// published, and the source removed.
func TestAnIdentityFileMovesAcrossFilesystems(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	src, dst := filepath.Join(from, credentialsFile), filepath.Join(to, credentialsFile)
	writeFileT(t, src, `{"v":1,"api_key":"sr-moved"}`)
	real := renameIdentityFile
	renameIdentityFile = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { renameIdentityFile = real })
	if runtime.GOOS == "windows" {
		renameIdentityFile = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.Errno(17)} }
	}
	if err := moveIdentityFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if lexists(src) || string(s2b(t, dst)) != `{"v":1,"api_key":"sr-moved"}` {
		t.Fatalf("the copy did not take the source's place")
	}
	if info, err := os.Stat(dst); err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Errorf("the copy's mode: %v %v", info.Mode(), err)
	}
	// Any other refusal is not a reason to copy.
	writeFileT(t, src, "x")
	renameIdentityFile = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: os.ErrPermission} }
	if err := moveIdentityFile(src, filepath.Join(to, "other")); err == nil || !lexists(src) {
		t.Errorf("a refusal that is not cross-device was copied over: %v", err)
	}
}

func s2b(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p) // #nosec G304 -- this test's own file
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A move that fails part way puts back what it moved, so the files are all
// in one place.
func TestAPartlyFailedIdentityMovePutsBackWhatItMoved(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	for _, n := range []string{credentialsFile, claimRecordFile} {
		writeFileT(t, filepath.Join(from, n), n)
	}
	moves, err := identityMoves(from, to)
	if err != nil || len(moves) != 2 {
		t.Fatalf("plan %v %v", moves, err)
	}
	real := renameIdentityFile
	t.Cleanup(func() { renameIdentityFile = real })
	calls := 0
	renameIdentityFile = func(a, b string) error {
		calls++
		if calls == 2 {
			return errors.New("refused")
		}
		return real(a, b)
	}
	if err := moveIdentity(moves); err == nil {
		t.Fatal("the failure was not reported")
	}
	for _, n := range []string{credentialsFile, claimRecordFile} {
		if !lexists(filepath.Join(from, n)) || lexists(filepath.Join(to, n)) {
			t.Errorf("%s was not put back", n)
		}
	}
}

// One directory spelled two ways is no move and no conflict.
func TestOneDirectoryUnderTwoNamesNeedsNoIdentityMove(t *testing.T) {
	s := newSetupSandbox(t)
	alias := filepath.Join(s.root, "alias")
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(s.home, alias); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if !sameDirectory(s.home, alias) || sameDirectory(s.home, s.root) {
		t.Fatal("sameDirectory does not see through the link, or sees too much")
	}
}

// Relative directories in an earlier config move nothing and are not
// carried: each would be read against whatever directory setup, or later
// any command, ran from.
func TestARelativeEarlierDirectoryMovesAndCarriesNothing(t *testing.T) {
	e := readEarlierConfig([]byte("[mining]\nstate_dir = \"state\"\n[miner]\nintake_dir = \"intake\"\nsessions_dir = \"s\"\n"))
	if root := e.identityRoot(); root != "" {
		t.Errorf("a relative intake directory gave identity root %q", root)
	}
	v := resolveSetupValues("/h", noEnv).carrying(e)
	if v.stateDir != "" || v.sessionsDir != "" {
		t.Errorf("relative directories were carried: state %q sessions %q", v.stateDir, v.sessionsDir)
	}
	e = readEarlierConfig([]byte("[mining]\nstate_dir = \"/abs/state\"\n[miner]\nintake_dir = \"rel\"\n"))
	if root := e.identityRoot(); root != "" {
		t.Errorf("a relative intake directory beside an absolute state one gave %q", root)
	}
}

// The release check's record is not a conflict: the earlier version's
// replaces one already there.
func TestAnEarlierStampReplacesOneAlreadyBesideTheStateDirectory(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	writeFileT(t, filepath.Join(from, updateStampFile), "earlier")
	writeFileT(t, filepath.Join(to, updateStampFile), "newer")
	moves, err := identityMoves(from, to)
	if err != nil || len(moves) != 1 {
		t.Fatalf("plan %v %v", moves, err)
	}
	if err := moveIdentity(moves); err != nil || string(s2b(t, filepath.Join(to, updateStampFile))) != "earlier" {
		t.Errorf("the earlier stamp did not take the place: %v", err)
	}
}
