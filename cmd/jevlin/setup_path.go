package main

// Which directory setup puts on PATH, and the line that makes the shell the
// person is typing into see it now. A shell reads its startup file only when
// it starts, so a profile or registry write never reaches the terminal setup
// ran in.

import "path/filepath"

// npmGlobal: is this process a global npm install's binary, started by npm's
// launcher?
func (r *setupRun) npmGlobal() bool {
	return classifyLaunch(r.exe, r.d.getenv("JEVLIN_LAUNCH")) == launchNPMGlobal
}

// pathDirectory is the directory a person's PATH needs for the `jevlin` they
// type. For an npm install that is npm's global bin directory, where the shim
// is, never the package directory holding the real binary: putting that on
// PATH would make the raw binary the command, past its launcher, and a later
// setup or upgrade from it is refused. nativeDir is the directory for any
// other install. dir is "" with a note saying so when npm's directory cannot
// be told.
func (r *setupRun) pathDirectory(nativeDir string) (dir, note string) {
	if !r.npmGlobal() {
		return nativeDir, ""
	}
	if d := npmBinDir(r.exe, r.d.getenv); d != "" {
		return filepath.Clean(d), ""
	}
	return "", "Setup could not tell which directory npm put the `jevlin` command in, so it writes no PATH line. " +
		"Put that directory on PATH by hand (`npm prefix -g` names its prefix; the command is in its bin directory on macOS and Linux, in the prefix itself on Windows)."
}

// printPathFix ends the shell step with the one command that fixes the
// current shell, unless dir is already on its PATH.
func (r *setupRun) printPathFix(d shellDialect, dir string, onPath bool) {
	if dir == "" || onPath {
		return
	}
	r.printf("\nTo use jevlin in this terminal now, run:\n\n    %s\n\nA terminal opened later does not need it.\n", currentShellFix(d, dir))
}
