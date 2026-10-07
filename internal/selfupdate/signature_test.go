package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// testReleaseKey stands in for the release key in every test in this
// package: init compiles its public half in, so the default verifier is the
// one under test.
var testReleaseKey = ed25519.NewKeyFromSeed(seed("jevlin selfupdate test release key"))

func seed(label string) []byte {
	s := sha256.Sum256([]byte(label))
	return s[:]
}

func init() {
	releasePublicKeys = []string{base64.StdEncoding.EncodeToString(testReleaseKey.Public().(ed25519.PublicKey))}
}

func withReleaseKeys(t *testing.T, keys ...string) {
	t.Helper()
	saved := releasePublicKeys
	releasePublicKeys = keys
	t.Cleanup(func() { releasePublicKeys = saved })
}

func signedAssets(target Artifact, archive []byte, key ed25519.PrivateKey) map[string][]byte {
	sums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), target.ArchiveName))
	return map[string][]byte{
		target.ArchiveName: archive,
		ChecksumAssetName:  sums,
		SignatureAssetName: SignChecksums(key, sums),
	}
}

func TestSignedVerifierAcceptsAReleaseSignedByACompiledInKey(t *testing.T) {
	target := Artifact{ArchiveName: "jevlin_0.3.0_linux_amd64.tar.gz"}
	if err := (SignedVerifier{}).Verify(context.Background(), signedAssets(target, []byte("archive"), testReleaseKey), target); err != nil {
		t.Fatal(err)
	}
	rotated := ed25519.NewKeyFromSeed(seed("rotated"))
	withReleaseKeys(t, releasePublicKeys[0], base64.StdEncoding.EncodeToString(rotated.Public().(ed25519.PublicKey)))
	if err := (SignedVerifier{}).Verify(context.Background(), signedAssets(target, []byte("archive"), rotated), target); err != nil {
		t.Errorf("a signature by the second compiled-in key: %v", err)
	}
}

// The attack this verifier exists for: whoever can replace release assets
// uploads a self-consistent archive and checksums.txt. Without the release
// key the pair must not verify, however consistent it is.
func TestSignedVerifierRejectsWhatReleaseAssetWriteAccessAloneCanProduce(t *testing.T) {
	target := Artifact{ArchiveName: "jevlin_0.3.0_linux_amd64.tar.gz"}
	attacker := ed25519.NewKeyFromSeed(seed("attacker"))
	good := signedAssets(target, []byte("archive"), testReleaseKey)
	cases := map[string]func(a map[string][]byte){
		"no signature asset": func(a map[string][]byte) { delete(a, SignatureAssetName) },
		"signed by another key": func(a map[string][]byte) {
			a[SignatureAssetName] = SignChecksums(attacker, a[ChecksumAssetName])
		},
		"checksums.txt rewritten for a new archive under the old signature": func(a map[string][]byte) {
			evil := []byte("evil")
			a[target.ArchiveName] = evil
			a[ChecksumAssetName] = []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(evil), target.ArchiveName))
		},
		"signature over the bare checksums, without the domain": func(a map[string][]byte) {
			a[SignatureAssetName] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(testReleaseKey, a[ChecksumAssetName])) + "\n")
		},
		"archive tampered, checksums still signed": func(a map[string][]byte) { a[target.ArchiveName] = []byte("evil") },
		"empty signature":                          func(a map[string][]byte) { a[SignatureAssetName] = nil },
		"not base64":                               func(a map[string][]byte) { a[SignatureAssetName] = []byte("!!!!\n") },
		"truncated signature": func(a map[string][]byte) {
			a[SignatureAssetName] = []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize-1)) + "\n")
		},
		"two signature lines": func(a map[string][]byte) {
			a[SignatureAssetName] = append(append([]byte(nil), good[SignatureAssetName]...), good[SignatureAssetName]...)
		},
	}
	for name, mutate := range cases {
		assets := map[string][]byte{}
		for k, v := range good {
			assets[k] = append([]byte(nil), v...)
		}
		mutate(assets)
		if err := (SignedVerifier{}).Verify(context.Background(), assets, target); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSignedVerifierDeclaresTheSignatureAndFailsClosedWithoutAKey(t *testing.T) {
	target := Artifact{ArchiveName: "jevlin_0.3.0_linux_amd64.tar.gz"}
	got, err := SignedVerifier{}.RequiredAssets(ReleaseInfo{}, target)
	if err != nil {
		t.Fatal(err)
	}
	want := []AssetRequirement{{target.ArchiveName, MaxArchiveBytes}, {ChecksumAssetName, MaxChecksumBytes}, {SignatureAssetName, MaxSignatureBytes}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("RequiredAssets = %v, want %v", got, want)
	}

	withReleaseKeys(t)
	if _, err := (SignedVerifier{}).RequiredAssets(ReleaseInfo{}, target); err == nil || !strings.Contains(err.Error(), "no release signing key") {
		t.Errorf("a build with no compiled-in key must refuse before downloading, got %v", err)
	}
	if err := (SignedVerifier{}).Verify(context.Background(), signedAssets(target, []byte("a"), testReleaseKey), target); err == nil {
		t.Error("a build with no compiled-in key verified a release")
	}
	if _, err := NewSignedVerifier().RequiredAssets(ReleaseInfo{}, target); err == nil {
		t.Error("an explicitly empty key set must refuse too")
	}
	withReleaseKeys(t, "not-a-key")
	if _, err := (SignedVerifier{}).RequiredAssets(ReleaseInfo{}, target); err == nil {
		t.Error("a malformed compiled-in key must refuse")
	}
}

func TestNewSignedVerifierTrustsExactlyItsKeys(t *testing.T) {
	target := Artifact{ArchiveName: "a.tar.gz"}
	other := ed25519.NewKeyFromSeed(seed("other"))
	v := NewSignedVerifier(other.Public().(ed25519.PublicKey))
	if err := v.Verify(context.Background(), signedAssets(target, []byte("a"), other), target); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(context.Background(), signedAssets(target, []byte("a"), testReleaseKey), target); err == nil {
		t.Error("an injected key set must not also trust the compiled-in key")
	}
}
