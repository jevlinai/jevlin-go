//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheProfileFileIsTheOneTheShellReads(t *testing.T) {
	type files map[string]string
	for _, c := range []struct {
		name, goos, shell string
		have              files
		xdg               string
		wantFile          string
		wantDialect       shellDialect
	}{
		{"zsh", "darwin", "/bin/zsh", nil, "", ".zshrc", dialectPOSIX},
		{"zsh on linux", "linux", "/usr/bin/zsh", nil, "", ".zshrc", dialectPOSIX},
		{"bash on linux", "linux", "/bin/bash", nil, "", ".bashrc", dialectPOSIX},
		{"bash on macOS, nothing yet: the login file is created", "darwin", "/bin/bash", nil, "", ".bash_profile", dialectPOSIX},
		{"bash on macOS, a login file that does not read .bashrc", "darwin", "/bin/bash", files{".bash_profile": "export A=1\n"}, "", ".bash_profile", dialectPOSIX},
		{"bash on macOS, a login file that sources .bashrc", "darwin", "/bin/bash", files{".bash_profile": "[ -f ~/.bashrc ] && . ~/.bashrc\n"}, "", ".bashrc", dialectPOSIX},
		{"bash on macOS, only .profile: creating .bash_profile would hide it", "darwin", "/bin/bash", files{".profile": "export A=1\n"}, "", ".profile", dialectPOSIX},
		{"bash on macOS, .bash_profile first of three", "darwin", "/bin/bash", files{".profile": "x\n", ".bash_login": "y\n", ".bash_profile": "z\n"}, "", ".bash_profile", dialectPOSIX},
		{"fish", "linux", "/usr/bin/fish", nil, "", filepath.Join(".config", "fish", "config.fish"), dialectFish},
		{"fish on macOS", "darwin", "/opt/homebrew/bin/fish", nil, "", filepath.Join(".config", "fish", "config.fish"), dialectFish},
		{"fish with XDG_CONFIG_HOME", "linux", "/usr/bin/fish", nil, "XDG", "x", dialectFish},
		{"an unknown shell", "linux", "/bin/dash", files{".bashrc": "x\n"}, "", "", dialectPOSIX},
		{"no SHELL", "linux", "", files{".bashrc": "x\n"}, "", "", dialectPOSIX},
	} {
		home := t.TempDir()
		for name, body := range c.have {
			if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		xdg, want := "", filepath.Join(home, c.wantFile)
		if c.xdg != "" {
			xdg = filepath.Join(home, "xdg")
			want = filepath.Join(xdg, "fish", "config.fish")
		}
		if c.wantFile == "" {
			want = ""
		}
		got := chooseProfile(home, c.goos, func(k string) string {
			switch k {
			case "SHELL":
				return c.shell
			case "XDG_CONFIG_HOME":
				return xdg
			}
			return ""
		}, lexists)
		if got.path != want || (want != "" && got.dialect != c.wantDialect) {
			t.Errorf("%s: chose %q (%d), want %q (%d)", c.name, got.path, got.dialect, want, c.wantDialect)
		}
	}
}

// ── setup, end to end ─────────────────────────────────────────────────────

// npmInstall puts the sandbox's binary where npm puts a global install's
// (<prefix>/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin) and marks the run
// as that install's launcher would.
func (s *setupSandbox) npmInstall(launcherBin string) (prefix string) {
	s.t.Helper()
	prefix = filepath.Join(s.root, "npmprefix")
	s.exe = filepath.Join(prefix, "lib", "node_modules", "@jevlinsearch", "jevlin", "bin", "jevlin")
	if err := os.MkdirAll(filepath.Dir(s.exe), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(s.exe, []byte("not run by these tests"), 0o700); err != nil { // #nosec G306 -- a placeholder in the test's own sandbox
		s.t.Fatal(err)
	}
	s.env["JEVLIN_LAUNCH"] = "npm:global"
	if launcherBin != "" {
		s.env[launchBinEnv] = launcherBin
	}
	return prefix
}

// npmShim puts the command npm's launcher stands behind in dir.
func (s *setupSandbox) npmShim(dir string) {
	s.t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatal(err)
	}
	writeFileT(s.t, filepath.Join(dir, "jevlin"), "shim")
}

func TestSetupUnderNpmPutsNpmsBinDirectoryOnPathNotThePackages(t *testing.T) {
	for _, c := range []struct {
		name     string
		launcher bool
	}{{"the launcher names it", true}, {"derived from the package path", false}} {
		t.Run(c.name, func(t *testing.T) {
			s := newSetupSandbox(t)
			s.platform.claim("credits")
			prefix := s.npmInstall("")
			shimDir := filepath.Join(prefix, "bin")
			s.npmShim(shimDir)
			if c.launcher {
				s.env[launchBinEnv] = shimDir
			}
			s.env["PATH"] = "/usr/bin"
			code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
			if code != exitOK {
				t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
			}
			profile := string(s.readFile(s.profilePath()))
			if !strings.Contains(profile, shellQuote(shimDir)) {
				t.Errorf("the profile does not name npm's bin directory %s:\n%s", shimDir, profile)
			}
			if strings.Contains(profile, "node_modules") {
				t.Errorf("the profile names the package directory:\n%s", profile)
			}
			fix := "export PATH=\"$PATH:" + shimDir + "\""
			if !strings.Contains(out, "To use jevlin in this terminal now, run:\n\n    "+fix+"\n") {
				t.Errorf("setup did not end its shell step with the command for this shell:\n%s", out)
			}
		})
	}
}

