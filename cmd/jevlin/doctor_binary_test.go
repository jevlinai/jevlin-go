package main

// The `binary on PATH` check: the judging is pure (doctorBinaryCheck), so
// every verdict runs on every OS; the gatherer's "nothing to check" answer
// runs against a home that keeps no binary.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDoctorBinaryCheckNamesTheCopyThatShadowsThisOne(t *testing.T) {
	own := filepath.Join("/home/u/.jevlin", "bin", "jevlin")
	shim := filepath.Join("/home/u/npm", "jevlin.cmd")

	if c := doctorBinaryCheck(binaryFacts{Checked: true, Own: own, OnPath: own, Same: true}); c.Verdict != verdictOK {
		t.Errorf("the installation's own binary on PATH judged %s: %s", c.Verdict, c.Detail)
	}

	c := doctorBinaryCheck(binaryFacts{Checked: true, Own: own, OnPath: shim})
	if c.Verdict != verdictNo || !strings.Contains(c.Detail, shim) || !strings.Contains(c.Detail, own) {
		t.Errorf("a shadowing copy was not named: %s — %s", c.Verdict, c.Detail)
	}
	if !strings.Contains(c.Fix, filepath.Dir(own)) {
		t.Errorf("the fix does not name the directory to move forward: %s", c.Fix)
	}

	c = doctorBinaryCheck(binaryFacts{Checked: true, Own: own})
	if c.Verdict != verdictNo || !strings.Contains(c.Detail, own) {
		t.Errorf("an empty PATH answer was not explained: %s — %s", c.Verdict, c.Detail)
	}
	// Nothing on PATH answers a bare `jevlin`, so the fix cannot be one.
	if !strings.Contains(c.Fix, own+" setup") {
		t.Errorf("the fix does not run setup by the binary's full path: %s", c.Fix)
	}
	// A home with a space in it, as on Windows under C:\Users\Jane Doe: the
	// path is quoted the way setup shows one, so the shell does not split it.
	spaced := filepath.Join("/home/Jane Doe/.jevlin", "bin", "jevlin")
	c = doctorBinaryCheck(binaryFacts{Checked: true, Own: spaced})
	if quoted := displayPath(spaced); quoted == spaced || !strings.Contains(c.Fix, quoted+" setup") {
		t.Errorf("the fix does not quote a path with a space: %s", c.Fix)
	}
}

// doctor -config naming another installation's jevlin.toml diagnoses that
// installation, wallet and binary alike: the check names that one's
// bin/jevlin, not the binary of the home JEVLIN_HOME points at.
func TestDoctorBinaryCheckFollowsTheConfigsInstallation(t *testing.T) {
	name := "jevlin"
	if runtime.GOOS == "windows" {
		name = "jevlin.exe"
	}
	other := t.TempDir()
	bin := filepath.Join(other, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(bin, name)
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a LookPath target must be executable
		t.Fatal(err)
	}
	cfgPath := filepath.Join(other, setupConfigFile)
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JEVLIN_HOME", t.TempDir()) // keeps no binary
	t.Setenv("PATHEXT", ".EXE")
	t.Setenv("PATH", bin)

	var out, stderr bytes.Buffer
	_ = cmdDoctor([]string{"-config", cfgPath}, &out, &stderr)
	var row string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "binary on PATH") {
			row = line
		}
	}
	if row == "" || !strings.Contains(row, own) {
		t.Fatalf("doctor -config did not check that installation's binary %s:\n%s%s", own, out.String(), stderr.String())
	}
}

func TestGatherBinaryFactsChecksNothingWithoutAnOwnBinary(t *testing.T) {
	home := t.TempDir() // keeps no bin/jevlin
	f := gatherBinaryFacts(home)
	if f.Checked {
		t.Fatalf("an installation with no binary of its own was checked: %+v", f)
	}
	// And an unchecked fact adds no row.
	for _, check := range assembleDoctor(doctorFacts{Binary: f}) {
		if check.Name == "binary on PATH" {
			t.Fatal("an unchecked binary fact still produced a row")
		}
	}
}

// The gather's positive path, against real files and a real PATH: an
// inverted Same would otherwise pass every test in this file.
func TestGatherBinaryFactsSeesThroughPATH(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	name := "jevlin"
	if runtime.GOOS == "windows" {
		name = "jevlin.exe"
	}
	own := filepath.Join(bin, name)
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a LookPath target must be executable
		t.Fatal(err)
	}
	t.Setenv("PATHEXT", ".EXE") // what Windows LookPath consults; unread elsewhere
	t.Setenv("PATH", bin)
	f := gatherBinaryFacts(home)
	if !f.Checked || !f.Same || f.OnPath == "" {
		t.Fatalf("the installation's own binary on PATH was not recognized: %+v", f)
	}

	shadow := t.TempDir()
	other := filepath.Join(shadow, name)
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a LookPath target must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+bin)
	f = gatherBinaryFacts(home)
	if !f.Checked || f.Same || !samePath(f.OnPath, other) {
		t.Fatalf("a shadowing copy was not seen: %+v", f)
	}

	t.Setenv("PATH", "")
	f = gatherBinaryFacts(home)
	if !f.Checked || f.OnPath != "" {
		t.Fatalf("an empty PATH still resolved something: %+v", f)
	}
}
