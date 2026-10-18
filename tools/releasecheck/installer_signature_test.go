package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

// npm/install.js verifies checksums.txt.sig in JavaScript, and the release
// is signed in Go. Two implementations of one rule drift unless something
// runs them against each other, so these tests run the script under Node:
// its verifier on signatures selfupdate's signer made and on the published
// v0.4.1 pair, and the script itself as npm's postinstall runs it, with
// https.get answering from a directory instead of GitHub.

var testInstallerSeed = bytes.Repeat([]byte{7}, ed25519.SeedSize)

func testInstallerKey() (ed25519.PrivateKey, string) {
	key := ed25519.NewKeyFromSeed(testInstallerSeed)
	return key, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func nodeBinary(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to run npm/install.js")
	}
	return node
}

func installJSPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "npm", "install.js"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// verifyScript calls install.js's verifyChecksumsSignature on the files
// named by its arguments, with the keys given as JSON (null: the script's
// own RELEASE_PUBLIC_KEYS), and prints which way it decided.
const verifyScript = `
const { verifyChecksumsSignature } = require(process.argv[1])
const fs = require("node:fs")
const keys = JSON.parse(process.argv[4])
const sums = fs.readFileSync(process.argv[2]), sig = fs.readFileSync(process.argv[3])
try {
  if (keys === null) verifyChecksumsSignature(sums, sig)
  else verifyChecksumsSignature(sums, sig, keys)
  process.stdout.write("accepted")
} catch (err) {
  process.stdout.write("refused: " + err.message)
}
`

// nodeVerify is "" when install.js accepts sig over sums under keys, and
// its refusal otherwise.
func nodeVerify(t *testing.T, sums, sig []byte, keys []string) string {
	t.Helper()
	dir := t.TempDir()
	sumsPath, sigPath := filepath.Join(dir, "checksums.txt"), filepath.Join(dir, "checksums.txt.sig")
	if err := os.WriteFile(sumsPath, sums, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	keysJSON := []byte("null")
	if keys != nil {
		var err error
		if keysJSON, err = json.Marshal(keys); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBinary(t), "-e", verifyScript, installJSPath(t), sumsPath, sigPath, string(keysJSON)) // #nosec G204 -- node from PATH running this test's script
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	switch s := string(out); {
	case s == "accepted":
		return ""
	case strings.HasPrefix(s, "refused: "):
		return strings.TrimPrefix(s, "refused: ")
	default:
		t.Fatalf("node printed neither verdict: %q", s)
		return ""
	}
}

func goVerify(t *testing.T, sums, sig []byte, keys []string) error {
	t.Helper()
	var trusted []ed25519.PublicKey
	for _, k := range keys {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			t.Fatal(err)
		}
		trusted = append(trusted, raw)
	}
	return selfupdate.VerifyChecksumsSignature(trusted, sums, sig)
}

var testSums = []byte("0000000000000000000000000000000000000000000000000000000000000000  jevlin_0.4.1_linux_amd64.tar.gz\n")

