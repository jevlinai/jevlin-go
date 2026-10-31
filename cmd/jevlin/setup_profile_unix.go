//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// profileChoice is the startup file setup writes and the shell dialect it is
// written in. path is "" for a shell setup does not write for.
type profileChoice struct {
	dialect shellDialect
	path    string
	shell   string // the shell's name, for what setup says when it writes nothing
	// fixOK: the shell reads the "run this now" line setup prints for the
	// dialect. A POSIX `export` is not csh's, tcsh's or nushell's.
	fixOK bool
}

// chooseProfile picks the file the shell $SHELL names reads, so the lines
// reach a shell the person starts next:
//
//   - zsh reads ~/.zshrc.
//   - bash on macOS starts as a login shell, which reads the first of
//     ~/.bash_profile, ~/.bash_login and ~/.profile that exists and never
//     ~/.bashrc; setup writes that file (~/.bash_profile when none exists),
//     or ~/.bashrc when that file already sources it.
//   - bash elsewhere reads ~/.bashrc.
//   - fish reads ~/.config/fish/config.fish ($XDG_CONFIG_HOME/fish/ when set
//     to an absolute path), and takes fish syntax.
//
// Any other shell, and no $SHELL, gets no file: guessing would write POSIX
// lines into a file its shell does not read or cannot parse.
func chooseProfile(userHome, goos string, getenv func(string) string, exists func(string) bool) profileChoice {
	shell := filepath.Base(getenv("SHELL"))
	if getenv("SHELL") == "" {
		shell = ""
	}
	switch shell {
	case "zsh":
		return profileChoice{dialectPOSIX, filepath.Join(userHome, ".zshrc"), shell, true}
	case "bash":
		if goos != "darwin" {
			return profileChoice{dialectPOSIX, filepath.Join(userHome, ".bashrc"), shell, true}
		}
		login := filepath.Join(userHome, ".bash_profile")
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			if p := filepath.Join(userHome, name); exists(p) {
				login = p
				break
			}
		}
		if exists(login) && fileSourcesBashrc(login) {
			return profileChoice{dialectPOSIX, filepath.Join(userHome, ".bashrc"), shell, true}
		}
		return profileChoice{dialectPOSIX, login, shell, true}
	case "fish":
		config := filepath.Join(userHome, ".config")
		if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
			config = x
		}
		return profileChoice{dialectFish, filepath.Join(config, "fish", "config.fish"), shell, true}
	}
	// No file for a shell setup does not know; the line for this terminal is
	// still POSIX's where the shell is of that family.
	return profileChoice{dialectPOSIX, "", strings.TrimSpace(shell), posixFamily[shell]}
}

// posixFamily are the shells that read `export NAME="value"`.
var posixFamily = map[string]bool{"sh": true, "dash": true, "ash": true, "ksh": true, "mksh": true, "zsh": true, "bash": true}

// bashrcSourced matches a command that reads ~/.bashrc: `. ~/.bashrc` or
// `source ~/.bashrc`, with the file spelled ~, $HOME or ${HOME}, optionally
// quoted, at the start of a command (after a space, `;`, `&`, `|`, `(`, or the
// `then` and `do` keywords, which end in a space).
var bashrcSourced = regexp.MustCompile(`(?:^|[\s;&|(])(?:\.|source)\s+["']?(?:~|\$HOME|\$\{HOME\})/\.bashrc["']?(?:[\s;&|)]|$)`)

// fileSourcesBashrc: does a bash startup file read ~/.bashrc? A comment, a
// variable such as HISTFILE=~/.bashrc_history, or a commented-out source line
// is not a command that reads it, and a login shell would then never see a
// block placed in ~/.bashrc.
func fileSourcesBashrc(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- the participant's own bash startup file under their home
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		if bashrcSourced.MatchString(line) {
			return true
		}
	}
	return false
}
