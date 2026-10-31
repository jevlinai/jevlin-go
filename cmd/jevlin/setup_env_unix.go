//go:build !windows

package main

// POSIX: owner-only is a mode, and the environment is a profile block.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

// restrictToOwner takes group and other access away. A directory is 0700,
// which is what the auth store requires; a file keeps its own
// owner bits (a read-only key stays read-only) and loses the rest.
func restrictToOwner(path string, dir bool) error {
	if dir {
		return os.Chmod(path, 0o700) // #nosec G302 G703 -- a directory: owner rwx is the most restrictive mode it can be used with; path is inside the installation directory setup owns
	}
	info, err := os.Lstat(path) // #nosec G703 -- path is inside the installation directory setup owns
	if err != nil {
		return err
	}
	return os.Chmod(path, info.Mode().Perm()&0o700) // #nosec G703 -- as above
}

// ownedByCurrentUser refuses an object whose owner is not this process's
// user. info is the object's Lstat.
func ownedByCurrentUser(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: its owner cannot be read", path)
	}
	if uid := os.Getuid(); int64(st.Uid) != int64(uid) {
		return fmt.Errorf("%s is owned by uid %d, not the current user (uid %d)", path, st.Uid, uid)
	}
	return nil
}

// systemUserEnvironment has no POSIX meaning: the profile block is the
// environment setup leaves.
func systemUserEnvironment() userEnvironment { return nil }

func (r *setupRun) environmentStep() (code int) {
	choice := chooseProfile(r.d.userHome, runtime.GOOS, r.d.getenv, lexists)
	binDir, pathNote := r.pathDirectory(filepath.Dir(r.exe))
	onPath := dirOnPath(r.d.getenv("PATH"), binDir)
	// An npm install whose bin directory is already on PATH, or cannot be
	// told, gets no PATH line; the profile then sets only JEVLIN_CONFIG.
	lineDir := binDir
	if r.npmGlobal() && (binDir == "" || onPath) {
		lineDir = ""
	}
	lines := envLinesFor(choice.dialect, lineDir, r.cfgPath)

	r.say("Shell environment")
	if r.leftForOtherInstallation("shell profile", "Your shell profile belongs to that one", "pointing PATH and JEVLIN_CONFIG here would repoint your real environment at this installation") {
		return exitOK
	}
	defer func() {
		if code == exitOK && !r.dry {
			r.printPathFix(choice.dialect, binDir, onPath)
		}
	}()
	if pathNote != "" {
		r.printf("%s\n", pathNote)
	}
	if r.npmGlobal() && onPath {
		r.printf("%s is already on PATH, so no PATH line is written.\n", binDir)
	}
	r.printf("These lines make the other commands short. Your key is not among them: a\n" +
		"search reads it from the stored credentials file (or JEVLIN_API_KEY, if a\n" +
		"shell exports one, which then wins):\n\n")
	for _, l := range lines {
		r.printf("    %s\n", l)
	}

	candidate := choice.path
	if candidate == "" {
		if choice.shell == "" {
			r.printf("$SHELL is not set, so setup does not know which file your shell reads. Add the lines above to its startup file by hand.\n")
		} else {
			r.printf("Setup does not write for %s. Add the lines above to its startup file by hand.\n", choice.shell)
		}
		return exitOK
	}
	if r.noProfile {
		r.printf("Left your shell profile alone (-no-profile).\n")
		r.skip("shell profile")
		return exitOK
	}
	target, existing, mode, err := profileTarget(candidate)
	if err != nil {
		r.printf("Not touching %s: %v. Add the lines above by hand.\n", candidate, err)
		return exitOK
	}
	next, err := rewriteProfile(existing, profileBlock(lines))
	if errors.Is(err, errProfileMalformed) {
		r.printf("Not touching %s: %v. Add the lines above to it by hand, between one start and one end marker:\n    %s\n    %s\n",
			candidate, err, profileMarkerStart, profileMarkerEnd)
		return exitOK
	}
	if bytes.Equal(next, existing) {
		r.shortCommands = true
		r.printf("%s already has them.\n", candidate)
		return exitOK
	}
	if r.dry {
		r.printf("(dry run) would write a jevlin block to %s\n", candidate)
		return exitOK
	}
	if !r.d.interactive && !r.yes {
		r.printf("Not an interactive shell — not touching %s (pass -yes to add them).\n", candidate)
		r.skip("shell profile")
		return exitOK
	}
	add, err := r.ask("Add them to " + tilde(r.d.userHome, candidate) + "?")
	if err != nil {
		return r.abort("your shell profile was not touched and the coding agents were not set up")
	}
	if !add {
		r.say("Left your shell profile alone")
		return exitOK
	}
	// fish's directory may not exist yet; every other profile sits in the home.
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		r.printf("Could not write %s: %v. Add the lines above by hand.\n", candidate, err)
		return exitOK
	}
	if err := fsx.WriteFileAtomic(filepath.Dir(target), filepath.Base(target), next, mode); err != nil {
		r.printf("Could not write %s: %v. Add the lines above by hand.\n", candidate, err)
		return exitOK
	}
	r.changed = true
	r.shortCommands = true
	r.say("Added a jevlin block to " + candidate)
	r.printf("  open a new shell, or: source %s\n", shellQuote(candidate))
	return exitOK
}
