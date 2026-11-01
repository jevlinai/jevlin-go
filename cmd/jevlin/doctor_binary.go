package main

// Which binary a bare `jevlin` runs, next to the one this installation
// keeps. The npm launcher's shim standing earlier on PATH than <home>/bin
// is how a participant comes to run `jevlin setup` from the npm copy over
// a native install without meaning to — the sequence that used to double
// every hook. Setup appends <home>/bin to the user PATH and deliberately
// never reorders what is there, so the shadowing is named here instead.

import (
	"fmt"
	"os"
	"path/filepath"
)

// binaryFacts is the raw answer, gathered once and judged pure, the way
// every other doctor fact is.
type binaryFacts struct {
	Checked bool   // the installation keeps a binary at <home>/bin, or the running one stands in
	Own     string // that binary
	Running bool   // Own is the running executable: the installation keeps none at <home>/bin
	OnPath  string // what the bare name resolves to on PATH, "" for none
	Same    bool   // OnPath is Own: one file, or one cleaned path
}

// gatherBinaryFacts compares PATH's answer with the binary the installation
// at home keeps: the one doctor is diagnosing (doctorInstallationHome), so
// `doctor -config <another installation's jevlin.toml>` asks about that
// installation's binary, as its state access check does.
//
// An installation whose binary is not at <home>/bin (one that was put at
// ~/.local/bin, say) is checked through the running executable instead, so
// the check does not go silent there. Only a native process takes that
// fallback: the running binary of an npm install is the one inside the
// package, never what a person types, and the `npm command on PATH` check
// already answers for it. exe is the running executable with links resolved,
// "" when it cannot be told, and launch is JEVLIN_LAUNCH.
func gatherBinaryFacts(home, exe, launch string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) binaryFacts {
	own := ownBinaryAt(home, stat)
	running := false
	if own == "" {
		if exe == "" || classifyLaunch(exe, launch) != launchNative {
			return binaryFacts{} // a pure npm install keeps none; nothing to compare
		}
		own, running = exe, true
	}
	f := binaryFacts{Checked: true, Own: own, Running: running}
	onPath, err := lookPath("jevlin")
	if err != nil {
		return f
	}
	f.OnPath = onPath
	f.Same = samePath(onPath, own)
	if !f.Same {
		if a, errA := stat(onPath); errA == nil {
			if b, errB := stat(own); errB == nil {
				f.Same = os.SameFile(a, b)
			}
		}
	}
	return f
}

// doctorBinaryCheck judges the facts. Another copy answering first is a
// fact and a successful diagnosis (NO), not an error: everything the agents
// run names this installation's binary by its full path and is unaffected —
// it is the participant's own typed `jevlin` that goes elsewhere.
func doctorBinaryCheck(f binaryFacts) doctorCheck {
	c := doctorCheck{Name: "binary on PATH"}
	if f.Running {
		return doctorRunningBinaryCheck(f)
	}
	switch {
	case f.OnPath == "":
		c.Verdict = verdictNo
		c.Detail = "nothing on PATH answers `jevlin`; this installation's binary is " + f.Own
		// By its full path, quoted as setup quotes a path it shows: a bare
		// `jevlin` is exactly what does not run here.
		c.Fix = "open a new terminal, or run " + displayPath(f.Own) + " setup to put " + filepath.Dir(f.Own) + " on PATH"
	case f.Same:
		c.Verdict = verdictOK
		c.Detail = "`jevlin` runs this installation's own binary, " + f.Own
	default:
		c.Verdict = verdictNo
		c.Detail = fmt.Sprintf("`jevlin` runs %s, not this installation's %s — another copy stands earlier on PATH", f.OnPath, f.Own)
		c.Fix = "move " + filepath.Dir(f.Own) + " ahead of it in PATH, or remove the other copy; the agents' files name this installation's binary by path and are unaffected"
	}
	return c
}

// doctorRunningBinaryCheck judges the same facts when the installation keeps
// no binary at <home>/bin and the running one stands in for it. Setup puts
// <home>/bin on PATH, not the directory this binary is in, so the fixes name
// that directory instead of running setup.
func doctorRunningBinaryCheck(f binaryFacts) doctorCheck {
	c := doctorCheck{Name: "binary on PATH"}
	dir := filepath.Dir(f.Own)
	switch {
	case f.OnPath == "":
		c.Verdict = verdictNo
		c.Detail = "nothing on PATH answers `jevlin`; the running binary is " + f.Own
		c.Fix = "put " + dir + " on PATH and open a new terminal"
	case f.Same:
		c.Verdict = verdictOK
		c.Detail = "`jevlin` runs the running binary, " + f.Own
	default:
		c.Verdict = verdictNo
		c.Detail = fmt.Sprintf("`jevlin` runs %s, not the running binary %s — another copy stands earlier on PATH", f.OnPath, f.Own)
		c.Fix = "move " + dir + " ahead of it in PATH, or remove the other copy; the agents' files name the binary they were installed with by path and are unaffected"
	}
	return c
}

