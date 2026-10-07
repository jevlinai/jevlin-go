package main

import (
	"errors"
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
	err := checkBinaryLocation(exe)
	if !errors.Is(err, errUnsafeBinaryLocation) || !strings.Contains(err.Error(), "temporary directory") {
		t.Fatalf("checkBinaryLocation(%q) = %v, want the temp-directory refusal", exe, err)
	}
	if err := checkBinaryLocation("jevlin"); !errors.Is(err, errUnsafeBinaryLocation) {
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
	ops.binaryLocation = func(exe string) error {
		return errors.New(exe + " is writable by every user; " + errUnsafeBinaryLocation.Error())
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
