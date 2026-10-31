//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// profileChoice is the startup file setup writes and the shell dialect it is
// written in. path is "" for a shell setup does not write for.
type profileChoice struct {
	dialect shellDialect
	path    string
	shell   string // the shell's name, for what setup says when it writes nothing
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
		return profileChoice{dialectPOSIX, filepath.Join(userHome, ".zshrc"), shell}
	case "bash":
		if goos != "darwin" {
			return profileChoice{dialectPOSIX, filepath.Join(userHome, ".bashrc"), shell}
		}
		login := filepath.Join(userHome, ".bash_profile")
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			if p := filepath.Join(userHome, name); exists(p) {
				login = p
				break
			}
		}
		if exists(login) && fileSourcesBashrc(login) {
			return profileChoice{dialectPOSIX, filepath.Join(userHome, ".bashrc"), shell}
		}
		return profileChoice{dialectPOSIX, login, shell}
	case "fish":
		config := filepath.Join(userHome, ".config")
		if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
			config = x
		}
		return profileChoice{dialectFish, filepath.Join(config, "fish", "config.fish"), shell}
	}
	return profileChoice{dialectPOSIX, "", strings.TrimSpace(shell)}
}

// fileSourcesBashrc: does a bash startup file read ~/.bashrc?
func fileSourcesBashrc(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- the participant's own bash startup file under their home
	return err == nil && strings.Contains(string(data), "bashrc")
}
