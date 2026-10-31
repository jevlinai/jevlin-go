package main

// Where a person's PATH should point when jevlin was installed with npm.
//
// Under `npm install -g` the `jevlin` command is npm's shim, in npm's global
// bin directory, and it runs a launcher that starts the real binary inside
// the package. The binary's own directory is therefore never what a PATH line
// names: it would make the raw binary the thing a person types, bypassing the
// launcher, and a later setup or upgrade from it is refused as an npm copy run
// around its launcher. The directory is the one holding the shim.

import "strings"

// launchBinEnv is what the npm launcher exports for the directory holding its
// shim, for a global install only.
const launchBinEnv = "JEVLIN_LAUNCH_BIN"

// npmBinDir is the directory that holds this install's `jevlin` command, or
// "" when it cannot be told. The launcher's own answer wins; failing that it
// is derived from the package path. A value that is not an absolute path
// holding no control character is not an answer.
func npmBinDir(exe string, getenv func(string) string) string {
	if v := getenv(launchBinEnv); v != "" && plainAbsolutePath(v) {
		return v
	}
	return npmShimDirFromPackagePath(exe)
}

// plainAbsolutePath: absolute in either OS's spelling (judged by the text, not
// by the OS running, so the same value is judged the same everywhere), and
// printable.
func plainAbsolutePath(p string) bool {
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return strings.HasPrefix(p, "/") || isWindowsAbs(p)
}

// isWindowsAbs reads a drive path (C:\x, C:/x) or a UNC path whatever OS this
// is, so the derivation below is the same function everywhere and testable
// everywhere.
func isWindowsAbs(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') {
		return true
	}
	return strings.HasPrefix(p, `\\`)
}

// npmShimDirFromPackagePath derives npm's global bin directory from a path
// inside the package: <prefix>/bin for
// <prefix>/lib/node_modules/@jevlinsearch/jevlin/..., <prefix> for
// <prefix>\node_modules\@jevlinsearch\jevlin\.... Any other layout (a
// project's node_modules, a cache) has no global command and answers "".
func npmShimDirFromPackagePath(exe string) string {
	windows := isWindowsAbs(exe)
	sep := "/"
	if windows {
		sep = `\`
	}
	parts := strings.FieldsFunc(exe, func(c rune) bool { return c == '/' || c == '\\' })
	pkg := strings.Split(npmPackageName, "/")
	for i := 0; i+len(pkg) < len(parts); i++ {
		if !eqSeg(parts[i], "node_modules", windows) || !eqSeg(parts[i+1], pkg[0], windows) || !eqSeg(parts[i+2], pkg[1], windows) {
			continue
		}
		root := parts[:i]
		prefix := ""
		switch {
		case windows:
			prefix = strings.Join(root, sep)
			if strings.HasPrefix(exe, `\\`) {
				prefix = `\\` + prefix
			}
		default:
			if len(root) == 0 || root[len(root)-1] != "lib" {
				return ""
			}
			root = root[:len(root)-1]
			prefix = sep + strings.Join(root, sep)
			if len(root) == 0 {
				prefix = ""
			}
			return prefix + sep + "bin"
		}
		if len(root) == 0 {
			return ""
		}
		// A drive root keeps its separator: "C:" alone is the drive's
		// working directory.
		if len(root) == 1 && strings.HasSuffix(root[0], ":") {
			prefix = root[0] + sep
		}
		return prefix
	}
	return ""
}

func eqSeg(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}
