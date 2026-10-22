package main

// The `binary on PATH` check: the judging is pure (doctorBinaryCheck), so
// every verdict runs on every OS; the gatherer's "nothing to check" answer
// runs against a home that keeps no binary.

import (
	"path/filepath"
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
