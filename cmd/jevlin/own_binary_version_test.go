package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

// setup never copies itself to <home>/bin, so the native copy moves only by
// `jevlin upgrade`, and an npm copy can be a release ahead of it. The swap
// must not point that copy's hooks and skill at a binary that predates them:
// it is made only when the home's binary is known to be no older, and the
// running copy keeps its own path, with the passed-over one named, whenever
// either version is unknown.
func TestAdoptOwnBinarySwapsOnlyToABinaryNoOlderThanTheRunningCopy(t *testing.T) {
	home := "/home/u/.jevlin"
	exe := "jevlin"
	if runtime.GOOS == "windows" {
		exe = "jevlin.exe"
	}
	own := filepath.Join(home, "bin", exe)
	npmExe := filepath.Join("/home/u/npm/node_modules/jevlin/bin", exe)
	stat := func(p string) (os.FileInfo, error) {
		if slash(p) == slash(own) {
			return fakeFileInfo{name: exe}, nil
		}
		return nil, fs.ErrNotExist
	}
	npmEnv := func(k string) string {
		if k == "JEVLIN_LAUNCH" {
			return "npm:global"
		}
		return ""
	}
	ownCfg, err := filepath.Abs(filepath.Join(home, "jevlin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, own, running string
		swap               bool
	}{
		{"the same release", "0.5.0", "0.5.0", true},
		{"the home's binary newer", "0.5.1", "0.5.0", true},
		{"the home's binary a patch older", "0.4.3", "0.4.4", false},
		{"the home's binary a minor older", "0.4.9", "0.5.0", false},
		{"the home's binary unknown", "", "0.5.0", false},
		{"the running copy unknown", "0.5.0", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := func(raw string) (selfupdate.Version, bool) {
				if raw == "" {
					return selfupdate.Version{}, false
				}
				v, err := selfupdate.ParseVersion(raw)
				if err != nil {
					t.Fatal(err)
				}
				return v, true
			}
			versionOf := func(p string) (selfupdate.Version, bool) {
				switch slash(p) {
				case slash(own):
					return release(tc.own)
				case slash(npmExe):
					return release(tc.running)
				}
				t.Fatalf("asked the version of %q", p)
				return selfupdate.Version{}, false
			}
			got := adoptOwnBinary(binEntry{command: npmExe, cfg: ownCfg}, home, npmEnv, stat, versionOf)
			if tc.swap {
				if got.command != own || got.ownBinary != npmExe || got.ownOlder != "" {
					t.Fatalf("want the swap: command %q, ownBinary %q, ownOlder %q", got.command, got.ownBinary, got.ownOlder)
				}
				if note := got.ownOlderNote(); note != "" {
					t.Fatalf("a swap still explains itself: %q", note)
				}
				return
			}
			// The ownership reference keeps the home's binary either way,
			// so its hooks are still replaced rather than doubled.
			if got.command != npmExe || got.ownBinary != own || got.ownOlder != own {
				t.Fatalf("want the running copy kept: command %q, ownBinary %q, ownOlder %q", got.command, got.ownBinary, got.ownOlder)
			}
			if note := got.ownOlderNote(); !strings.Contains(note, own+" upgrade") {
				t.Fatalf("the note does not say how to use the home's binary: %q", note)
			}
		})
	}
}

// binaryReleaseVersion reads a copy's release from its build information,
// where a release build carries its tag as the main module's version (v0.1.0
// through v0.4.3 all do; -trimpath drops the -ldflags that set main.version).
// Anything that is not a jevlin release answers false.
func TestABinarysReleaseIsItsTaggedModuleVersion(t *testing.T) {
	build := func(path, version string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: path, Version: version}}
	}
	if got, ok := releaseOfBuild(build(jevlinModulePath, "v0.4.3")); !ok || got != (selfupdate.Version{Minor: 4, Patch: 3}) {
		t.Fatalf("a tagged release read as %v, %v", got, ok)
	}
	for _, info := range []*debug.BuildInfo{
		nil,
		build(jevlinModulePath, "(devel)"),
		build(jevlinModulePath, "v0.4.4-0.20261024112354-866106b2cd90"),
		build(jevlinModulePath, "v0.4.3+dirty"),
		build(jevlinModulePath, "0.4.3"),
		build("example.com/other", "v9.0.0"),
	} {
		if got, ok := releaseOfBuild(info); ok {
			t.Errorf("%+v read as release %v", info, got)
		}
	}
	// This test binary is a jevlin build, but no release.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := binaryReleaseVersion(self); ok {
		t.Errorf("the test binary read as release %v", got)
	}
	script := filepath.Join(t.TempDir(), "jevlin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := binaryReleaseVersion(script); ok {
		t.Errorf("a script read as release %v", got)
	}
}
