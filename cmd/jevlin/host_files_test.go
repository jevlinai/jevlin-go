package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// A host's removals wait on its writes. With one host's write failed, that
// host's removal is not made and the run says so; another host's removal,
// whose writes went through, is made; the failure is counted once.
func TestAHostsRemovalsWaitOnItsFailedWrite(t *testing.T) {
	m, ops := newFakeMachine()
	failing := "/home/u/.claude/settings.json"
	m.files["/home/u/.claude/skills/jevlin/SKILL.md"] = []byte("skill")
	m.files["/home/u/.cursor/skills/jevlin/SKILL.md"] = []byte("skill")
	write := ops.writeFile
	ops.writeFile = func(path string, b []byte, mode os.FileMode) error {
		if path == failing {
			return fs.ErrPermission
		}
		return write(path, b, mode)
	}
	p := &agentPlan{
		writes: []agentWrite{
			{surface: "Claude Code", path: failing, contents: []byte("{}\n"), mode: 0o600},
			{surface: "Cursor", path: "/home/u/.cursor/hooks.json", contents: []byte("{}\n"), mode: 0o600},
		},
		removes: []agentRemove{
			{surface: "Claude Code", path: "/home/u/.claude/skills/jevlin"},
			{surface: "Cursor", path: "/home/u/.cursor/skills/jevlin"},
		},
	}
	var out, errOut strings.Builder
	if failures := commitPlan(ops, p, &out, &errOut); failures != 1 {
		t.Fatalf("failures = %d, want the one failed write\n%s", failures, errOut.String())
	}
	if _, ok := m.files["/home/u/.claude/skills/jevlin/SKILL.md"]; !ok {
		t.Error("Claude Code's skill was removed after its hooks file could not be written")
	}
	if _, ok := m.files["/home/u/.cursor/skills/jevlin/SKILL.md"]; ok {
		t.Error("Cursor's skill was kept, though Cursor's write went through")
	}
	// Spelled the way the run spells it: tilde shortens a path only under
	// the OS's own separator, so on Windows the fake machine's POSIX path is
	// printed whole.
	if !strings.Contains(errOut.String(), "kept "+tilde(ops.home, "/home/u/.claude/skills/jevlin")) {
		t.Errorf("the held removal was not named:\n%s", errOut.String())
	}
}

// uninstall -binary with a hook file it cannot write keeps the binary: the
// hooks are still in the host's file and run it. Driven through the real
// setup and uninstall, against a settings.json the participant made
// read-only, and asserted on the files left on disk.
func TestUninstallBinaryKeepsTheBinaryAHookFileItCouldNotWriteStillRuns(t *testing.T) {
	posixOnly(t)
	s := newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", "jevlin")
	writeFileT(t, s.exe, "the binary")
	s.onPath["claude"] = true
	if code, out, errOut := s.run(nil, false, "-yes", "-with", "claude"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	paths := s.agentOps(false).paths(s.getenv)
	settings, skill := paths.claudeSettings, filepath.Dir(paths.claudeSkill)
	ours, err := os.ReadFile(settings) // #nosec G304 -- test path
	if err != nil || !strings.Contains(string(ours), "jevlin") {
		t.Fatalf("precondition: setup wrote no hooks into %s: %v", settings, err)
	}
	// A setting of the participant's own beside ours, so uninstall has to
	// rewrite the file rather than delete one that held only our hooks.
	var doc map[string]any
	if err := json.Unmarshal(ours, &doc); err != nil {
		t.Fatal(err)
	}
	doc["theme"] = "dark"
	before, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, settings, string(before))
	if err := os.Chmod(settings, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(settings, 0o600) })
	if f, err := os.OpenFile(settings, os.O_WRONLY, 0); err == nil { // #nosec G304 -- test path
		_ = f.Close()
		skipPermissionTest(t, "this account writes a read-only file anyway (root)")
	}

	code, out, errOut := s.uninstall(t, nil, false, "-yes", "-binary")
	if code == exitOK {
		t.Fatalf("uninstall -binary exited 0 with a hook file it could not write\n%s\n%s", out, errOut)
	}
	if !lexists(s.exe) {
		t.Fatalf("the binary was removed while the hooks that run it stay in %s\n%s\n%s", settings, out, errOut)
	}
	if after, err := os.ReadFile(settings); err != nil || string(after) != string(before) { // #nosec G304 -- test path
		t.Errorf("the read-only settings.json changed: %v", err)
	}
	if !lexists(skill) {
		t.Errorf("Claude Code's skill was removed though its hooks were not")
	}
	if !strings.Contains(errOut, "kept "+s.exe) || !strings.Contains(out, "The binary is still in place") {
		t.Errorf("the run did not say what it kept and why\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
}
