package main

// Which directory setup puts on PATH, and the line that makes the shell the
// person is typing into see it now. A shell reads its startup file only when
// it starts, so a profile or registry write never reaches the terminal setup
// ran in.

import (
	"path/filepath"
	"runtime"
)

// npmGlobal: is this process a global npm install's binary, started by npm's
// launcher?
func (r *setupRun) npmGlobal() bool {
	return classifyLaunch(r.exe, r.d.getenv("JEVLIN_LAUNCH")) == launchNPMGlobal
}

// npmCommandName is npm's command file in its global bin directory.
func npmCommandName() string {
	if runtime.GOOS == "windows" {
		return "jevlin.cmd"
	}
	return "jevlin"
}

// pathDirectory is the directory a person's PATH needs for the `jevlin` they
// type. For an npm install that is npm's global bin directory, where the shim
// is, never the package directory holding the real binary: putting that on
// PATH would make the raw binary the command, past its launcher, and a later
// setup or upgrade from it is refused. nativeDir is the directory for any
// other install.
//
// The npm directory is taken only when the command is there and the location
// passes the check the binary itself passes: a directory is written into a
// startup file that every later shell reads, so a guessed one, or one other
// users can write, is not. dir is "" with a note saying why otherwise.
func (r *setupRun) pathDirectory(nativeDir string) (dir, note string) {
	if !r.npmGlobal() {
		return nativeDir, ""
	}
	d := npmBinDir(r.exe, r.d.getenv)
	if d == "" {
		return "", "Setup could not tell which directory npm put the `jevlin` command in, so it writes no PATH line. " + r.npmByHand()
	}
	d = filepath.Clean(d)
	command := filepath.Join(d, npmCommandName())
	if !lexists(command) {
		return "", "Setup found no `jevlin` command in " + d + ", so it writes no PATH line. " + r.npmByHand()
	}
	if err := vetBinaryLocation(r.d.binaryLocationFn(), command, "jevlin setup", r.d.stderr); err != nil {
		return "", "Setup will not put " + d + " on PATH: " + err.Error() + ". " + r.npmByHand()
	}
	return d, ""
}

func (r *setupRun) npmByHand() string {
	return "Put that directory on PATH by hand (`npm prefix -g` names its prefix; the command is in its bin directory on macOS and Linux, in the prefix itself on Windows)."
}

// invocation is how this install is run by a person who may have no `jevlin`
// on PATH yet. A native binary is run by its path. An npm install is run
// through npm's command, never the package binary, which setup refuses when it
// is run around its launcher.
func (r *setupRun) invocation() string {
	if !r.npmGlobal() {
		return r.displayPath(r.exe)
	}
	if d := npmBinDir(r.exe, r.d.getenv); d != "" && lexists(filepath.Join(d, npmCommandName())) {
		return r.displayPath(filepath.Join(d, npmCommandName()))
	}
	if runtime.GOOS == "windows" {
		return `& "$(npm prefix -g)\jevlin.cmd"`
	}
	return `"$(npm prefix -g)/bin/jevlin"`
}

// printPathFix ends the shell step with the one command that fixes the
// current shell, unless dir is already on its PATH. fixOK is false for a shell
// whose syntax the command is not written in: it is told the directory.
func (r *setupRun) printPathFix(d shellDialect, dir string, onPath, fixOK bool) {
	if dir == "" || onPath {
		return
	}
	if !fixOK {
		r.printf("\nYour shell is not one setup writes a command for. To use jevlin in this terminal now, add %s to its PATH.\n", dir)
		return
	}
	r.printf("\nTo use jevlin in this terminal now, run:\n\n    %s\n\nA terminal opened later does not need it.\n", currentShellFix(d, dir))
}
