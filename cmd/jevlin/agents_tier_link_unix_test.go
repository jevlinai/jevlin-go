//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A link at the tier file's name — a participant's dotfiles, or anything
// else — is not followed by search, so it is not followed here either:
// status reads it as unset, setting a band replaces the link with the
// setting rather than writing through it into whatever it names, and the
// reset removes the link itself, whether it names a file or a directory,
// leaving what it named untouched.
func TestAgentsTierTreatsALinkAsANameNotItsTarget(t *testing.T) {
	cfg, run := tierOnDisk(t)
	path := filepath.Join(filepath.Dir(cfg), tierFile)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(outside, []byte("balanced\n"), 0o600); err != nil { // #nosec G703 -- a path in this test's own temp dir
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}

	if code, out, _ := run("status"); code != exitOK || !strings.Contains(out, "unset") {
		t.Fatalf("status through a link: %d %q, want unset as search reads it", code, out)
	}
	if code, _, errOut := run("max"); code != exitOK {
		t.Fatalf("set max over a link: %d %q", code, errOut)
	}
	if b, _ := os.ReadFile(outside); string(b) != "balanced\n" { // #nosec G304 -- this test's own file
		t.Fatalf("setting a band wrote through the link: the target now holds %q", b)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the setting is not a regular file at its name: %v %v", info, err)
	}
	if got := searchTierDefault(cfg, configSearchFor(t, cfg), os.Getenv); got != "max" {
		t.Fatalf("search sends %q after `agents tier max`, want max", got)
	}

	dir := filepath.Join(t.TempDir(), "a-directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600); err != nil { // #nosec G703 -- a path in this test's own temp dir
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, path); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run("default"); code != exitOK {
		t.Fatalf("default over a link to a directory: %d %q", code, errOut)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("the link is still there after default: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(b) != "mine" { // #nosec G304 -- this test's own file
		t.Fatalf("default touched what the link named: %q %v", b, err)
	}
}
