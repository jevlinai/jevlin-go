//go:build windows

package fsx

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// holdOpen opens path for reading the way another process might, and
// returns the function that closes it; it is closed when the test ends, too.
// share is what that process lets others do.
func holdOpen(t *testing.T, path string, share uint32) (release func()) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("hold %s open: %v", path, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = windows.CloseHandle(h) }) }
	t.Cleanup(release)
	return release
}

// The two ways another process opens a file for reading: sharing reading and
// writing only (Go's os.Open, and the C runtime's fopen, which Python's open
// uses), and sharing deletion too (Node's fs, through libuv, and Rust's std,
// which Claude Code, Cursor and Codex are built on).
var holderShares = map[string]uint32{
	"shared for reading and writing only": windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE,
	"shared for deletion too":             windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE,
}

// A rename over a file another handle holds open is refused, however that
// handle shares the file: the CI runners refused it both ways, which an
// in-place write was not when writing was shared. So a hold that outlasts
// ReplaceFile's retry -- five attempts, about a second -- is a refusal: the
// old bytes stay whole, no temporary file is left, and the caller is told.
func TestReplaceFileRefusedByAHoldThatOutlastsTheRetry(t *testing.T) {
	for name, share := range holderShares {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			holdOpen(t, path, share)

			start := time.Now()
			err := ReplaceFile(path, []byte("new"), 0o600)
			if err == nil {
				t.Fatal("the rename replaced a file another handle held open")
			}
			if waited := time.Since(start); waited < (replaceAttempts-1)*replaceDelay {
				t.Errorf("refused after %v, before the retry's %v", waited, (replaceAttempts-1)*replaceDelay)
			}
			if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "old" { // #nosec G304 -- test path
				t.Fatalf("the original after a refused rename: %q %v", got, rerr)
			}
			noStagedFiles(t, dir)
		})
	}
}

// A hold that ends inside the retry's window is waited out, and the file is
// replaced: the case of a host that has its settings open for the moment it
// takes to read them.
func TestReplaceFileWaitsOutABriefHold(t *testing.T) {
	for name, share := range holderShares {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			release := holdOpen(t, path, share)
			timer := time.AfterFunc(400*time.Millisecond, release)
			defer timer.Stop()

			if err := ReplaceFile(path, []byte("new"), 0o600); err != nil {
				t.Fatalf("a hold released after 400ms was not waited out: %v", err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "new" { // #nosec G304 -- test path
				t.Fatalf("after the hold ended: %q %v", got, err)
			}
			noStagedFiles(t, dir)
		})
	}
}

// The in-place write ReplaceFile replaced went through a hold that shared
// writing: the difference the retry exists for, kept here as the record.
func TestAnInPlaceWriteWentThroughAHoldSharingWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, path, holderShares["shared for reading and writing only"])
	if err := os.WriteFile(path, []byte("in place"), 0o600); err != nil {
		t.Fatalf("an in-place write under a hold sharing writing: %v", err)
	}
}
