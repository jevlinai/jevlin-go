package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// releaseKeysFile is where the updater's trusted release keys are written.
const releaseKeysFile = "internal/selfupdate/signature.go"

// CheckReleaseKeys reads releasePublicKeys out of signature.go's source as
// the tag carries it, and requires at least one entry, each a standard
// base64 Ed25519 public key, by the rule selfupdate.ReleasePublicKeys
// applies.
//
// A tag whose list is empty builds an updater that refuses every release,
// and releasecheck sign, at goreleaser's sign step, refuses to sign for it.
// That failure comes after the tag is pushed, and tags are immutable, so
// the version is spent. Here it stops before anything is pushed.
//
// The source is parsed rather than the list taken from this tool's own
// build, because preflight runs from whatever checkout the maintainer or
// the workflow has, and every file it judges is the tag's (docs/RELEASING.md).
func CheckReleaseKeys(src []byte) (int, error) {
	keys, err := releaseKeysFromSource(src)
	return len(keys), err
}

// releaseKeysFromSource is CheckReleaseKeys returning the keys themselves,
// as the source spells them.
func releaseKeysFromSource(src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), releaseKeysFile, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("%s at the tag does not parse: %w", releaseKeysFile, err)
	}
	var list *ast.CompositeLit
	found := 0
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != "releasePublicKeys" {
					continue
				}
				found++
				if i < len(vs.Values) {
					list, _ = vs.Values[i].(*ast.CompositeLit)
				}
			}
		}
	}
	if found != 1 || list == nil {
		return nil, fmt.Errorf("%s at the tag has no releasePublicKeys = []string{...} this check can read", releaseKeysFile)
	}
	keys := make([]string, 0, len(list.Elts))
	for i, elt := range list.Elts {
		lit, ok := elt.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return nil, fmt.Errorf("%s at the tag: releasePublicKeys entry %d is not a string literal", releaseKeysFile, i)
		}
		encoded, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, fmt.Errorf("%s at the tag: releasePublicKeys entry %d does not unquote: %w", releaseKeysFile, i, err)
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s at the tag: releasePublicKeys entry %d is not a base64 Ed25519 public key", releaseKeysFile, i)
		}
		keys = append(keys, encoded)
	}
	if len(list.Elts) == 0 {
		return nil, fmt.Errorf("%s at the tag compiles in no release signing key: an updater built from it refuses every "+
			"release, and goreleaser's sign step would refuse after the tag is pushed (docs/RELEASING.md, "+
			"\"The release signing key\")", releaseKeysFile)
	}
	return keys, nil
}

// installerKeysList is npm/install.js's RELEASE_PUBLIC_KEYS array, its
// entries one per line as the file writes them.
var installerKeysList = regexp.MustCompile(`(?s)\nconst RELEASE_PUBLIC_KEYS = \[\n(.*?)\n\]\n`)

// installerKeyLine is one entry of it: a string literal and an optional
// trailing comment, nothing else.
var installerKeyLine = regexp.MustCompile(`^\s*"([A-Za-z0-9+/=]*)",\s*(//.*)?$`)

// InstallerReleaseKeys reads the keys npm/install.js trusts. The script is
// JavaScript and nothing here parses JavaScript, so the list must be in the
// one shape the file writes it in, and any other is refused rather than read
// some way the script might not.
func InstallerReleaseKeys(installJS []byte) ([]string, error) {
	m := installerKeysList.FindAllSubmatch(installJS, -1)
	if len(m) != 1 {
		return nil, fmt.Errorf("npm/install.js at the tag has no single `const RELEASE_PUBLIC_KEYS = [` list, one key per line, this check can read")
	}
	var keys []string
	for i, line := range strings.Split(string(m[0][1]), "\n") {
		e := installerKeyLine.FindStringSubmatch(line)
		if e == nil {
			return nil, fmt.Errorf("npm/install.js at the tag: RELEASE_PUBLIC_KEYS line %d is not one quoted key and a comma: %q", i+1, line)
		}
		keys = append(keys, e[1])
	}
	return keys, nil
}

// CheckInstallerTrustsTheReleaseKeys holds the npm wrapper to the updater's
// keys at the same tag. The release is signed by a key releasecheck sign
// finds in signature.go's list; an installer with any other list refuses
// that release at every participant's npm install, and a published npm
// version cannot be withdrawn. Order does not matter, the set does.
func CheckInstallerTrustsTheReleaseKeys(installJS, keysSource []byte) (int, error) {
	updater, err := releaseKeysFromSource(keysSource)
	if err != nil {
		return 0, err
	}
	installer, err := InstallerReleaseKeys(installJS)
	if err != nil {
		return 0, err
	}
	a, b := slices.Clone(updater), slices.Clone(installer)
	slices.Sort(a)
	slices.Sort(b)
	if !slices.Equal(a, b) {
		return 0, fmt.Errorf("npm/install.js at the tag trusts %v and %s compiles in %v: "+
			"npm installs would refuse a release the updater accepts, or accept one it refuses. "+
			"The two lists change together, in one commit (docs/RELEASING.md, \"The release signing key\")",
			installer, releaseKeysFile, updater)
	}
	return len(installer), nil
}
