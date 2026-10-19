package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// noStagedHostFiles fails if a temporary file is left in dir.
func noStagedHostFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary files left in %s: %v %v", dir, left, err)
	}
}

// A participant's settings.json kept in a dotfiles repository, with a link
// from ~/.claude: the real agents install writes our hooks into the file the
// link names, leaves the link a link, keeps the participant's own settings
// and the file's mode, and leaves no temporary file in either directory
// (issue #37).
func TestAgentsInstallWritesThroughALinkedSettingsFile(t *testing.T) {
	m := newTwoInstallations(t)
	dotfiles := filepath.Join(filepath.Dir(m.ops.home), "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dotfiles, "claude-settings.json")
	if err := os.WriteFile(target, []byte(`{"theme": "dark"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil { // #nosec G302 -- the participant's readable mode, which the test checks is kept
		t.Fatal(err)
	}
	link := m.paths.claudeSettings
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		skipPermissionTest(t, "this machine will not make a symbolic link: "+err.Error())
	}

	if code, out := m.agents("install", "-yes", "-config", m.first, "-client", "claude"); code != exitOK {
		t.Fatalf("agents install: exit %d\n%s", code, out)
	}

	info, err := os.Lstat(link)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("~/.claude/settings.json is no longer a link: %v %v", info, err)
	}
	if said, err := os.Readlink(link); err != nil || said != target {
		t.Fatalf("the link now says %q (%v), want %q", said, err, target)
	}
	raw, err := os.ReadFile(target) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the target is not JSON: %v\n%s", err, raw)
	}
	if doc["theme"] != "dark" || doc["hooks"] == nil {
		t.Fatalf("the target lost the participant's setting or got no hooks:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("the target's mode: %v %v, want its 0644 kept", info.Mode().Perm(), err)
		}
	}
	noStagedHostFiles(t, dotfiles)
	noStagedHostFiles(t, filepath.Dir(link))
}

// A host file that did not exist is created with the mode the plan gives
// it, 0600, as the in-place write created it.
func TestAgentsInstallCreatesANewHostFileOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps no POSIX permission bits")
	}
	m := newTwoInstallations(t)
	if code, out := m.agents("install", "-yes", "-config", m.first, "-client", "claude"); code != exitOK {
		t.Fatalf("agents install: exit %d\n%s", code, out)
	}
	info, err := os.Stat(m.paths.claudeSettings)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new settings.json: %v %v, want 0600", info.Mode().Perm(), err)
	}
	noStagedHostFiles(t, filepath.Dir(m.paths.claudeSettings))
}
