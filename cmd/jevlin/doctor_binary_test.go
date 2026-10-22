package main

// The `binary on PATH` check: the judging is pure (doctorBinaryCheck), so
// every verdict runs on every OS; the gatherer's "nothing to check" answer
// runs against a home that keeps no binary.

import (
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
}

func TestGatherBinaryFactsChecksNothingWithoutAnOwnBinary(t *testing.T) {
	home := t.TempDir() // keeps no bin/jevlin
	f := gatherBinaryFacts(func(k string) string {
		if k == "JEVLIN_HOME" {
			return home
		}
		return ""
	})
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
	if err := os.MkdirAll(bin, 0o755); err != nil {
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
	getenv := func(k string) string {
		if k == "JEVLIN_HOME" {
			return home
		}
		return ""
	}

	t.Setenv("PATHEXT", ".EXE") // what Windows LookPath consults; unread elsewhere
	t.Setenv("PATH", bin)
	f := gatherBinaryFacts(getenv)
	if !f.Checked || !f.Same || f.OnPath == "" {
		t.Fatalf("the installation's own binary on PATH was not recognized: %+v", f)
	}

	shadow := t.TempDir()
	other := filepath.Join(shadow, name)
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a LookPath target must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+bin)
	f = gatherBinaryFacts(getenv)
	if !f.Checked || f.Same || !samePath(f.OnPath, other) {
		t.Fatalf("a shadowing copy was not seen: %+v", f)
	}

	t.Setenv("PATH", "")
	f = gatherBinaryFacts(getenv)
	if !f.Checked || f.OnPath != "" {
		t.Fatalf("an empty PATH still resolved something: %+v", f)
	}
}
