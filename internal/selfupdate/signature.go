package selfupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// releasePublicKeys are the Ed25519 public keys, standard base64, whose
// signature over checksums.txt makes a release authentic. The private half
// is held only by the release workflow's release-signing environment, so
// write access to the repository's release assets is not enough to publish
// something an installed updater accepts. More than one key is a rotation
// window: a signature by any of them is accepted. Run
// `go run ./tools/releasecheck keygen` to provision one (docs/RELEASING.md).
//
// While the list is empty this build verifies no release, and the release
// workflow refuses to sign, so nothing is published that it would accept.
var releasePublicKeys = []string{}

// signatureDomain prefixes every signed message, so a signature made for
// this purpose is never valid for any other use of the same key.
const signatureDomain = "jevlin release checksums.txt v1\n"

// ReleasePublicKeys are the compiled-in release signing keys.
func ReleasePublicKeys() ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(releasePublicKeys))
	for i, encoded := range releasePublicKeys {
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("compiled-in release key %d is not a base64 Ed25519 public key", i)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

// SignedMessage is exactly what a release signature covers.
func SignedMessage(checksums []byte) []byte {
	return append([]byte(signatureDomain), checksums...)
}

// SignChecksums is the signature asset for checksums under key: one line of
// standard base64.
func SignChecksums(key ed25519.PrivateKey, checksums []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, SignedMessage(checksums))) + "\n")
}

// VerifyChecksumsSignature accepts sig only when it is one well-formed
// signature line by one of keys over checksums.
func VerifyChecksumsSignature(keys []ed25519.PublicKey, checksums, sig []byte) error {
	if len(keys) == 0 {
		return errNoReleaseKeys
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSuffix(string(sig), "\n"))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%s is not one base64 Ed25519 signature", SignatureAssetName)
	}
	msg := SignedMessage(checksums)
	for _, key := range keys {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, msg, raw) {
			return nil
		}
	}
	return fmt.Errorf("%s is not signed by a release key this build trusts", ChecksumAssetName)
}

var errNoReleaseKeys = errors.New("this build carries no release signing key, so it cannot verify any release; reinstall from the releases page")

// SignedVerifier is the updater's verifier: checksums.txt must carry a
// signature by a compiled-in release key, and the archive must then match
// its line in that file. The signature proves who published the checksums;
// SHA256Verifier then binds the archive to them.
type SignedVerifier struct {
	keys []ed25519.PublicKey // nil: ReleasePublicKeys
}

// NewSignedVerifier trusts exactly keys instead of the compiled-in ones.
func NewSignedVerifier(keys ...ed25519.PublicKey) SignedVerifier {
	return SignedVerifier{keys: append([]ed25519.PublicKey{}, keys...)}
}

func (v SignedVerifier) trusted() ([]ed25519.PublicKey, error) {
	if v.keys != nil {
		return v.keys, nil
	}
	return ReleasePublicKeys()
}

func (v SignedVerifier) RequiredAssets(release ReleaseInfo, target Artifact) ([]AssetRequirement, error) {
	keys, err := v.trusted()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errNoReleaseKeys
	}
	reqs, err := SHA256Verifier{}.RequiredAssets(release, target)
	if err != nil {
		return nil, err
	}
	return append(reqs, AssetRequirement{Name: SignatureAssetName, MaxBytes: MaxSignatureBytes}), nil
}

func (v SignedVerifier) Verify(ctx context.Context, assets map[string][]byte, target Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keys, err := v.trusted()
	if err != nil {
		return err
	}
	sums, ok := assets[ChecksumAssetName]
	if !ok {
		return fmt.Errorf("verifier did not receive %q", ChecksumAssetName)
	}
	sig, ok := assets[SignatureAssetName]
	if !ok {
		return fmt.Errorf("verifier did not receive %q", SignatureAssetName)
	}
	if err := VerifyChecksumsSignature(keys, sums, sig); err != nil {
		return err
	}
	return SHA256Verifier{}.Verify(ctx, assets, target)
}
