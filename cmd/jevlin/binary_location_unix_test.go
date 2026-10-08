//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// layoutRoot is a fresh temporary directory with the mode every layout under
// it assumes. testing creates t.TempDir() with os.Mkdir(dir, 0o777), so its
// mode is whatever the umask leaves: 0755 under CI's 022, but 0777 under 0 —
// a directory the walk refuses at before it reaches the modes a test set — and
// 0700 under 0077. It is resolved too, so the paths a refusal names match on
// macOS, where the temp directory is under the /var -> /private/var link.
func layoutRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chmod(t, root, 0o755)
	return root
}

// layout is root/bin/jevlin with TMPDIR moved away from root, so only the
// modes decide. The walk reports the nearest offending component first, so a
// refusal that names neither bin nor the file came from above root.
func binaryLayout(t *testing.T) (root, bin, exe string) {
	t.Helper()
	root = layoutRoot(t)
	t.Setenv("TMPDIR", filepath.Join(root, "elsewhere"))
	bin = filepath.Join(root, "bin")
	exe = filepath.Join(bin, "jevlin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, bin, exe
}

func chmod(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}

func refusedAt(err error, p string) bool {
	return errors.Is(err, errUnsafeBinaryLocation) && strings.Contains(err.Error(), ": "+p+" ")
}

func TestBinaryLocationRefusesWritableComponents(t *testing.T) {
	_, bin, exe := binaryLayout(t)
	if _, err := checkBinaryLocation(exe); refusedAt(err, bin) || refusedAt(err, exe) {
		t.Fatalf("an owner-only layout was refused at its own files: %v", err)
	}

	chmod(t, bin, 0o777|os.ModeSticky)
	if _, err := checkBinaryLocation(exe); !refusedAt(err, bin) || !strings.Contains(err.Error(), "every user") {
		t.Errorf("a world-writable sticky directory was accepted: %v", err)
	}
	chmod(t, bin, 0o700)

	chmod(t, exe, 0o706)
	if _, err := checkBinaryLocation(exe); !refusedAt(err, exe) {
		t.Errorf("a world-writable binary was accepted: %v", err)
	}
	chmod(t, exe, 0o700)

	// Group-write by this machine's real group for the directory: never a
	// refusal, and a warning unless the group is private or root-equivalent.
	chmod(t, bin, 0o770)
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	gid := fi.Sys().(*syscall.Stat_t).Gid
	name := groupName(gid)
	warnings, err := checkBinaryLocation(exe)
	if refusedAt(err, bin) {
		t.Errorf("a group-writable directory was refused: %v", err)
	}
	trusted := privateGroup(gid, name) || rootEquivalentGroup(name)
	if warnedAt(warnings, bin) == trusted {
		t.Errorf("group-writable directory, group %q trusted %v: warnings %q", name, trusted, warnings)
	}
}

func warnedAt(warnings []string, p string) bool {
	for _, w := range warnings {
		if strings.HasPrefix(w, p+" ") {
			return true
		}
	}
	return false
}

// homebrewLayout is the tree npm leaves under a Homebrew prefix:
// prefix/lib/node_modules/jevlin/bin/jevlin, with prefix/bin/jevlin linking
// to it, prefix, prefix/lib and prefix/bin group-writable and the rest 0755.
// TMPDIR is moved away so only the modes and the group decide. It answers
// the prefix and the two paths a hook could record: the link and the file.
func homebrewLayout(t *testing.T) (prefix, link, exe string) {
	t.Helper()
	root := layoutRoot(t)
	t.Setenv("TMPDIR", filepath.Join(root, "elsewhere"))
	prefix = filepath.Join(root, "homebrew")
	pkgBin := filepath.Join(prefix, "lib", "node_modules", "jevlin", "bin")
	if err := os.MkdirAll(pkgBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(pkgBin, "jevlin")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(prefix, "bin", "jevlin")
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "jevlin", "bin", "jevlin"), link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"lib/node_modules", "lib/node_modules/jevlin", "lib/node_modules/jevlin/bin"} {
		chmod(t, filepath.Join(prefix, d), 0o755)
	}
	for _, d := range []string{"", "lib", "bin"} {
		chmod(t, filepath.Join(prefix, d), 0o775)
	}
	chmod(t, exe, 0o755)
	return prefix, link, exe
}

