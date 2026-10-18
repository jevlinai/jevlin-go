package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

var testSeed = bytes.Repeat([]byte{3}, ed25519.SeedSize)

func testKey() (string, ed25519.PublicKey) {
	key := ed25519.NewKeyFromSeed(testSeed)
	return base64.StdEncoding.EncodeToString(testSeed) + "\n", key.Public().(ed25519.PublicKey)
}

func TestSignChecksumsSignsWhatTheUpdaterAccepts(t *testing.T) {
	encoded, public := testKey()
	sums := []byte("0123  jevlin_0.3.0_linux_amd64.tar.gz\n")
	sig, err := SignChecksums(encoded, []ed25519.PublicKey{public}, sums)
	if err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.VerifyChecksumsSignature([]ed25519.PublicKey{public}, sums, sig); err != nil {
		t.Fatalf("the updater refuses what sign wrote: %v", err)
	}
	if err := selfupdate.VerifyChecksumsSignature([]ed25519.PublicKey{public}, append(sums, 'x'), sig); err == nil {
		t.Fatal("a signature verified over checksums it did not sign")
	}
}

// A key the tag's updater does not trust would publish a release no
// installed updater could take.
func TestSignChecksumsRefusesAKeyTheUpdaterDoesNotTrust(t *testing.T) {
	encoded, _ := testKey()
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	for name, trusted := range map[string][]ed25519.PublicKey{
		"no compiled-in key": nil,
		"another key":        {other},
	} {
		if _, err := SignChecksums(encoded, trusted, []byte("sums\n")); err == nil ||
			!strings.Contains(err.Error(), "releasePublicKeys") {
			t.Errorf("%s: err = %v, want a refusal naming releasePublicKeys", name, err)
		}
	}
}

func TestSignChecksumsRefusesAMalformedSeedWithoutEchoingIt(t *testing.T) {
	_, public := testKey()
	for _, encoded := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(testSeed[:31])} {
		_, err := SignChecksums(encoded, []ed25519.PublicKey{public}, []byte("sums\n"))
		if err == nil {
			t.Fatalf("seed %q was accepted", encoded)
		}
		if encoded != "" && strings.Contains(err.Error(), encoded) {
			t.Errorf("the error echoes the seed: %v", err)
		}
	}
}

func TestSignRefusesWithoutTheSecretAndUnderTheCompiledInKeys(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(in, []byte("sums\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "checksums.txt.sig")
	t.Setenv(signingKeyEnv, "")
	if err := runSign([]string{"-in", in, "-out", out}, io.Discard); err == nil {
		t.Fatal("sign ran without the secret")
	}
	// The test seed is no release key, whatever the compiled-in list holds.
	encoded, _ := testKey()
	t.Setenv(signingKeyEnv, encoded)
	if err := runSign([]string{"-in", in, "-out", out}, io.Discard); err == nil {
		t.Fatal("sign signed with a key the updater does not compile in")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("a refused sign left %s behind: %v", out, err)
	}
}

func TestKeygenWritesOnlyTheFileAndPrintsOnlyThePublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-signing.key")
	var out bytes.Buffer
	if err := runKeygen([]string{"-out", path}, &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("the seed file is mode %o, readable beyond its owner", mode)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	seed := strings.TrimSpace(string(raw))
	if strings.Contains(out.String(), seed) {
		t.Fatal("keygen printed the private seed")
	}
	decoded, err := base64.StdEncoding.DecodeString(seed)
	if err != nil || len(decoded) != ed25519.SeedSize {
		t.Fatalf("the seed file does not hold a base64 seed: %v", err)
	}
	public := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(decoded).Public().(ed25519.PublicKey))
	if !strings.Contains(out.String(), public) {
		t.Fatalf("keygen did not print the seed's public key:\n%s", out.String())
	}
	if _, err := SignChecksums(string(raw), nil, nil); err == nil {
		t.Fatal("a fresh key signed before its public half was compiled in")
	}
	if err := runKeygen([]string{"-out", path}, io.Discard); err == nil {
		t.Fatal("keygen overwrote an existing key file")
	}
}
