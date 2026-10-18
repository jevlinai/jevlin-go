package main

// Where the running binary may live before setup and `agents install` record
// it. Every hook and skill calls that path by name and setup puts its
// directory on PATH, so it must stay this user's for as long as those files
// do. A temp directory is emptied by reboots and cleaners, and anyone who can
// write a directory on the way to the binary can put their own program at the
// recorded name once it is gone (or before); the agents would then run it as
// this user.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var errUnsafeBinaryLocation = errors.New("move this binary to a directory only you can write, such as ~/.local/bin, and run it from there")

// binaryLocationCheck is the shape of checkBinaryLocation, so a test can stand
// in for it.
type binaryLocationCheck func(exe string) (warnings []string, err error)

// checkBinaryLocation refuses exe when it is under the OS temp directory, or
// when it or any directory above it can be changed by another user. Both the
// path as given and the file it resolves to are checked: the hooks run the
// first, and the second is what that path opens.
//
// A component writable by a group that is not root-equivalent is a warning,
// not a refusal: whether its members are people other than this one is a
// question about the machine's accounts that a mode cannot answer, and the
// shared prefixes set up that way (Debian's /usr/local, Linuxbrew) are where
// package managers put binaries. The warnings are
// returned whether or not the check then refuses.
//
// On Windows only the temp-directory rule applies: writableByOthers has no
// owner or access list to read there (binary_location_windows.go).
func checkBinaryLocation(exe string) (warnings []string, err error) {
	if !filepath.IsAbs(exe) {
		return nil, fmt.Errorf("%s is not an absolute path; %w", exe, errUnsafeBinaryLocation)
	}
	exe = filepath.Clean(exe)
	candidates := []string{exe}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != exe {
		candidates = append(candidates, resolved)
	}
	for _, p := range candidates {
		if tmp := underTempDir(p); tmp != "" {
			return nil, fmt.Errorf("%s is under the temporary directory %s, which is emptied and which other users can write; %w", exe, tmp, errUnsafeBinaryLocation)
		}
	}
	// Nearest component first across both chains, so a refusal names the
	// directory closest to the binary that someone else can change.
	var chains [][]string
	for _, p := range candidates {
		var chain []string
		for dir := p; ; dir = filepath.Dir(dir) {
			chain = append(chain, dir)
			if filepath.Dir(dir) == dir {
				break
			}
		}
		chains = append(chains, chain)
	}
	checked := map[string]bool{}
	for i := 0; ; i++ {
		more := false
		for _, chain := range chains {
			if i >= len(chain) {
				continue
			}
			more = true
			dir := chain[i]
			if checked[dir] {
				continue
			}
			checked[dir] = true
			fi, err := os.Lstat(dir)
			if err != nil {
				return warnings, fmt.Errorf("cannot check who can change %s: %w", dir, err)
			}
			// A symlink cannot be rewritten in place, only replaced through
			// its directory, which the walk checks next; where it points is
			// the resolved candidate's chain.
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			refuse, warn := writableByOthers(fi)
			if refuse != "" {
				return warnings, fmt.Errorf("%s: %s %s; %w", exe, dir, refuse, errUnsafeBinaryLocation)
			}
			if warn != "" {
				warnings = append(warnings, fmt.Sprintf("%s %s, so its members could replace %s; if anyone else is in it, %s", dir, warn, exe, errUnsafeBinaryLocation))
			}
		}
		if !more {
			return warnings, nil
		}
	}
}

// vetBinaryLocation runs check on exe and prints its warnings to stderr under
// prog; the error, if any, is the caller's to print, since it stops the
// command.
func vetBinaryLocation(check binaryLocationCheck, exe, prog string, stderr io.Writer) error {
	warnings, err := check(exe)
	for _, w := range warnings {
		fmt.Fprintf(stderr, "%s: warning: %s\n", prog, w)
	}
	return err
}

// underTempDir names the temp directory p is inside, or "" when it is not.
func underTempDir(p string) string {
	tmp := filepath.Clean(os.TempDir())
	dirs := []string{tmp}
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil && resolved != tmp {
		dirs = append(dirs, resolved)
	}
	for _, d := range dirs {
		if filepath.Dir(d) == d {
			continue // a temp directory of "/" would refuse everything
		}
		if pathUnder(p, d) {
			return tmp
		}
	}
	return ""
}