func TestTheInstallerAcceptsWhatTheReleaseSignerSigns(t *testing.T) {
	key, pub := testInstallerKey()
	sig := selfupdate.SignChecksums(key, testSums)
	for name, s := range map[string][]byte{
		"the signer's line": sig,
		"with no newline":   bytes.TrimSuffix(sig, []byte("\n")),
	} {
		if msg := nodeVerify(t, testSums, s, []string{pub}); msg != "" {
			t.Errorf("%s: install.js refused a signature the release signer made: %s", name, msg)
		}
		if err := goVerify(t, testSums, s, []string{pub}); err != nil {
			t.Errorf("%s: the updater refused it too, so this case shows nothing: %v", name, err)
		}
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	otherPub := base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	if msg := nodeVerify(t, testSums, sig, []string{otherPub, pub}); msg != "" {
		t.Errorf("a rotation window of two keys: install.js refused the second one's signature: %s", msg)
	}
}

// Every way the updater refuses a signature, install.js refuses too. Each
// case is checked against selfupdate.VerifyChecksumsSignature first, so a
// case the Go side would accept cannot pass here by testing nothing.
func TestTheInstallerRefusesWhatTheUpdaterRefuses(t *testing.T) {
	key, pub := testInstallerKey()
	sig := selfupdate.SignChecksums(key, testSums)
	text := strings.TrimSuffix(string(sig), "\n")

	tamperedSums := bytes.Clone(testSums)
	tamperedSums[0] = '1'
	raw, _ := base64.StdEncoding.DecodeString(text)
	flipped := bytes.Clone(raw)
	flipped[0] ^= 1
	// The last character before "==" carries two bits of the signature and
	// four of padding; setting a padding bit is the same bytes, spelled in
	// a way strict base64 refuses.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	nonCanonical := []byte(text)
	nonCanonical[85] = alphabet[strings.IndexByte(alphabet, nonCanonical[85])|1]
	if string(nonCanonical) == text {
		nonCanonical[85] = alphabet[strings.IndexByte(alphabet, nonCanonical[85])|2]
	}
	otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))

	for name, c := range map[string]struct {
		sums, sig []byte
		keys      []string
	}{
		"checksums changed after signing":   {tamperedSums, sig, []string{pub}},
		"a signature byte changed":          {testSums, []byte(base64.StdEncoding.EncodeToString(flipped) + "\n"), []string{pub}},
		"signed by a key it does not trust": {testSums, selfupdate.SignChecksums(otherKey, testSums), []string{pub}},
		"signed without the domain prefix":  {testSums, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, testSums)) + "\n"), []string{pub}},
		"non-canonical base64":              {testSums, append(nonCanonical, '\n'), []string{pub}},
		"two lines":                         {testSums, append(bytes.Clone(sig), sig...), []string{pub}},
		"trailing spaces":                   {testSums, []byte(text + "  \n"), []string{pub}},
		"empty":                             {testSums, nil, []string{pub}},
		"not base64":                        {testSums, []byte("not a signature\n"), []string{pub}},
		"no trusted key at all":             {testSums, sig, []string{}},
	} {
		if err := goVerify(t, c.sums, c.sig, c.keys); err == nil {
			t.Errorf("%s: the updater accepts this, so it is not a refusal case", name)
			continue
		}
		if msg := nodeVerify(t, c.sums, c.sig, c.keys); msg == "" {
			t.Errorf("%s: install.js accepted what the updater refuses", name)
		}
	}
}

// Under its own keys, the script accepts the release v0.4.1 actually
// published, signed by the release workflow, and refuses the test key. When
// the v0.4.1 key is rotated out of the list, replace the fixture with a
// release signed under a key the list still carries.
func TestTheInstallerAcceptsThePublishedReleaseUnderItsOwnKeys(t *testing.T) {
	sums := mustRead(t, filepath.Join("testdata", "release-v0.4.1", "checksums.txt"))
	sig := mustRead(t, filepath.Join("testdata", "release-v0.4.1", "checksums.txt.sig"))
	trusted, err := selfupdate.ReleasePublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.VerifyChecksumsSignature(trusted, sums, sig); err != nil {
		t.Fatalf("fixture: the updater refuses the published v0.4.1 pair: %v", err)
	}
	if msg := nodeVerify(t, sums, sig, nil); msg != "" {
		t.Errorf("install.js refused the published v0.4.1 release under its own keys: %s", msg)
	}
	key, _ := testInstallerKey()
	if msg := nodeVerify(t, testSums, selfupdate.SignChecksums(key, testSums), nil); msg == "" {
		t.Error("install.js accepted a signature by a key it does not carry")
	}
}

// httpsStub replaces https.get for the script it is preloaded into: every
// URL is answered from $JEVLIN_TEST_ASSETS by its last path element, 404
// when there is no such file, and every URL asked for is appended to
// $JEVLIN_TEST_ASSETS/requests.
const httpsStub = `
const https = require("node:https")
const fs = require("node:fs")
const path = require("node:path")
const { PassThrough } = require("node:stream")
const { EventEmitter } = require("node:events")
const dir = process.env.JEVLIN_TEST_ASSETS
https.get = (url, options, callback) => {
  fs.appendFileSync(path.join(dir, "requests"), url + "\n")
  const res = new PassThrough()
  res.headers = {}
  const file = path.join(dir, path.basename(new URL(url).pathname))
  if (fs.existsSync(file)) {
    res.statusCode = 200
    res.end(fs.readFileSync(file))
  } else {
    res.statusCode = 404
    res.end()
  }
  process.nextTick(() => callback(res))
  return new EventEmitter()
}
`

