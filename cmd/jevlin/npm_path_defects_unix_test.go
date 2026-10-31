//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bash on macOS: any mention of "bashrc" in the login file is read as
// sourcing it, so the block goes to ~/.bashrc, which a macOS login shell does
// not read — the same failure as an unread profile, by another road.
func TestALoginFileThatOnlyMentionsBashrcIsNotTakenAsSourcingIt(t *testing.T) {
	for _, body := range []string{
		"# aliases used to live in ~/.bashrc; moved here\nexport A=1\n",
		"export HISTFILE=~/.bashrc_history\n",
		"#. ~/.bashrc\n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := chooseProfile(home, "darwin", func(k string) string {
			if k == "SHELL" {
				return "/bin/bash"
			}
			return ""
		}, lexists)
		if want := filepath.Join(home, ".bash_profile"); got.path != want {
			t.Errorf(".bash_profile %q does not source .bashrc, but setup chose %s", body, got.path)
		}
	}
}

// A block an earlier setup wrote to ~/.bashrc (an earlier setup chose
// .bashrc for any bash) is left beside the new ~/.bash_profile block on
// macOS. For an npm install that old block's PATH line names the package
// directory, the raw-binary PATH that makes setup and upgrade refuse.
func TestMacBashRerunLeavesTheEarlierBashrcBlock(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chooseProfile reads runtime.GOOS; the macOS branch runs on macOS")
	}
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	prefix := s.npmInstall("")
	shimDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(shimDir, "jevlin"), "shim")
	s.env[launchBinEnv] = shimDir
	s.env["SHELL"] = "/bin/bash"
	s.env["PATH"] = "/usr/bin"
	// What the earlier setup wrote for this npm install: the package directory.
	old := profileBlock(envLinesFor(dialectPOSIX, filepath.Dir(s.exe), s.cfgPath()))
	writeFileT(t, filepath.Join(s.userHome, ".bashrc"), "# mine\n"+old)
	writeFileT(t, filepath.Join(s.userHome, ".bash_profile"), "export A=1\n")

	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	bp := string(s.readFile(filepath.Join(s.userHome, ".bash_profile")))
	brc := string(s.readFile(filepath.Join(s.userHome, ".bashrc")))
	t.Logf(".bash_profile after setup:\n%s\n.bashrc after setup:\n%s", bp, brc)
	if strings.Contains(brc, profileMarkerStart) && strings.Contains(bp, profileMarkerStart) {
		mentioned := strings.Contains(out, ".bashrc")
		t.Errorf("two jevlin blocks after the rerun (.bashrc still names %s on PATH); setup mentioned .bashrc: %v", filepath.Dir(s.exe), mentioned)
	}
}

// Setup vets the binary's location (temp directory, other users'
// write) before recording it, and puts a directory on PATH for the same
// reason; the npm directory it now puts on PATH is never vetted.
func TestTheNpmDirectoryIsVettedLikeTheBinary(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.npmInstall("")
	open := filepath.Join(s.root, "shared")
	if err := os.MkdirAll(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil { // #nosec G302 -- a world-writable directory is the case under test, in the sandbox
		t.Fatal(err)
	}
	s.env[launchBinEnv] = open
	s.env["PATH"] = "/usr/bin"
	var asked []string
	s.binaryLocation = func(exe string) ([]string, error) {
		asked = append(asked, exe)
		fi, err := os.Lstat(exe)
		if err == nil {
			if refuse, _ := writableByOthers(fi); refuse != "" {
				return nil, errUnsafeBinaryLocation
			}
		}
		return nil, nil
	}
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	profile := string(s.readFile(s.profilePath()))
	if strings.Contains(profile, shellQuote(open)) {
		t.Errorf("setup put the world-writable %s on PATH (exit %d); the location check was asked about %q\nprofile:\n%s\n%s%s", open, code, asked, profile, out, errOut)
	}
}

// With no JEVLIN_LAUNCH_BIN (an older launcher, or the launcher found no
// shim), setup derives <prefix>/bin and writes it without looking for the
// command there; the launcher itself refuses to pass that same guess on.
func TestADerivedNpmDirectoryWithoutTheCommandIsNotWritten(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	prefix := s.npmInstall("") // no <prefix>/bin/jevlin is created
	s.env["PATH"] = "/usr/bin"
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "bin", "jevlin")); err == nil {
		t.Fatal("fixture: a shim exists")
	}
	if profile := string(s.readFile(s.profilePath())); strings.Contains(profile, shellQuote(filepath.Join(prefix, "bin"))) {
		t.Errorf("setup put %s on PATH though no jevlin command is there, and told this terminal to add it:\n%s", filepath.Join(prefix, "bin"), out)
	}
}

