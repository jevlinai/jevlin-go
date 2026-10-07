//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// layout is root/bin/jevlin with TMPDIR moved away from root, so only the
// modes decide. The walk reports the nearest offending component first, so a
// refusal that names neither bin nor the file came from above root.
func binaryLayout(t *testing.T) (root, bin, exe string) {
	t.Helper()
	root = t.TempDir()
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
	if err := checkBinaryLocation(exe); refusedAt(err, bin) || refusedAt(err, exe) {
		t.Fatalf("an owner-only layout was refused at its own files: %v", err)
	}

	chmod(t, bin, 0o777|os.ModeSticky)
	if err := checkBinaryLocation(exe); !refusedAt(err, bin) || !strings.Contains(err.Error(), "every user") {
		t.Errorf("a world-writable sticky directory was accepted: %v", err)
	}
	chmod(t, bin, 0o700)

	chmod(t, exe, 0o706)
	if err := checkBinaryLocation(exe); !refusedAt(err, exe) {
		t.Errorf("a world-writable binary was accepted: %v", err)
	}
	chmod(t, exe, 0o700)

	chmod(t, bin, 0o770)
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	err = checkBinaryLocation(exe)
	if private := privateGroup(fi.Sys().(*syscall.Stat_t).Gid); private == refusedAt(err, bin) {
		t.Errorf("group-writable directory, private group %v: %v", private, err)
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
	if err := checkBinaryLocation(link); !refusedAt(err, open) {
		t.Errorf("a link in a world-writable directory was accepted: %v", err)
	}
	chmod(t, open, 0o700)

	chmod(t, bin, 0o777)
	if err := checkBinaryLocation(link); !refusedAt(err, bin) {
		t.Errorf("a link to a binary in a world-writable directory was accepted: %v", err)
	}
}
