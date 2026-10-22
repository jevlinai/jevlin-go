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
	"os/exec"
	"path/filepath"
)

// binaryFacts is the raw answer, gathered once and judged pure, the way
// every other doctor fact is.
type binaryFacts struct {
	Checked bool   // the installation keeps a binary at <home>/bin
	Own     string // that binary
	OnPath  string // what the bare name resolves to on PATH, "" for none
	Same    bool   // OnPath is Own: one file, or one cleaned path
}

func gatherBinaryFacts(getenv func(string) string) binaryFacts {
	home := getenv("JEVLIN_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil || userHome == "" {
			return binaryFacts{}
		}
		home = filepath.Join(userHome, ".jevlin")
	}
	own := ownBinaryAt(home, os.Stat)
	if own == "" {
		return binaryFacts{} // a pure npm install keeps none; nothing to compare
	}
	f := binaryFacts{Checked: true, Own: own}
	onPath, err := exec.LookPath("jevlin")
	if err != nil {
		return f
	}
	f.OnPath = onPath
	f.Same = samePath(onPath, own)
	if !f.Same {
		if a, errA := os.Stat(onPath); errA == nil {
			if b, errB := os.Stat(own); errB == nil {
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
	switch {
	case f.OnPath == "":
		c.Verdict = verdictNo
		c.Detail = "nothing on PATH answers `jevlin`; this installation's binary is " + f.Own
		c.Fix = "open a new terminal, or run jevlin setup to put " + filepath.Dir(f.Own) + " on PATH"
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