// The process PATH is what the agent that ran setup sees, so it says nothing
// about a person's next terminal: the block is written the same either way,
// and the PATH only decides whether the command for this terminal is printed.
func TestSetupUnderNpmWritesTheBlockAndNoFixWhenTheDirectoryIsAlreadyOnPath(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	prefix := s.npmInstall(filepath.Join(s.root, "npmprefix", "bin"))
	shimDir := filepath.Join(prefix, "bin")
	s.npmShim(shimDir)
	s.env["PATH"] = "/usr/bin" + string(os.PathListSeparator) + shimDir
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	profile := string(s.readFile(s.profilePath()))
	if !strings.Contains(profile, shellQuote(shimDir)) || !strings.Contains(profile, "JEVLIN_CONFIG") {
		t.Errorf("the block should carry the PATH line and JEVLIN_CONFIG as for a native install:\n%s", profile)
	}
	if strings.Contains(out, "To use jevlin in this terminal now") {
		t.Errorf("setup offered a fix for a PATH that already has the directory:\n%s", out)
	}
}

func TestSetupWritesTheFishFileInFishSyntaxAndSaysHowToFixThisShell(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.env["SHELL"] = "/usr/bin/fish"
	s.env["PATH"] = "/usr/bin"
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	fishFile := filepath.Join(s.userHome, ".config", "fish", "config.fish")
	body := string(s.readFile(fishFile))
	if !strings.Contains(body, "set -gx JEVLIN_CONFIG ") || strings.Contains(body, "export ") || strings.Contains(body, "case ") {
		t.Errorf("config.fish is not in fish syntax:\n%s", body)
	}
	if !strings.Contains(out, "set -gx PATH $PATH ") || strings.Contains(out, "export PATH=") {
		t.Errorf("the fix for this shell is not fish's:\n%s", out)
	}
	if _, err := os.Lstat(filepath.Join(s.userHome, ".zshrc")); err == nil {
		t.Error("a fish user's run wrote .zshrc")
	}
}

func TestSetupWritesNothingForAShellItDoesNotKnowAndPrintsTheLines(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.env["SHELL"] = "/bin/dash"
	if err := os.WriteFile(filepath.Join(s.userHome, ".bashrc"), []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := s.run(tty(), true, "-yes", "-no-agents")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s%s", code, out, errOut)
	}
	if got := string(s.readFile(filepath.Join(s.userHome, ".bashrc"))); got != "# mine\n" {
		t.Errorf("a shell setup does not know had .bashrc rewritten:\n%s", got)
	}
	if !strings.Contains(out, "export JEVLIN_CONFIG=") || !strings.Contains(out, "Setup does not write for dash") {
		t.Errorf("setup did not print the lines and say it wrote none:\n%s", out)
	}
}

// A block setup wrote to a file it now chooses (.bash_profile, .profile,
// fish's config.fish) is removed by uninstall as the .zshrc one is, and the
// bytes around it stay.
func TestUninstallRemovesTheBlocksSetupWritesForBashLoginAndFish(t *testing.T) {
	posixOnly(t)
	s := newSetupSandbox(t)
	writeFileT(t, s.cfgPath(), "")
	posix := profileBlock(envLinesFor(dialectPOSIX, "/opt/npm/bin", s.cfgPath()))
	fish := profileBlock(envLinesFor(dialectFish, "/opt/npm/bin", s.cfgPath()))
	files := map[string]string{
		".bash_profile":            "export A=1\n" + posix,
		".profile":                 posix + "# tail\n",
		".config/fish/config.fish": "set -x B 2\n" + fish,
	}
	for name, body := range files {
		p := filepath.Join(s.userHome, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFileT(t, p, body)
	}
	if code, out, errOut := s.uninstall(t, nil, false, "-yes"); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for name, want := range map[string]string{
		".bash_profile":            "export A=1\n",
		".profile":                 "# tail\n",
		".config/fish/config.fish": "set -x B 2\n",
	} {
		got, err := os.ReadFile(filepath.Join(s.userHome, name)) // #nosec G304 -- sandbox profile
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
}

// A login file that really reads ~/.bashrc, in the spellings people write.
func TestALoginFileThatSourcesBashrcIsRecognized(t *testing.T) {
	for _, body := range []string{
		"[ -f ~/.bashrc ] && . ~/.bashrc\n",
		"if [ -r \"$HOME/.bashrc\" ]; then\n  . \"$HOME/.bashrc\"\nfi\n",
		"source ~/.bashrc\n",
		"test -f ${HOME}/.bashrc && source '${HOME}/.bashrc' # shared\n",
		"if [ -f ~/.bashrc ]; then . ~/.bashrc; fi\n",
	} {
		p := filepath.Join(t.TempDir(), ".bash_profile")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if !fileSourcesBashrc(p) {
			t.Errorf("a login file that sources .bashrc was not recognized:\n%s", body)
		}
	}
}

// dirOnPath answers for the real directory: a prefix reached through a link
// is the one node resolved the launcher to.
func TestDirOnPathSeesThroughALink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !dirOnPath(link+string(os.PathListSeparator)+"/usr/bin", real) || !dirOnPath(real, link) {
		t.Error("a directory on PATH through a symlink was not found")
	}
}
