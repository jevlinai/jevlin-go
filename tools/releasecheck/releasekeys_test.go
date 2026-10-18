package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The repository's own signature.go is what preflight reads at a tag cut
// from here, so the parse is held to the real file, not to a fixture.
func TestCheckReleaseKeysReadsTheRepositorysOwnList(t *testing.T) {
	n, err := CheckReleaseKeys(mustRead(t, filepath.Join("..", "..", releaseKeysFile)))
	if err != nil || n < 1 {
		t.Fatalf("the repository's own %s: %d key(s), %v", releaseKeysFile, n, err)
	}
}

var releaseKeysList = regexp.MustCompile(`(?s)var releasePublicKeys = \[\]string\{.*?\n\}`)

// withReleaseKeysList is the real signature.go with its list's entries
// replaced by entries.
func withReleaseKeysList(t *testing.T, entries string) []byte {
	t.Helper()
	src := string(mustRead(t, filepath.Join("..", "..", releaseKeysFile)))
	if !releaseKeysList.MatchString(src) {
		t.Fatalf("fixture: %s has no multi-line releasePublicKeys list to replace", releaseKeysFile)
	}
	return []byte(releaseKeysList.ReplaceAllLiteralString(src, "var releasePublicKeys = []string{"+entries+"}"))
}

func TestCheckReleaseKeysRefusesAListNoUpdaterCouldUse(t *testing.T) {
	short := strconv.Quote(base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize-1)))
	for name, c := range map[string]struct {
		src  []byte
		want string
	}{
		"an empty list":                {withReleaseKeysList(t, ""), "compiles in no release signing key"},
		"an entry that is not literal": {withReleaseKeysList(t, "someKey"), "is not a string literal"},
		"an entry that is not base64":  {withReleaseKeysList(t, `"not base64!"`), "is not a base64 Ed25519 public key"},
		"an entry of the wrong length": {withReleaseKeysList(t, short), "is not a base64 Ed25519 public key"},
		"no list at all":               {[]byte("package selfupdate\n"), "has no releasePublicKeys"},
		"source that does not parse":   {[]byte("package selfupdate\nvar releasePublicKeys = []string{"), "does not parse"},
	} {
		if n, err := CheckReleaseKeys(c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %d key(s), err %v; want a refusal saying %q", name, n, err, c.want)
		}
	}

	key := strconv.Quote(base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)))
	if n, err := CheckReleaseKeys(withReleaseKeysList(t, key+", "+key)); err != nil || n != 2 {
		t.Errorf("a rotation window of two keys: %d key(s), %v", n, err)
	}
}

// A tag whose updater trusts no key is refused before anything is built,
// and the list judged is the tag's: main having one again does not make
// the tag right.
func TestPreflightRefusesATagThatCompilesInNoReleaseKey(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	write(t, r.dir, releaseKeysFile, string(withReleaseKeysList(t, "")))
	commit(t, r.dir, "the key list emptied")
	r.annotate("v0.2.8")
	copyFromRepo(t, r.dir, releaseKeysFile)
	commit(t, r.dir, "main has a key again")

	if _, err := r.preflight("v0.2.8"); err == nil || !strings.Contains(err.Error(), "compiles in no release signing key") {
		t.Fatalf("a tag that compiles in no release key: err %v", err)
	}
}