// npmShimFacts is the same question for an npm install, which keeps no binary
// at <home>/bin: the `jevlin` a person types is npm's shim, in npm's global
// bin directory, and it answers only when that directory is on PATH. Without
// this check doctor said nothing about PATH exactly when an npm install
// needs it said.
type npmShimFacts struct {
	Checked   bool         // this process is a global npm install's binary
	Dir       string       // npm's bin directory, "" when it cannot be told
	OnPath    string       // what the bare name resolves to on PATH, "" for none
	Here      bool         // OnPath is in Dir: this package's shim answers
	DirOnPath bool         // Dir is one of PATH's entries at all
	Dialect   shellDialect // the shell the fix is spelled for
}

// gatherNpmShimFacts looks when the launcher marked this process a global npm
// install, and also when the package binary was run by its own path, which is
// how the skills, the hooks and setup's closing lines run it and which carries
// no marker: a binary inside a global npm layout is that install either way.
// lookPath is exec.LookPath in production.
func gatherNpmShimFacts(exe string, goos string, getenv func(string) string, lookPath func(string) (string, error)) npmShimFacts {
	switch classifyLaunch(exe, getenv("JEVLIN_LAUNCH")) {
	case launchNPMGlobal:
	case launchNPMDirect:
		if npmShimDirFromPackagePath(exe) == "" {
			return npmShimFacts{}
		}
	default:
		return npmShimFacts{}
	}
	f := npmShimFacts{Checked: true, Dir: npmBinDir(exe, getenv), Dialect: dialectPOSIX}
	switch {
	case goos == "windows":
		f.Dialect = dialectPowerShell
	case filepath.Base(getenv("SHELL")) == "fish":
		f.Dialect = dialectFish
	}
	f.DirOnPath = dirOnPath(getenv("PATH"), f.Dir)
	if onPath, err := lookPath("jevlin"); err == nil {
		f.OnPath = onPath
		f.Here = f.Dir != "" && sameDir(filepath.Dir(onPath), f.Dir)
	}
	return f
}

// sameDir compares two directories through any link in them, so a shim found
// by a symlinked prefix is still the shim.
func sameDir(a, b string) bool {
	if samePath(a, b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && samePath(ra, rb)
}

// doctorNpmShimCheck judges the facts. A failure here is a diagnosis (NO),
// not an error: the agents' skills and hooks name the binary by its full path
// and are unaffected, and only the person's typed `jevlin` is.
func doctorNpmShimCheck(f npmShimFacts) doctorCheck {
	c := doctorCheck{Name: "npm command on PATH"}
	fix := "put npm's global bin directory (the one holding the `jevlin` command) on PATH"
	if f.Dir != "" {
		fix = "put " + f.Dir + " on PATH: run " + currentShellFix(f.Dialect, f.Dir) + " in this terminal, and add it to your shell's startup file (`jevlin setup` does that); a new terminal is needed after that"
	}
	switch {
	case f.OnPath == "":
		c.Verdict = verdictNo
		c.Detail = "nothing on PATH answers `jevlin`"
		if f.Dir != "" {
			c.Detail += "; npm's command is in " + f.Dir
		}
		c.Fix = fix
	case f.Here:
		c.Verdict = verdictOK
		c.Detail = "`jevlin` runs this package's npm command, " + f.OnPath
	case f.Dir == "":
		c.Verdict = verdictOK
		c.Detail = "`jevlin` resolves to " + f.OnPath + " (npm's own bin directory could not be told, so it is not compared)"
	case !f.DirOnPath:
		c.Verdict = verdictNo
		c.Detail = fmt.Sprintf("`jevlin` runs %s; this package's command is in %s, which is not on PATH", f.OnPath, f.Dir)
		c.Fix = fix
	default:
		c.Verdict = verdictNo
		c.Detail = fmt.Sprintf("`jevlin` runs %s, not this package's command in %s — another copy stands earlier on PATH", f.OnPath, f.Dir)
		c.Fix = "move " + f.Dir + " ahead of it in PATH, or remove the other copy; the agents' files name this installation's binary by path and are unaffected"
	}
	return c
}
