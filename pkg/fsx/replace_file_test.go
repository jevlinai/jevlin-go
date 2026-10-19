package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// noStagedFiles fails if a temporary file is left in dir.
func noStagedFiles(t *testing.T, dir string) {
	t.Helper()
	temps, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("staged files left in %s: %v %v", dir, temps, err)
	}
}

// A write that stops at any stage before the rename leaves the old bytes
// and no temporary file, whether the name is the file or a link to it; and
// the link stays a link.
func TestReplaceFileFailureStagesLeaveTheOldBytes(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		for _, stage := range []string{"write", "file sync", "close", "publication"} {
			t.Run(stage+map[bool]string{true: "/through a link", false: "/direct"}[viaLink], func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "settings.json")
				if err := os.WriteFile(target, []byte(`{"old":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				name := target
				if viaLink {
					name = filepath.Join(t.TempDir(), "settings.json")
					symlinkOrSkip(t, target, name)
				}
				failure := errors.New("injected failure")
				ops := defaultOperations()
				create := ops.create
				ops.create = func(dir, pattern string) (stagedFile, error) {
					f, err := create(dir, pattern)
					if err != nil {
						return nil, err
					}
					return failingFile{f, stage, failure}, nil
				}
				if stage == "publication" {
					ops.publish = func(string, string, bool) error { return failure }
				}
				err := replaceFile(name, []byte(`{"new":true}`), 0o600, ops)
				var se *StageError
				if !errors.Is(err, failure) || !errors.As(err, &se) || se.Stage != stage || se.Published {
					t.Fatalf("stage classification: %v", err)
				}
				if got, err := os.ReadFile(target); err != nil || string(got) != `{"old":true}` { // #nosec G304 -- test path
					t.Fatalf("the original after a failed %s: %q %v", stage, got, err)
				}
				noStagedFiles(t, dir)
				if viaLink {
					if info, err := os.Lstat(name); err != nil || info.Mode()&fs.ModeSymlink == 0 {
						t.Fatalf("the link is no longer a link: %v %v", info, err)
					}
				}
			})
		}
	}
}

// A link is followed to its file, which is replaced; the link itself, and
// what it says, are left as they were. A relative link, an absolute one, a
// chain of two, and a dangling one, which a write through the link creates.
func TestReplaceFileWritesThroughALinkAndLeavesTheLink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dangling bool
		chain    bool
		relative bool
	}{
		{name: "absolute"},
		{name: "relative", relative: true},
		{name: "a chain of two", chain: true},
		{name: "dangling", dangling: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dotfiles := t.TempDir()
			target := filepath.Join(dotfiles, "settings.json")
			if !tc.dangling {
				if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			home := t.TempDir()
			link := filepath.Join(home, "settings.json")
			dest := target
			if tc.relative {
				rel, err := filepath.Rel(home, target)
				if err != nil {
					t.Fatal(err)
				}
				dest = rel
			}
			if tc.chain {
				middle := filepath.Join(t.TempDir(), "middle.json")
				symlinkOrSkip(t, target, middle)
				dest = middle
			}
			symlinkOrSkip(t, dest, link)

			if err := ReplaceFile(link, []byte("new"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "new" { // #nosec G304 -- test path
				t.Fatalf("the target: %q %v", got, err)
			}
			info, err := os.Lstat(link)
			if err != nil || info.Mode()&fs.ModeSymlink == 0 {
				t.Fatalf("the link was replaced by a regular file: %v %v", info, err)
			}
			if said, err := os.Readlink(link); err != nil || said != dest {
				t.Fatalf("the link now says %q (%v), want %q", said, err, dest)
			}
			noStagedFiles(t, home)
			noStagedFiles(t, dotfiles)
		})
	}
}

// An existing file keeps its permission bits, whatever mode the caller
// planned for a new one; a new file gets the planned mode. The rename is of a
// new inode, so its bits come from the temporary file, which is why this is
// not automatic.
func TestReplaceFileKeepsAnExistingFilesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no POSIX permission bits to keep")
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o644); err != nil { // #nosec G302 -- a mode the test checks is kept; chmod, which the umask does not touch
		t.Fatal(err)
	}
	if err := ReplaceFile(existing, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(existing); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("an existing 0644 file: %v %v, want 0644 kept", info.Mode().Perm(), err)
	}

	// Through a link, the bits kept are the target's.
	target := filepath.Join(dir, "target.toml")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil { // #nosec G302 -- a mode the test checks is kept
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	symlinkOrSkip(t, target, link)
	if err := ReplaceFile(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("a linked 0640 target: %v %v, want 0640 kept", info.Mode().Perm(), err)
	}

	fresh := filepath.Join(dir, "hooks.json")
	if err := ReplaceFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new file: %v %v, want the planned 0600", info.Mode().Perm(), err)
	}
}

// The temporary file a killed writer would leave carries no suffix a host
// loads by pattern.
func TestReplaceFileStagesUnderANameNoHostLoads(t *testing.T) {
	dir := t.TempDir()
	var staged string
	ops := defaultOperations()
	create := ops.create
	ops.create = func(dir, pattern string) (stagedFile, error) {
		f, err := create(dir, pattern)
		if err == nil {
			staged = f.Name()
		}
		return f, err
	}
	if err := replaceFile(filepath.Join(dir, "hooks.json"), []byte("{}"), 0o600, ops); err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(staged)
	if !strings.HasPrefix(base, ".tmp-") {
		t.Fatalf("staged as %q, want a .tmp- name", base)
	}
	for _, suffix := range []string{".json", ".yaml", ".yml", ".toml"} {
		if strings.HasSuffix(base, suffix) {
			t.Fatalf("staged as %q, a name a host loads by pattern", base)
		}
	}
}

// What is not a regular file is not replaced: a directory, and a loop of
// links, which is refused rather than followed for ever.
func TestReplaceFileRefusesWhatIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(sub, []byte("x"), 0o600); err == nil {
		t.Fatal("a directory was replaced")
	}
	if info, err := os.Stat(sub); err != nil || !info.IsDir() {
		t.Fatalf("the directory: %v %v", info, err)
	}

	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	symlinkOrSkip(t, b, a)
	symlinkOrSkip(t, a, b)
	if err := ReplaceFile(a, []byte("x"), 0o600); err == nil {
		t.Fatal("a loop of links was written through")
	}
	noStagedFiles(t, dir)
}

// A file the in-place write could not have written is not replaced either:
// one made read-only stays as it is, and the error is the one opening it for
// writing gives.
func TestReplaceFileLeavesAFileItCouldNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil { // #nosec G304 -- test path
		_ = f.Close()
		t.Skip("this account writes a read-only file anyway (root)")
	}
	err := ReplaceFile(path, []byte("ours"), 0o600)
	var se *StageError
	if !errors.As(err, &se) || se.Stage != "open" || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("replacing a read-only file: %v, want an open-stage permission error", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "theirs" { // #nosec G304 -- test path
		t.Fatalf("the read-only file: %q %v", got, err)
	}
	noStagedFiles(t, dir)
}

// A relative link with ".." under a directory that is itself a link is
// followed as the kernel follows it, from the real directory. Here
// home/.claude is a link to dotfiles/claude, and the settings file there
// names ../shared/settings.json: the file replaced is dotfiles/shared's,
// and home/shared's, which a lexical reading of the path would pick, is left
// byte for byte.
func TestReplaceFileFollowsARelativeLinkFromTheRealDirectory(t *testing.T) {
	root := t.TempDir()
	dotfiles := filepath.Join(root, "dotfiles")
	home := filepath.Join(root, "home")
	for _, d := range []string{filepath.Join(dotfiles, "claude"), filepath.Join(dotfiles, "shared"), filepath.Join(home, "shared")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realFile := filepath.Join(dotfiles, "shared", "settings.json")
	bystander := filepath.Join(home, "shared", "settings.json")
	if err := os.WriteFile(realFile, []byte("real, old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bystander, []byte("someone else's"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(dotfiles, "claude"), filepath.Join(home, ".claude"))
	symlinkOrSkip(t, filepath.Join("..", "shared", "settings.json"), filepath.Join(dotfiles, "claude", "settings.json"))

	if err := ReplaceFile(filepath.Join(home, ".claude", "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(realFile); err != nil || string(got) != "new" { // #nosec G304 -- test path
		t.Fatalf("the file the link names: %q %v, want the new bytes", got, err)
	}
	if got, err := os.ReadFile(bystander); err != nil || string(got) != "someone else's" { // #nosec G304 -- test path
		t.Fatalf("the file a lexical reading names was written: %q %v", got, err)
	}
	noStagedFiles(t, filepath.Join(dotfiles, "shared"))
	noStagedFiles(t, filepath.Join(home, "shared"))
}