// postinstallFixture is a copy of npm/install.js that trusts only the test
// key, beside a package.json at version, and a directory of release assets
// for this platform: the archive holds a "binary" whose bytes are content.
type postinstallFixture struct {
	pkg, assets, binary, archive string
	sums                         []byte
}

func newPostinstallFixture(t *testing.T, version, content string) postinstallFixture {
	t.Helper()
	_, pub := testInstallerKey()
	src := string(mustRead(t, installJSPath(t)))
	withTestKey := installerKeysList.ReplaceAllLiteralString(src, "\nconst RELEASE_PUBLIC_KEYS = [\n  \""+pub+"\",\n]\n")
	if withTestKey == src || !strings.Contains(withTestKey, pub) {
		t.Fatal("fixture: npm/install.js has no RELEASE_PUBLIC_KEYS list to replace with the test key")
	}
	f := postinstallFixture{pkg: t.TempDir(), assets: t.TempDir()}
	write(t, f.pkg, "install.js", withTestKey)
	write(t, f.pkg, "package.json", `{"version":"`+version+`"}`+"\n")
	if err := os.MkdirAll(filepath.Join(f.pkg, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, f.assets, "stub.js", httpsStub)

	osName := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	if osName == "" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skipf("install.js supports no %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	bin, ext := "jevlin", "tar.gz"
	if osName == "windows" {
		bin, ext = "jevlin.exe", "zip"
	}
	f.binary = filepath.Join(f.pkg, "bin", bin)
	f.archive = "jevlin_" + version + "_" + osName + "_" + runtime.GOARCH + "." + ext
	var buf bytes.Buffer
	if ext == "zip" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(bin)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(content))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: bin, Mode: 0o755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	write(t, f.assets, f.archive, buf.String())
	sum := sha256.Sum256(buf.Bytes())
	f.sums = []byte(hex.EncodeToString(sum[:]) + "  " + f.archive + "\n")
	write(t, f.assets, "checksums.txt", string(f.sums))
	return f
}

// run is npm's postinstall, `node install.js`, with https.get stubbed.
func (f postinstallFixture) run(t *testing.T) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeBinary(t), "--require", filepath.Join(f.assets, "stub.js"), filepath.Join(f.pkg, "install.js")) // #nosec G204 -- node from PATH running this test's copy of install.js
	cmd.Dir = f.pkg
	cmd.Env = append(os.Environ(), "JEVLIN_TEST_ASSETS="+f.assets, "JEVLIN_SKIP_DOWNLOAD=", "JEVLIN_BINARY=")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		t.Fatalf("node: %v\n%s", err, out)
		return 0, ""
	}
}

func TestThePostinstallInstallsASignedRelease(t *testing.T) {
	f := newPostinstallFixture(t, "0.4.1", "the release binary\n")
	key, _ := testInstallerKey()
	write(t, f.assets, "checksums.txt.sig", string(selfupdate.SignChecksums(key, f.sums)))

	code, out := f.run(t)
	if code != 0 {
		t.Fatalf("a signed release did not install: exit %d\n%s", code, out)
	}
	if b, err := os.ReadFile(f.binary); err != nil || string(b) != "the release binary\n" {
		t.Fatalf("the binary the postinstall left: %q, %v", b, err)
	}
	requests := string(mustRead(t, filepath.Join(f.assets, "requests")))
	base := "https://github.com/jevlinai/jevlin-go/releases/download/v0.4.1/"
	for _, name := range []string{f.archive, "checksums.txt", "checksums.txt.sig"} {
		if !strings.Contains(requests, base+name+"\n") {
			t.Errorf("the postinstall never asked for %s%s; it asked for:\n%s", base, name, requests)
		}
	}
}

