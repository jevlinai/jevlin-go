package main

// The lines setup puts in a shell's startup file, and the one line that
// makes the current shell see jevlin now. Three dialects: a POSIX shell, fish
// and PowerShell. Quoting is each shell's own, never Go's %q.

import (
	"path/filepath"
	"strings"
)

type shellDialect int

const (
	dialectPOSIX shellDialect = iota
	dialectFish
	dialectPowerShell
)

// fishQuote single-quotes s for fish, where only \ and ' are special inside
// single quotes.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// doubleQuoted is s inside double quotes for a POSIX shell or PowerShell,
// with the characters either would expand escaped.
func doubleQuoted(d shellDialect, s string) string {
	if d == dialectPowerShell {
		return `"` + strings.NewReplacer("`", "``", `"`, "`\"", "$", "`$").Replace(s) + `"`
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
}

// currentShellFix is the one command that puts dir on PATH for the shell it
// is typed into, and only that one: the profile covers the shells to come.
func currentShellFix(d shellDialect, dir string) string {
	switch d {
	case dialectFish:
		// The block's own line: for this session only. fish_add_path would
		// write fish_user_paths, a universal variable that persists and that
		// nothing here could take out again.
		return "set -gx PATH $PATH " + fishQuote(dir)
	case dialectPowerShell:
		return "$env:Path += " + doubleQuoted(d, ";"+dir)
	}
	inner := doubleQuoted(d, dir)
	return `export PATH="$PATH:` + inner[1:]
}

// dirOnPath: is dir one of the entries of a PATH value, through any link in
// either (node resolves the launcher's real path, so a prefix reached through
// a symlink is the same directory)?
func dirOnPath(pathVar, dir string) bool {
	if dir == "" {
		return false
	}
	for _, e := range filepath.SplitList(pathVar) {
		if e != "" && sameDir(e, dir) {
			return true
		}
	}
	return false
}

// envLinesFor are the lines of the profile block for a dialect. binDir is ""
// for a block that sets no PATH.
func envLinesFor(d shellDialect, binDir, cfgPath string) []string {
	if d == dialectFish {
		var lines []string
		if binDir != "" {
			q := fishQuote(binDir)
			lines = append(lines, "contains -- "+q+" $PATH; or set -gx PATH $PATH "+q)
		}
		return append(lines, "set -gx JEVLIN_CONFIG "+fishQuote(cfgPath))
	}
	lines := []string{}
	if binDir != "" {
		q := shellQuote(binDir)
		lines = append(lines, `case ":$PATH:" in *:`+q+`:*) ;; *) export PATH="$PATH:"`+q+` ;; esac`)
	}
	return append(lines, "export JEVLIN_CONFIG="+shellQuote(cfgPath))
}

// profileFiles are every file setup can have written an environment block to,
// for uninstall to look in, whichever shell is current now.
func profileFiles(userHome string, getenv func(string) string) []string {
	config := filepath.Join(userHome, ".config")
	if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		config = x
	}
	return []string{
		filepath.Join(userHome, ".zshrc"),
		filepath.Join(userHome, ".bashrc"),
		filepath.Join(userHome, ".bash_profile"),
		filepath.Join(userHome, ".bash_login"),
		filepath.Join(userHome, ".profile"),
		filepath.Join(config, "fish", "config.fish"),
	}
}
