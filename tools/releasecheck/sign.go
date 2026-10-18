package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

// signingKeyEnv names the release-signing environment's secret: the
// standard-base64 32-byte Ed25519 seed. Only release.yml's release-binaries
// job, running on a v* tag, can read it.
const signingKeyEnv = "JEVLIN_RELEASE_SIGNING_KEY"

// SignChecksums signs checksums with the seed, refusing a seed whose public
// half the tag's updater would not trust: a release signed by any other key
// is one no installed updater can verify, so it must not be published.
func SignChecksums(encodedSeed string, trusted []ed25519.PublicKey, checksums []byte) ([]byte, error) {
	seed, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(encodedSeed))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("$%s is not a base64 Ed25519 seed", signingKeyEnv)
	}
	key := ed25519.NewKeyFromSeed(seed)
	public := key.Public().(ed25519.PublicKey)
	known := false
	for _, k := range trusted {
		known = known || bytes.Equal(k, public)
	}
	if !known {
		return nil, fmt.Errorf("the signing key's public half %s is not compiled into internal/selfupdate's releasePublicKeys; "+
			"no updater built from this tag could verify the release, so it is not signed (docs/RELEASING.md)",
			base64.StdEncoding.EncodeToString(public))
	}
	sig := selfupdate.SignChecksums(key, checksums)
	if err := selfupdate.VerifyChecksumsSignature(trusted, checksums, sig); err != nil {
		return nil, err
	}
	return sig, nil
}

func runSign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	in := fs.String("in", "", "the checksums file goreleaser wrote")
	dst := fs.String("out", "", "where the signature goes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *dst == "" {
		return errors.New("-in and -out are required")
	}
	encoded := os.Getenv(signingKeyEnv)
	if encoded == "" {
		return fmt.Errorf("$%s is not set: the release-binaries job reads it from the release-signing environment", signingKeyEnv)
	}
	trusted, err := selfupdate.ReleasePublicKeys()
	if err != nil {
		return err
	}
	checksums, err := readBoundedFile(*in, selfupdate.MaxChecksumBytes)
	if err != nil {
		return err
	}
	sig, err := SignChecksums(encoded, trusted, checksums)
	if err != nil {
		return err
	}
	if err := writeNew(*dst, sig, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  signed %s\n", *in)
	return nil
}

func runVerifySignature(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify-signature", flag.ExitOnError)
	sums := fs.String("sums", "", "the published checksums.txt")
	sig := fs.String("sig", "", "the published checksums.txt.sig")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sums == "" || *sig == "" {
		return errors.New("-sums and -sig are required")
	}
	trusted, err := selfupdate.ReleasePublicKeys()
	if err != nil {
		return err
	}
	checksums, err := readBoundedFile(*sums, selfupdate.MaxChecksumBytes)
	if err != nil {
		return err
	}
	signature, err := readBoundedFile(*sig, selfupdate.MaxSignatureBytes)
	if err != nil {
		return err
	}
	if err := selfupdate.VerifyChecksumsSignature(trusted, checksums, signature); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  %s is signed by a compiled-in release key\n", *sums)
	return nil
}

// runKeygen makes a release signing key. The seed goes only to a new file,
// never to the terminal or a log.
func runKeygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dst := fs.String("out", "", "new file for the base64 seed (the secret)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dst == "" {
		return errors.New("-out is required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := writeNew(*dst, []byte(base64.StdEncoding.EncodeToString(private.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "public key: %s\n\n"+
		"Add it to releasePublicKeys in internal/selfupdate/signature.go, store %s as the %s secret\n"+
		"of the release-signing environment, then delete that file. See docs/RELEASING.md.\n",
		base64.StdEncoding.EncodeToString(public), *dst, signingKeyEnv)
	return nil
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- a path the workflow or operator named
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is over its %d-byte bound", path, max)
	}
	return b, nil
}

// writeNew creates path, refusing one that exists, so nothing is overwritten
// and no link is followed.
func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- a path the workflow or operator named
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
