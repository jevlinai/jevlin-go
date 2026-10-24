package main

// Who owns the binary that is running: the one classifier setup, uninstall
// -binary and upgrade all consult, so no two of them can disagree about
// whether a copy is npm's.
//
// The npm launcher is the first authority. It sets JEVLIN_LAUNCH to
// npm:global, npm:local or npm:unknown for the binary it starts; nothing else
// sets it. A path inside npm's temporary exec cache is npm's whatever the
// variable says, because the path is the proof. With no marker, a binary
// inside node_modules was run around the launcher, and a binary beside npm's
// package files (package.json naming this package, install.js, and the
// launcher jevlin.js in its own directory) is the package's copy
// wherever that package lives. A partial set of those files is ambiguous. A
// launcher started with JEVLIN_BINARY still sets the marker, so that
// copy is treated as npm's: conservative, and never destructive.
//
// Each consumer decides what a kind permits. Setup accepts a global npm
// install, since hooks pointing at it survive; uninstall -binary and upgrade
// accept only a native copy, because replacing or removing bytes npm owns is
// npm's job.

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

// launchKind is what the classifier concluded.
type launchKind int

const (
	launchNative     launchKind = iota // no npm authority or layout
	launchNPMGlobal                    // JEVLIN_LAUNCH=npm:global
	launchNPMLocal                     // JEVLIN_LAUNCH=npm:local
	launchNPMUnknown                   // the launcher could not tell, or an unrecognized marker
	launchNPMCache                     // npm's temporary exec cache
	launchNPMDirect                    // inside node_modules, run around the launcher
	launchNPMLayout                    // beside npm's package files, no marker
	launchAmbiguous                    // some of npm's package files, not all
)

const npmPackageName = "@jevlinsearch/jevlin"

// classifyLaunch decides who owns exe, given the launcher's marker.
func classifyLaunch(exe, marker string) launchKind {
	segments := strings.FieldsFunc(exe, func(c rune) bool { return c == '/' || c == '\\' })
	for _, seg := range segments {
		switch strings.ToLower(seg) {
		case "_npx", "_cacache", "npm-cache":
			return launchNPMCache
		}
	}
	switch marker {
	case "npm:global":
		return launchNPMGlobal
	case "npm:local":
		return launchNPMLocal
	case "":
	default:
		return launchNPMUnknown
	}
	for _, seg := range segments {
		if strings.EqualFold(seg, "node_modules") {
			return launchNPMDirect
		}
	}
	return npmLayoutKind(exe)
}

// ownBinaryAt is the binary an installation keeps at <home>/bin — a regular
// file at the one name setup installs to and uninstall owns (ownedBinary) —
// or "" when there is none there or home is unknown.
func ownBinaryAt(home string, stat func(string) (os.FileInfo, error)) string {
	if home == "" {
		return ""
	}
	name := "jevlin"
	if runtime.GOOS == "windows" {
		name = "jevlin.exe"
	}
	own := filepath.Join(home, "bin", name)
	if info, err := stat(own); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return own
}

// adoptOwnBinary fills entry.ownBinary from the installation's home and,
// when this process is an npm-managed copy while the installation keeps a
// native binary, makes THAT binary the command the integrations run. Hooks
// written by the npm copy then survive `npm uninstall`, the skill keeps
// naming the installation's own copy, and a reinstall from either copy
// refreshes the one set of entries instead of standing a second set beside
// the first — with both binaries in the ownership reference (refFor), a
// machine that already carries both sets collapses them to one on the next
// install. A native process keeps its own path, and an installation with no
// binary of its own (a pure npm install) is exactly as before.
//
// The swap is made only for the home's OWN config (or discovery): home here
// is the machine's installation (JEVLIN_HOME, else ~/.jevlin), and an
// `agents install -config <elsewhere>` run from an npm copy must not wire
// another installation's hooks to this home's binary, whose later
// `uninstall -binary` would strand them. Such a run keeps its own path, as
// before; the ownership reference still carries both.
//
// And only when the home's binary is known to be no older than the copy
// running: setup never copies itself to <home>/bin, so the native copy moves
// only by `jevlin upgrade`, and an npm copy a release ahead would otherwise
// write its hooks and its skill for a binary that predates them. Either
// version unknown keeps the running path, the way it was before the swap;
// entry.ownOlder then names the copy that was passed over, for the plan to
// say why.
func adoptOwnBinary(entry binEntry, home string, getenv func(string) string, stat func(string) (os.FileInfo, error), versionOf func(string) (selfupdate.Version, bool)) binEntry {
	own := ownBinaryAt(home, stat)
	if own == "" {
		return entry
	}
	entry.ownBinary = own
	// Through Abs, as resolveEntry spells entry.cfg: on Windows a rootless
	// path gains the working drive there, and the comparison must see both
	// sides the same way.
	ownCfg, err := filepath.Abs(filepath.Join(home, setupConfigFile))
	if err != nil {
		ownCfg = filepath.Join(home, setupConfigFile)
	}
	homesOwnConfig := entry.cfg == "" || samePath(entry.cfg, ownCfg)
	if own == entry.command || !homesOwnConfig || classifyLaunch(entry.command, getenv("JEVLIN_LAUNCH")) == launchNative {
		return entry
	}
	if !ownIsNoOlder(versionOf, own, entry.command) {
		entry.ownOlder = own
		return entry
	}
	entry.command, entry.ownBinary = own, entry.command
	return entry
}

