package main

// The `binary on PATH` check: the judging is pure (doctorBinaryCheck), so
// every verdict runs on every OS; the gatherer's "nothing to check" answer
// runs against a home that keeps no binary.

import (
	"bytes"
	"os"
	"os/exec"
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
// installation, state and binary alike: the check names that one's
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
	if err := os.WriteFile(cfgPath, []byte("schema = 1\n"), 0o600); err != nil {
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
	f := gatherBinaryFacts(home, "", "", exec.LookPath, os.Stat)
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
	f := gatherBinaryFacts(home, "", "", exec.LookPath, os.Stat)
	if !f.Checked || !f.Same || f.OnPath == "" {
		t.Fatalf("the installation's own binary on PATH was not recognized: %+v", f)
	}

	shadow := t.TempDir()
	other := filepath.Join(shadow, name)
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a LookPath target must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", shadow+string(os.PathListSeparator)+bin)
	f = gatherBinaryFacts(home, "", "", exec.LookPath, os.Stat)
	if !f.Checked || f.Same || !samePath(f.OnPath, other) {
		t.Fatalf("a shadowing copy was not seen: %+v", f)
	}

	t.Setenv("PATH", "")
	f = gatherBinaryFacts(home, "", "", exec.LookPath, os.Stat)
	if !f.Checked || f.OnPath != "" {
		t.Fatalf("an empty PATH still resolved something: %+v", f)
	}
}

func TestDoctorNpmShimCheckNamesTheDirectoryToAdd(t *testing.T) {
	dir := "/usr/local/bin"
	shim := dir + "/jevlin"

	if c := doctorNpmShimCheck(npmShimFacts{Checked: true, Dir: dir, OnPath: shim, Here: true}); c.Verdict != verdictOK || c.Fix != "" {
		t.Errorf("this package's shim on PATH judged %s (fix %q): %s", c.Verdict, c.Fix, c.Detail)
	}

	c := doctorNpmShimCheck(npmShimFacts{Checked: true, Dir: dir, Dialect: dialectPOSIX})
	if c.Verdict != verdictNo || !strings.Contains(c.Detail, dir) || !strings.Contains(c.Fix, `export PATH="$PATH:`+dir+`"`) {
		t.Errorf("an empty PATH answer: %s — %s — %s", c.Verdict, c.Detail, c.Fix)
	}
	if c := doctorNpmShimCheck(npmShimFacts{Checked: true, Dir: dir, Dialect: dialectFish}); !strings.Contains(c.Fix, "set -gx PATH $PATH '"+dir+"'") {
		t.Errorf("fish is not told fish's command: %s", c.Fix)
	}
	if c := doctorNpmShimCheck(npmShimFacts{Checked: true, Dir: `C:\Users\u\npm`, Dialect: dialectPowerShell}); !strings.Contains(c.Fix, `$env:Path += ";C:\Users\u\npm"`) {
		t.Errorf("PowerShell is not told its command: %s", c.Fix)
	}

	other := "/opt/other/jevlin"
	c = doctorNpmShimCheck(npmShimFacts{Checked: true, Dir: dir, OnPath: other, DirOnPath: true})
	if c.Verdict != verdictNo || !strings.Contains(c.Detail, other) || !strings.Contains(c.Detail, dir) || !strings.Contains(c.Fix, dir) {
		t.Errorf("another copy answering first: %s — %s — %s", c.Verdict, c.Detail, c.Fix)
	}

	// npm's directory could not be told: what answers is reported, and no
	// directory is invented.
	c = doctorNpmShimCheck(npmShimFacts{Checked: true, OnPath: other})
	if c.Verdict != verdictOK || !strings.Contains(c.Detail, other) {
		t.Errorf("an unknown directory with an answer: %s — %s", c.Verdict, c.Detail)
	}
	c = doctorNpmShimCheck(npmShimFacts{Checked: true})
	if c.Verdict != verdictNo || strings.Contains(c.Fix, "PATH=") {
		t.Errorf("an unknown directory and no answer: %s — %s", c.Verdict, c.Fix)
	}

	// An unchecked fact adds no row.
	for _, check := range assembleDoctor(doctorFacts{NpmShim: npmShimFacts{}}) {
		if check.Name == "npm command on PATH" {
			t.Fatal("an unchecked npm fact still produced a row")
		}
	}
	found := false
	for _, check := range assembleDoctor(doctorFacts{NpmShim: npmShimFacts{Checked: true, Dir: dir}}) {
		found = found || check.Name == "npm command on PATH"
	}
	if !found {
		t.Fatal("a global npm install got no PATH row")
	}
}

func TestGatherNpmShimFactsLooksOnlyAtAGlobalNpmInstall(t *testing.T) {
	pkgExe := "/usr/local/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin"
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	lookup := func(path string, err error) func(string) (string, error) {
		return func(string) (string, error) { return path, err }
	}
	// A native binary, or a project's copy, is not asked about.
	for _, c := range []struct{ exe, launch string }{
		{"/home/u/.jevlin/bin/jevlin", ""},
		{"/work/app/node_modules/@jevlinsearch/jevlin/bin/jevlin", "npm:local"},
	} {
		if f := gatherNpmShimFacts(c.exe, "linux", env(map[string]string{"JEVLIN_LAUNCH": c.launch}), lookup("/usr/local/bin/jevlin", nil)); f.Checked {
			t.Errorf("%s (%q) was checked: %+v", c.exe, c.launch, f)
		}
	}
	g := env(map[string]string{"JEVLIN_LAUNCH": "npm:global", "SHELL": "/usr/bin/fish"})
	f := gatherNpmShimFacts(pkgExe, "linux", g, lookup("/usr/local/bin/jevlin", nil))
	if !f.Checked || f.Dir != "/usr/local/bin" || !f.Here || f.Dialect != dialectFish {
		t.Errorf("the shim in npm's directory: %+v", f)
	}
	f = gatherNpmShimFacts(pkgExe, "linux", g, lookup("/elsewhere/jevlin", nil))
	if !f.Checked || f.Here || f.OnPath != "/elsewhere/jevlin" {
		t.Errorf("another copy first: %+v", f)
	}
	f = gatherNpmShimFacts(pkgExe, "linux", g, lookup("", exec.ErrNotFound))
	if !f.Checked || f.OnPath != "" || f.Here {
		t.Errorf("nothing on PATH: %+v", f)
	}
	f = gatherNpmShimFacts(`C:\u\npm\node_modules\@jevlinsearch\jevlin\bin\jevlin.exe`, "windows", g, lookup("", exec.ErrNotFound))
	if f.Dialect != dialectPowerShell || f.Dir != `C:\u\npm` {
		t.Errorf("windows: %+v", f)
	}
}

// An installation whose binary is not at <home>/bin (the 0.6.x layout put it
// in ~/.local/bin) is checked through the running executable, so the row does
// not vanish. The table drives the real gatherer with a fake PATH lookup and a
// stat that finds nothing, and the real judge on its facts.
func TestBinaryOnPathWhenTheInstallationKeepsNoBinaryAtHome(t *testing.T) {
	exe := "/home/u/.local/bin/jevlin"
	shadow := "/opt/other/bin/jevlin"
	found := func(p string) func(string) (string, error) {
		return func(string) (string, error) { return p, nil }
	}
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	noFile := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	for _, tc := range []struct {
		name     string
		launch   string
		exe      string
		lookPath func(string) (string, error)
		wantRow  bool
		verdict  doctorVerdict
		detail   []string // each must appear in the detail
	}{
		{"on PATH", "", exe, found(exe), true, verdictOK,
			[]string{"the running binary", exe}},
		{"PATH resolves to another file", "", exe, found(shadow), true, verdictNo,
			[]string{shadow, exe, "the running binary"}},
		{"nothing on PATH", "", exe, none, true, verdictNo,
			[]string{"nothing on PATH", "the running binary is " + exe}},
		{"a global npm launch is left to the npm row", "npm:global", exe, found(shadow), false, doctorVerdict(""), nil},
		{"a package binary run by its path is left to the npm row", "",
			"/opt/p/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin", found(shadow), false, doctorVerdict(""), nil},
		{"no executable to go on", "", "", found(shadow), false, doctorVerdict(""), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gatherBinaryFacts(t.TempDir(), tc.exe, tc.launch, tc.lookPath, noFile)
			if f.Checked != tc.wantRow {
				t.Fatalf("Checked = %v, want %v: %+v", f.Checked, tc.wantRow, f)
			}
			var row *doctorCheck
			for _, c := range assembleDoctor(doctorFacts{Binary: f}) {
				if c.Name == "binary on PATH" {
					c := c
					row = &c
				}
			}
			if !tc.wantRow {
				if row != nil {
					t.Fatalf("a row appeared where the npm row answers: %s — %s", row.Verdict, row.Detail)
				}
				return
			}
			if row == nil {
				t.Fatal("no row: the check went silent")
			}
			if row.Verdict != tc.verdict {
				t.Fatalf("verdict %s, want %s: %s", row.Verdict, tc.verdict, row.Detail)
			}
			for _, want := range tc.detail {
				if !strings.Contains(row.Detail, want) {
					t.Errorf("detail %q lacks %q", row.Detail, want)
				}
			}
			if strings.Contains(row.Detail, "this installation's") {
				t.Errorf("the <home>/bin wording leaked into the fallback: %q", row.Detail)
			}
			if row.Verdict == verdictNo && row.Fix == "" {
				t.Error("a NO with no fix")
			}
		})
	}
}

// With the binary at <home>/bin the lines are the ones they always were,
// whatever the running executable is.
func TestTheHomeBinWordingIsUnchangedBesideAFallbackBinary(t *testing.T) {
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
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a stat target
		t.Fatal(err)
	}
	f := gatherBinaryFacts(home, "/home/u/.local/bin/jevlin", "", func(string) (string, error) { return own, nil }, os.Stat)
	if !f.Checked || f.Running || f.Own != own || !f.Same {
		t.Fatalf("the installation's own binary was not preferred: %+v", f)
	}
	if c := doctorBinaryCheck(f); c.Detail != "`jevlin` runs this installation's own binary, "+own {
		t.Errorf("the <home>/bin line changed: %q", c.Detail)
	}
}
