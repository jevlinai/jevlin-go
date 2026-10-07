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
	"os"
	"path/filepath"
)

var errUnsafeBinaryLocation = errors.New("move this binary to a directory only you can write, such as ~/.local/bin, and run it from there")

// checkBinaryLocation refuses exe when it is under the OS temp directory, or
// when it or any directory above it can be changed by another user. Both the
// path as given and the file it resolves to are checked: the hooks run the
// first, and the second is what that path opens.
func checkBinaryLocation(exe string) error {
	if !filepath.IsAbs(exe) {
		return fmt.Errorf("%s is not an absolute path; %w", exe, errUnsafeBinaryLocation)
	}
	exe = filepath.Clean(exe)
	candidates := []string{exe}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != exe {
		candidates = append(candidates, resolved)
	}
	for _, p := range candidates {
		if tmp := underTempDir(p); tmp != "" {
			return fmt.Errorf("%s is under the temporary directory %s, which is emptied and which other users can write; %w", exe, tmp, errUnsafeBinaryLocation)
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
				return fmt.Errorf("cannot check who can change %s: %w", dir, err)
			}
			// A symlink cannot be rewritten in place, only replaced through
			// its directory, which the walk checks next; where it points is
			// the resolved candidate's chain.
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if why := writableByOthers(fi); why != "" {
				return fmt.Errorf("%s: %s %s; %w", exe, dir, why, errUnsafeBinaryLocation)
			}
		}
		if !more {
			return nil
		}
	}
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
