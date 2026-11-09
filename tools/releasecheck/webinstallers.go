package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The installers jevlin.ai serves live here, beside the binary whose
// releases they install. Each carries its own copy of the release keys and
// checks checksums.txt.sig against it, where a tool on the machine can: the
// shell script as RELEASE_PUBLIC_KEYS, one line of space-separated keys,
// and the PowerShell script as $releaseKeys, one @(...) list.
const (
	shellInstallerFile      = "install/install.sh"
	powershellInstallerFile = "install/install.ps1"
)

var (
	// shellKeysLine is every assignment to RELEASE_PUBLIC_KEYS at the start
	// of a line, and shellKeysValue the one shape its value may take: one
	// double-quoted word list of base64 keys, nothing expanded.
	shellKeysLine  = regexp.MustCompile(`(?m)^RELEASE_PUBLIC_KEYS=(.*)$`)
	shellKeysValue = regexp.MustCompile(`^"([A-Za-z0-9+/=]+(?: [A-Za-z0-9+/=]+)*)"$`)

	// psKeysLine is every assignment to $releaseKeys, and psKeysValue the one
	// shape its value may take: an @(...) of double-quoted keys.
	psKeysLine  = regexp.MustCompile(`(?m)^[ \t]*\$releaseKeys[ \t]*=[ \t]*(.*?)[ \t]*$`)
	psKeysValue = regexp.MustCompile(`^@\(("[A-Za-z0-9+/=]+"(?:, "[A-Za-z0-9+/=]+")*)\)$`)
	psKeyEntry  = regexp.MustCompile(`"([A-Za-z0-9+/=]+)"`)
)

// ShellInstallerReleaseKeys reads the keys install.sh trusts. Nothing here
// runs a shell, so the assignment must be the one line in the one shape the
// file writes it in; any other is refused rather than read some way the
// script might not.
func ShellInstallerReleaseKeys(src []byte) ([]string, error) {
	m := shellKeysLine.FindAllSubmatch(normalizeNewlines(src), -1)
	if len(m) != 1 {
		return nil, fmt.Errorf("%s at the tag has no single RELEASE_PUBLIC_KEYS=\"...\" line this check can read", shellInstallerFile)
	}
	v := shellKeysValue.FindSubmatch(m[0][1])
	if v == nil {
		return nil, fmt.Errorf("%s at the tag: RELEASE_PUBLIC_KEYS is not one quoted list of keys: %q", shellInstallerFile, m[0][1])
	}
	return strings.Fields(string(v[1])), nil
}

// PowerShellInstallerReleaseKeys reads the keys install.ps1 trusts, held to
// the one shape the file writes them in, as ShellInstallerReleaseKeys is.
func PowerShellInstallerReleaseKeys(src []byte) ([]string, error) {
	m := psKeysLine.FindAllSubmatch(normalizeNewlines(src), -1)
	if len(m) != 1 {
		return nil, fmt.Errorf("%s at the tag has no single $releaseKeys = @(...) line this check can read", powershellInstallerFile)
	}
	v := psKeysValue.FindSubmatch(m[0][1])
	if v == nil {
		return nil, fmt.Errorf("%s at the tag: $releaseKeys is not one @(...) of quoted keys: %q", powershellInstallerFile, m[0][1])
	}
	var keys []string
	for _, e := range psKeyEntry.FindAllSubmatch(v[1], -1) {
		keys = append(keys, string(e[1]))
	}
	return keys, nil
}

// normalizeNewlines reads a CRLF file as LF: .gitattributes checks *.ps1 out
// with CRLF, and a line's carriage return is not part of its value.
func normalizeNewlines(src []byte) []byte {
	return []byte(strings.ReplaceAll(string(src), "\r\n", "\n"))
}

// CheckWebInstallersTrustTheReleaseKeys holds both scripts to the updater's
// keys at the same tag, as CheckInstallerTrustsTheReleaseKeys holds the npm
// wrapper. A script that trusts a retired key refuses every release signed
// after a rotation, on every machine that has a verifier, and one that trusts
// a key the updater does not accepts a release the updater would refuse. The
// website serves the scripts from main, so the tag is where a disagreement
// can still be stopped. Order does not matter, the set does.
func CheckWebInstallersTrustTheReleaseKeys(installSh, installPs1, keysSource []byte) (int, error) {
	updater, err := releaseKeysFromSource(keysSource)
	if err != nil {
		return 0, err
	}
	shell, err := ShellInstallerReleaseKeys(installSh)
	if err != nil {
		return 0, err
	}
	ps, err := PowerShellInstallerReleaseKeys(installPs1)
	if err != nil {
		return 0, err
	}
	for _, c := range []struct {
		file string
		keys []string
	}{{shellInstallerFile, shell}, {powershellInstallerFile, ps}} {
		if !sameKeySet(c.keys, updater) {
			return 0, fmt.Errorf("%s at the tag trusts %v and %s compiles in %v: "+
				"the installer would refuse a release the updater accepts, or accept one it refuses. "+
				"The lists change together, in one commit (docs/RELEASING.md, \"The release signing key\")",
				c.file, c.keys, releaseKeysFile, updater)
		}
	}
	return len(updater), nil
}

func sameKeySet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