// nameTreeGroup makes groupName answer name for the group the files under
// root were created with, which this test cannot chgrp to admin or staff.
func nameTreeGroup(t *testing.T, root, name string) {
	t.Helper()
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	gid := fi.Sys().(*syscall.Stat_t).Gid
	real := groupName
	t.Cleanup(func() { groupName = real })
	groupName = func(g uint32) string {
		if g == gid {
			return name
		}
		return real(g)
	}
}

// judgedUnder reports a refusal or warning that names a path under root.
// A refusal is allowed only at a directory above it, which is the test
// machine's own layout — /tmp is world-writable — and which the walk reaches
// only after every component under root, nearest first; any other error
// means the walk never got that far, and passing on it would be vacuous.
func judgedUnder(t *testing.T, root string, warnings []string, err error) {
	t.Helper()
	if err != nil {
		above := false
		for d := filepath.Dir(root); ; d = filepath.Dir(d) {
			above = above || refusedAt(err, d)
			if filepath.Dir(d) == d {
				break
			}
		}
		if !above {
			t.Errorf("refused at or under %s: %v", root, err)
		}
	}
	for _, w := range warnings {
		if strings.HasPrefix(w, root) {
			t.Errorf("warned under %s: %s", root, w)
		}
	}
}

// A Homebrew prefix is group-writable by admin, whose members can already
// become root. `npm install -g jevlin && jevlin setup` with Homebrew's Node
// is the README's first install path, so a binary there is accepted without
// a word, whichever root-equivalent group the prefix has.
func TestBinaryLocationAcceptsAHomebrewPrefix(t *testing.T) {
	for _, group := range []string{"admin", "wheel", "sudo", "root"} {
		t.Run(group, func(t *testing.T) {
			prefix, link, exe := homebrewLayout(t)
			nameTreeGroup(t, prefix, group)
			for _, p := range []string{link, exe} {
				warnings, err := checkBinaryLocation(p)
				judgedUnder(t, prefix, warnings, err)
			}
		})
	}
}

// Any other group — Debian's root:staff /usr/local, a Linuxbrew prefix — is
// warned about by name, nearest directory first, and not refused: a mode
// cannot say whether anyone but this user is in it.
func TestBinaryLocationWarnsOnAnyOtherGroup(t *testing.T) {
	for _, group := range []string{"staff", "linuxbrew", ""} {
		t.Run(fmt.Sprintf("%q", group), func(t *testing.T) {
			prefix, link, _ := homebrewLayout(t)
			nameTreeGroup(t, prefix, group)
			warnings, err := checkBinaryLocation(link)
			if err != nil && strings.Contains(err.Error(), ": "+prefix) {
				t.Fatalf("a group-writable prefix was refused: %v", err)
			}
			lib, bin := filepath.Join(prefix, "lib"), filepath.Join(prefix, "bin")
			if len(warnings) < 3 || !warnedAt(warnings, bin) || !warnedAt(warnings, lib) || !warnedAt(warnings, prefix) {
				t.Fatalf("want warnings at %s, %s and %s, got %q", bin, lib, prefix, warnings)
			}
			want := "is writable by its group (gid "
			if group != "" {
				want = "is writable by its group " + group + " (gid "
			}
			if !strings.Contains(warnings[0], want) || !strings.Contains(warnings[0], link) {
				t.Errorf("warning %q does not name the group as %q and the binary", warnings[0], want)
			}
		})
	}
}