// ownIsNoOlder reports whether the binary at own is a release no older than
// the one at running. Both are read the same way, from the file, so a
// question about two copies never compares a stamped string with a read one.
func ownIsNoOlder(versionOf func(string) (selfupdate.Version, bool), own, running string) bool {
	ov, ok := versionOf(own)
	if !ok {
		return false
	}
	rv, ok := versionOf(running)
	return ok && ov.Compare(rv) >= 0
}

// jevlinModulePath is the main module a jevlin binary's build information
// names.
const jevlinModulePath = "github.com/jevlinai/jevlin-go"

// binaryReleaseVersion is the release a jevlin binary on disk was built as,
// read from the build information the Go linker records in it, never by
// running it. A release is built from its tag, so the main module's version
// is that tag (v0.4.3); -trimpath, which the release build uses, leaves that
// and drops the -ldflags that set main.version, so this is the one reading of
// a copy's version that works from outside it. A build from an untagged or
// modified tree carries a pseudo-version, which is no release, and a file
// that is not a jevlin binary answers false.
func binaryReleaseVersion(path string) (selfupdate.Version, bool) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return selfupdate.Version{}, false
	}
	return releaseOfBuild(info)
}

// releaseOfBuild is binaryReleaseVersion's reading of build information
// already in hand.
func releaseOfBuild(info *debug.BuildInfo) (selfupdate.Version, bool) {
	if info == nil || info.Main.Path != jevlinModulePath {
		return selfupdate.Version{}, false
	}
	v, err := selfupdate.ParseReleaseTag(info.Main.Version)
	return v, err == nil
}

// npmLayoutKind recognizes the files npm/install.js leaves around the binary:
// <package>/bin/jevlin beside <package>/bin/jevlin.js, with
// <package>/package.json and <package>/install.js.
func npmLayoutKind(exe string) launchKind {
	binDir := filepath.Dir(exe)
	if filepath.Base(binDir) != "bin" {
		return launchNative
	}
	root := filepath.Dir(binDir)
	manifest, manifestErr := os.ReadFile(filepath.Join(root, "package.json")) // #nosec G304 -- a fixed name beside this executable
	_, installErr := os.Lstat(filepath.Join(root, "install.js"))
	_, wrapperErr := os.Lstat(filepath.Join(binDir, "jevlin.js"))
	absent := 0
	for _, err := range []error{manifestErr, installErr, wrapperErr} {
		if errors.Is(err, fs.ErrNotExist) {
			absent++
		}
	}
	switch {
	case absent == 3:
		return launchNative
	case manifestErr != nil || installErr != nil || wrapperErr != nil:
		return launchAmbiguous
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(manifest, &pkg) != nil || pkg.Name != npmPackageName {
		return launchAmbiguous
	}
	return launchNPMLayout
}

// npmRemovalGuidance is how a participant removes or updates a copy the
// package manager owns.
func npmRemovalGuidance(kind launchKind, verb string) string {
	switch kind {
	case launchNPMGlobal:
		if verb == "upgrade" {
			return "npm owns this copy: npm install -g @jevlinsearch/jevlin@latest"
		}
		return "npm owns this copy: npm uninstall -g @jevlinsearch/jevlin"
	case launchNPMLocal:
		return "this copy belongs to a project's node_modules: " + verb + " it through that project's dependencies (package.json and its lockfile)"
	case launchAmbiguous:
		return "this copy sits beside some of npm's package files but not all of them, so who owns it is unclear; " + verb + " it the way it was installed"
	default:
		return "npm owns this copy; " + verb + " it with the package manager that installed it"
	}
}

// checkSetupLaunch decides whether this binary may be the one every hook and
// skill is written to call: a native copy or a global npm install. Anything
// npm or a project will discard is refused. An ambiguous layout is accepted,
// as it always was: setup's question is only whether the path will survive.
func checkSetupLaunch(exe, launch string) error {
	switch classifyLaunch(exe, launch) {
	case launchNative, launchNPMGlobal, launchAmbiguous:
		return nil
	case launchNPMCache:
		return fmt.Errorf("%w (this copy runs from npm's temporary cache, %s)", errNpmLaunch, exe)
	case launchNPMDirect, launchNPMLayout:
		return fmt.Errorf("%w (%s)", errNpmDirect, exe)
	case launchNPMLocal:
		return fmt.Errorf("%w (this copy is a project-local node_modules install, %s)", errNpmLaunch, exe)
	default:
		return fmt.Errorf("%w (npm could not say where this copy is installed: %s)", errNpmLaunch, exe)
	}
}

// checkUpgradeLaunch admits a native self-upgrade only for a native copy:
// every npm kind, and an ambiguous layout, is the package manager's to
// update, and the refusal says how.
func checkUpgradeLaunch(exe, launch string) error {
	if kind := classifyLaunch(exe, launch); kind != launchNative {
		return errors.New(npmRemovalGuidance(kind, "upgrade"))
	}
	return nil
}
