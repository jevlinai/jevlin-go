package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A binary under the OS temp directory is refused, by its own path and by
// the path a symlink elsewhere resolves to.
func TestBinaryLocationRefusesTempDir(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "jevlin")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := checkBinaryLocation(exe)
	if !errors.Is(err, errUnsafeBinaryLocation) || !strings.Contains(err.Error(), "temporary directory") {
		t.Fatalf("checkBinaryLocation(%q) = %v, want the temp-directory refusal", exe, err)
	}
	if _, err := checkBinaryLocation("jevlin"); !errors.Is(err, errUnsafeBinaryLocation) {
		t.Fatalf("a relative path was accepted: %v", err)
	}
}

// Setup run from a temp directory writes nothing: no PATH line, no hook, no
// config naming that binary.
func TestSetupRefusesBinaryInTempDir(t *testing.T) {
	s := newSetupSandbox(t)
	s.binaryLocation = checkBinaryLocation
	if err := os.MkdirAll(filepath.Dir(s.exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, s.root)
	code, _, errOut := s.run(nil, false, "-yes")
	if code != exitUsage || !strings.Contains(errOut, "~/.local/bin") {
		t.Fatalf("a binary in the temp directory was not refused: exit %d\n%s", code, errOut)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, s.root)) {
		t.Error("a refused binary location wrote something")
	}
}

// agents install refuses before planning anything; status still reports.
func TestAgentsInstallRefusesUnsafeBinaryLocation(t *testing.T) {
	m, ops := newFakeMachine("claude", "cursor")
	ops.binaryLocation = func(exe string) ([]string, error) {
		return nil, errors.New(exe + " is writable by every user; " + errUnsafeBinaryLocation.Error())
	}
	code, _, errOut := runAgents(t, ops, nil, "install", "-yes", "-config", testCfg)
	if code != exitUsage || !strings.Contains(errOut, "writable by every user") {
		t.Fatalf("install from an unsafe location was not refused: exit %d\n%s", code, errOut)
	}
	if len(m.files) != 0 {
		t.Errorf("a refused install wrote %d files", len(m.files))
	}
	if code, _, errOut := runAgents(t, ops, nil, "status", "-config", testCfg); code != exitOK {
		t.Errorf("status refused too: exit %d\n%s", code, errOut)
	}
}

// groupWarning is what checkBinaryLocation returns for a directory a group
// that is not root-equivalent can write.
func groupWarning(exe string) binaryLocationCheck {
	return func(string) ([]string, error) {
		return []string{"/usr/local is writable by its group staff (gid 50), so its members could replace " + exe}, nil
	}
}

// A warning is printed and setup goes on: a group-writable prefix such as
// Debian's /usr/local is where a binary is commonly put, and whether anyone
// else is in its group is not something a mode says.
func TestSetupWarnsAndContinuesOnAGroupWritableLocation(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.binaryLocation = groupWarning(s.exe)
	code, out, errOut := s.run(tty("n"), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("a warned location stopped setup: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "jevlin setup: warning: /usr/local is writable by its group staff") {
		t.Errorf("setup did not print the warning:\n%s", errOut)
	}
}

func TestAgentsInstallWarnsAndContinuesOnAGroupWritableLocation(t *testing.T) {
	m, ops := newFakeMachine("claude")
	ops.binaryLocation = groupWarning("/home/u/.jevlin/bin/jevlin")
	code, _, errOut := runAgents(t, ops, nil, "install", "-yes", "-config", testCfg)
	if code != exitOK {
		t.Fatalf("a warned location stopped agents install: exit %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "jevlin agents: warning: /usr/local is writable by its group staff") {
		t.Errorf("agents install did not print the warning:\n%s", errOut)
	}
	if len(m.files) == 0 {
		t.Error("a warned install wrote nothing")
	}
}

// prefer rewrites every installed skill with the running binary's path, so it
// is held to install's rule: refused before the preference file or a skill
// is written, warned and carried on otherwise. status still answers.
func TestAgentsPreferRefusesUnsafeBinaryLocation(t *testing.T) {
	m, ops := newFakeMachine("claude", "codex")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-yes", "-config", testCfg); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	before := maps.Clone(m.files)
	ops.binaryLocation = func(exe string) ([]string, error) {
		return nil, errors.New(exe + " is writable by every user; " + errUnsafeBinaryLocation.Error())
	}
	code, _, errOut := runAgents(t, ops, nil, "prefer", "off", "-config", testCfg)
	if code != exitUsage || !strings.Contains(errOut, "jevlin agents prefer: ") || !strings.Contains(errOut, "writable by every user") {
		t.Fatalf("prefer from an unsafe location was not refused: exit %d\n%s", code, errOut)
	}
	if !reflect.DeepEqual(before, m.files) {
		t.Error("a refused prefer changed a file")
	}
	if code, out, errOut := runAgents(t, ops, nil, "prefer", "status", "-config", testCfg); code != exitOK || !strings.Contains(out, "search default: on") {
		t.Errorf("prefer status refused too: exit %d\n%s%s", code, out, errOut)
	}

	ops.binaryLocation = groupWarning("/home/u/.jevlin/bin/jevlin")
	code, out, errOut := runAgents(t, ops, nil, "prefer", "off", "-config", testCfg)
	if code != exitOK || !strings.Contains(out, "search default: off") {
		t.Fatalf("a warned location stopped prefer: exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "jevlin agents prefer: warning: /usr/local is writable by its group staff") {
		t.Errorf("prefer did not print the warning:\n%s", errOut)
	}
}