// ownedInfo is a directory with the owner, group and mode a test names. A
// test cannot chown a file to another user, so the owner rule is driven
// through writableByOthers with this in place of a real Lstat.
type ownedInfo struct {
	fakeFileInfo
	mode     os.FileMode
	uid, gid uint32
}

func (f ownedInfo) Mode() os.FileMode { return os.ModeDir | f.mode }
func (f ownedInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid, Gid: f.gid} }

// otherUID is a uid that is neither this user's nor root's.
func otherUID() uint32 {
	self := uint32(os.Getuid()) // #nosec G115 -- a uid is never negative on POSIX
	for u := uint32(4242); ; u++ {
		if u != self {
			return u
		}
	}
}

// A directory owned by anyone but this user or root is refused whatever
// its mode, because its owner can change it; this user's and root's own
// 0755 directories are accepted.
func TestWritableByOthersRefusesAnotherOwner(t *testing.T) {
	self := uint32(os.Getuid())  // #nosec G115 -- a uid is never negative on POSIX
	group := uint32(os.Getgid()) // #nosec G115 -- a gid is never negative on POSIX
	other := otherUID()
	for _, c := range []struct {
		uid    uint32
		mode   os.FileMode
		refuse bool
	}{
		{self, 0o755, false},
		{0, 0o755, false},
		{other, 0o755, true},
		{other, 0o700, true},
	} {
		refuse, warn := writableByOthers(ownedInfo{fakeFileInfo: fakeFileInfo{name: "bin", dir: true}, mode: c.mode, uid: c.uid, gid: group})
		if warn != "" {
			t.Errorf("uid %d mode %v: warned %q", c.uid, c.mode, warn)
		}
		want := fmt.Sprintf("is owned by another user (uid %d)", c.uid)
		if c.refuse && refuse != want {
			t.Errorf("uid %d mode %v: refusal %q, want %q", c.uid, c.mode, refuse, want)
		}
		if !c.refuse && refuse != "" {
			t.Errorf("uid %d mode %v: refused %q", c.uid, c.mode, refuse)
		}
	}
}

// The group's write bit does not soften the rules before it: a prefix
// another user owns, or one every user can write, is still refused.
func TestBinaryLocationGroupTrustDoesNotCoverOtherRules(t *testing.T) {
	prefix, link, _ := homebrewLayout(t)
	nameTreeGroup(t, prefix, "admin")
	lib := filepath.Join(prefix, "lib")
	chmod(t, lib, 0o777)
	if _, err := checkBinaryLocation(link); !refusedAt(err, lib) {
		t.Errorf("a world-writable admin directory was accepted: %v", err)
	}

	// Owned by another user and group-writable by admin, as a Homebrew
	// prefix another account installed would be.
	fi, err := os.Stat(prefix)
	if err != nil {
		t.Fatal(err)
	}
	gid := fi.Sys().(*syscall.Stat_t).Gid
	other := otherUID()
	refuse, _ := writableByOthers(ownedInfo{fakeFileInfo: fakeFileInfo{name: "homebrew", dir: true}, mode: 0o775, uid: other, gid: gid})
	if want := fmt.Sprintf("is owned by another user (uid %d)", other); refuse != want {
		t.Errorf("an admin directory another user owns: refusal %q, want %q", refuse, want)
	}
}

// A symlink is judged by both ends: the directory holding the link, and
// every directory above the file it resolves to.
func TestBinaryLocationChecksBothEndsOfASymlink(t *testing.T) {
	root, bin, exe := binaryLayout(t)
	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(open, "jevlin")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}

	chmod(t, open, 0o777)
	if _, err := checkBinaryLocation(link); !refusedAt(err, open) {
		t.Errorf("a link in a world-writable directory was accepted: %v", err)
	}
	chmod(t, open, 0o700)

	chmod(t, bin, 0o777)
	if _, err := checkBinaryLocation(link); !refusedAt(err, bin) {
		t.Errorf("a link to a binary in a world-writable directory was accepted: %v", err)
	}
}
