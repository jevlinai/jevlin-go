//go:build windows

package fsx

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// holdOpen opens path for reading the way another process might, and keeps
// it open until the test ends. share is what that process lets others do.
func holdOpen(t *testing.T, path string, share uint32) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("hold %s open: %v", path, err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
}

// What a rename does to a host file another process has open, which an
// in-place write did not have to care about.
//
// A reader that shares reading and writing but not deletion -- Go's os.Open
// and the C runtime's fopen, which Python's open uses, both open that way --
// blocks the rename: MoveFileEx refuses to replace a file whose open handles
// do not allow it to be deleted. The in-place write it replaces succeeded,
// because writing was shared. The refusal costs nothing but the write: the
// old bytes stay whole, no temporary file is left, and the caller is told.
//
// A reader that also shares deletion -- Node's fs (libuv) and Rust's std open
// that way, and they are what Claude Code, Cursor and Codex are built on --
// does not block it.
func TestReplaceFileWithTheFileHeldOpen(t *testing.T) {
	t.Run("shared for reading and writing only", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		holdOpen(t, path, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)

		err := ReplaceFile(path, []byte("new"), 0o600)
		if err == nil {
			t.Fatal("the rename replaced a file held open without delete sharing")
		}
		t.Logf("refused: %v", err)
		if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "old" { // #nosec G304 -- test path
			t.Fatalf("the original after a refused rename: %q %v", got, rerr)
		}
		noStagedFiles(t, dir)

		// The write this replaces: in place, it went through the same hold.
		if err := os.WriteFile(path, []byte("in place"), 0o600); err != nil {
			t.Fatalf("an in-place write under the same hold: %v", err)
		}
	})
	t.Run("shared for deletion too", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		holdOpen(t, path, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)

		if err := ReplaceFile(path, []byte("new"), 0o600); err != nil {
			t.Fatalf("the rename over a file held open with delete sharing: %v", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "new" { // #nosec G304 -- test path
			t.Fatalf("after the rename: %q %v", got, err)
		}
		noStagedFiles(t, dir)
	})
}