// The same, end to end: setup run for bash on macOS with a login file
// that mentions .bashrc in a comment, then a real bash login shell (what a
// macOS terminal starts) with that HOME. The block setup wrote is not read.
func TestMacBashLoginShellDoesNotSeeTheBlock(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS branch")
	}
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.env["SHELL"] = "/bin/bash"
	s.env["PATH"] = "/usr/bin"
	writeFileT(t, filepath.Join(s.userHome, ".bash_profile"), "# my old ~/.bashrc is no longer used\nexport FROM_LOGIN=1\n")
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	cmd := exec.Command("/bin/bash", "-l", "-c", `printf '%s|%s' "$FROM_LOGIN" "$JEVLIN_CONFIG"`)
	cmd.Env = []string{"HOME=" + s.userHome, "PATH=/usr/bin:/bin", "TERM=dumb"}
	got, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("setup said: %s", grepLine(out, "Added a jevlin block"))
	if string(got) != "1|"+s.cfgPath() {
		t.Errorf("bash -l read .bash_profile (FROM_LOGIN=1) but not the block: got %q, want JEVLIN_CONFIG=%s", got, s.cfgPath())
	}
}

func grepLine(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// doctor, npm's directory not on PATH at all while another jevlin
// answers: the row says the directory "stands" behind the other copy and
// tells the person to move it ahead, though it is not on PATH to move.
func TestDoctorDoesNotSayMoveAheadForADirectoryNotOnPath(t *testing.T) {
	pkgExe := "/opt/npmprefix/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin"
	env := map[string]string{"JEVLIN_LAUNCH": "npm:global", "PATH": "/usr/bin:/home/u/.jevlin/bin"}
	f := gatherNpmShimFacts(pkgExe, "linux", func(k string) string { return env[k] },
		func(string) (string, error) { return "/home/u/.jevlin/bin/jevlin", nil })
	c := doctorNpmShimCheck(f)
	if strings.Contains(c.Fix, "ahead of it") || strings.Contains(c.Detail, "earlier on PATH") {
		t.Errorf("%s is not on PATH (%s), yet: %s / fix: %s", f.Dir, env["PATH"], c.Detail, c.Fix)
	}
}

// uninstall takes out both blocks a rerun can leave,
// and only them.
func TestSoundUninstallRemovesBothMacBashBlocks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS branch")
	}
	s := newSetupSandbox(t)
	writeFileT(t, s.cfgPath(), "")
	old := profileBlock(envLinesFor(dialectPOSIX, "/opt/npmprefix/lib/node_modules/@jevlinsearch/jevlin/bin", s.cfgPath()))
	nw := profileBlock(envLinesFor(dialectPOSIX, "/opt/npmprefix/bin", s.cfgPath()))
	other := profileBlock(envLinesFor(dialectPOSIX, "/x/bin", "/elsewhere/jevlin.toml"))
	writeFileT(t, filepath.Join(s.userHome, ".bashrc"), "# mine\n"+old)
	writeFileT(t, filepath.Join(s.userHome, ".bash_profile"), "export A=1\n"+nw)
	writeFileT(t, filepath.Join(s.userHome, ".zshrc"), "# z\n"+other)
	if code, out, errOut := s.uninstall(t, nil, false, "-yes"); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for name, want := range map[string]string{".bashrc": "# mine\n", ".bash_profile": "export A=1\n", ".zshrc": "# z\n" + other} {
		if got := string(s.readFile(filepath.Join(s.userHome, name))); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// When the shell step is skipped for an npm install, the closing line names
// npm's command, not the package binary, which setup refuses to run.
func TestClosingCommandForSkippedNpmProfileNamesTheShim(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	prefix := s.npmInstall(filepath.Join(s.root, "npmprefix", "bin"))
	writeFileT(t, filepath.Join(prefix, "bin", "jevlin"), "shim")
	s.env["PATH"] = "/usr/bin"
	_, out, _ := s.run(tty(), true, "-yes", "-no-agents", "-no-profile")
	line := grepLine(out, "setup -config")
	if line == "" || strings.Contains(line, "node_modules") || !strings.Contains(line, filepath.Join(prefix, "bin", "jevlin")) {
		t.Errorf("the closing command %q does not name npm's command", line)
	}
	if err := checkSetupLaunch(s.exe, ""); err == nil {
		t.Error("fixture: setup should refuse the package binary run around its launcher")
	}
}

// For a shell setup does not write for, the "run this now" line is
// still POSIX `export`, which csh/tcsh and nushell reject.
func TestNoPosixFixLineForACshUser(t *testing.T) {
	for _, sh := range []string{"/bin/tcsh", "/bin/csh", "/opt/homebrew/bin/nu"} {
		s := newSetupSandbox(t)
		s.platform.claim("credits")
		s.npmInstall(filepath.Join(s.root, "npmprefix", "bin"))
		s.env["SHELL"] = sh
		s.env["PATH"] = "/usr/bin"
		_, out, _ := s.run(tty(), true, "-yes", "-no-agents")
		_, after, ok := strings.Cut(out, "To use jevlin in this terminal now, run:\n\n")
		if l, _, _ := strings.Cut(after, "\n"); ok && strings.HasPrefix(strings.TrimSpace(l), "export ") {
			t.Errorf("SHELL=%s is told to run, in this terminal now: %s", sh, strings.TrimSpace(l))
		}
	}
}