// The postinstall unpacks nothing unless checksums.txt carries a signature
// by a key the package trusts, however well the archive matches it.
func TestThePostinstallRefusesAReleaseItCannotAuthenticate(t *testing.T) {
	otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	key, _ := testInstallerKey()
	for name, c := range map[string]struct {
		sig  func(sums []byte) []byte // nil: no signature published
		want string
	}{
		"no signature published": {nil, "checksums.txt.sig: HTTP 404"},
		"signed by another key": {func(sums []byte) []byte { return selfupdate.SignChecksums(otherKey, sums) },
			"not signed by a release key this package trusts"},
		"checksums.txt rewritten to match a new archive": {func(sums []byte) []byte {
			return selfupdate.SignChecksums(key, []byte("an earlier checksums.txt\n"))
		}, "not signed by a release key this package trusts"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPostinstallFixture(t, "0.4.1", "not the project's binary\n")
			if c.sig != nil {
				write(t, f.assets, "checksums.txt.sig", string(c.sig(f.sums)))
			}
			code, out := f.run(t)
			if code == 0 {
				t.Fatalf("the postinstall exited 0:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("the refusal does not say %q:\n%s", c.want, out)
			}
			if _, err := os.Lstat(f.binary); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a binary was installed from a release the postinstall refused: %v", err)
			}
		})
	}
}

// The installer and the updater trust the same keys: the repository's own
// pair, and refusals for every way a tag's install.js could disagree.
func TestTheInstallerTrustsExactlyTheUpdatersKeys(t *testing.T) {
	keysSource := mustRead(t, filepath.Join("..", "..", releaseKeysFile))
	if n, err := CheckInstallerTrustsTheReleaseKeys(realInstallJS(t), keysSource); err != nil || n < 1 {
		t.Fatalf("the repository's own npm/install.js and %s: %d key(s), %v", releaseKeysFile, n, err)
	}

	real := string(realInstallJS(t))
	updaterKeys, err := releaseKeysFromSource(keysSource)
	if err != nil {
		t.Fatal(err)
	}
	_, testPub := testInstallerKey()
	withList := func(lines ...string) []byte {
		out := installerKeysList.ReplaceAllLiteralString(real, "\nconst RELEASE_PUBLIC_KEYS = [\n"+strings.Join(lines, "\n")+"\n]\n")
		if out == real && len(lines) > 0 {
			t.Fatal("fixture: npm/install.js has no RELEASE_PUBLIC_KEYS list to replace")
		}
		return []byte(out)
	}
	quoted := func(k string) string { return `  "` + k + `",` }

	var reversed []string
	for i := len(updaterKeys) - 1; i >= 0; i-- {
		reversed = append(reversed, quoted(updaterKeys[i]))
	}
	if _, err := CheckInstallerTrustsTheReleaseKeys(withList(reversed...), keysSource); err != nil {
		t.Errorf("the same keys in another order were refused: %v", err)
	}

	for name, c := range map[string]struct {
		installJS []byte
		want      string
	}{
		"another key":         {withList(quoted(testPub)), "trusts"},
		"one key more":        {withList(append([]string{quoted(testPub)}, reversed...)...), "trusts"},
		"an entry not quoted": {withList("  someKey,"), "is not one quoted key"},
		"no list at all":      {[]byte(strings.Replace(real, "const RELEASE_PUBLIC_KEYS = [", "const KEYS = [", 1)), "no single"},
	} {
		if _, err := CheckInstallerTrustsTheReleaseKeys(c.installJS, keysSource); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v; want a refusal saying %q", name, err, c.want)
		}
	}
}

// A tag whose installer trusts other keys than its updater is refused
// before anything is built: every npm install of it would refuse the
// release the workflow signs, in a version npm cannot take back.
func TestPreflightRefusesATagWhoseInstallerTrustsAnotherKey(t *testing.T) {
	r := newReleaseRepo(t, "0.2.8")
	_, testPub := testInstallerKey()
	src := string(realInstallJS(t))
	other := installerKeysList.ReplaceAllLiteralString(src, "\nconst RELEASE_PUBLIC_KEYS = [\n  \""+testPub+"\",\n]\n")
	if other == src {
		t.Fatal("fixture: npm/install.js has no RELEASE_PUBLIC_KEYS list to replace")
	}
	write(t, r.dir, "npm/install.js", other)
	commit(t, r.dir, "the installer trusts another key")
	r.annotate("v0.2.8")

	if _, err := r.preflight("v0.2.8"); err == nil || !strings.Contains(err.Error(), "npm/install.js at the tag trusts") {
		t.Fatalf("a tag whose installer trusts another key: err %v", err)
	}
}
