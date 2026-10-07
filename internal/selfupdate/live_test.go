package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLiveReleaseVerification runs the real pipeline against the published
// v0.2.8 release, which predates release signing: discovery and selection
// succeed, and the updater must then refuse it for carrying no
// checksums.txt.sig, before downloading or staging anything. Nothing is
// replaced, and the "installed binary" is a placeholder in a temporary
// directory. It is opt-in, because ordinary tests must not depend on GitHub:
//
//	JEVLIN_LIVE_RELEASE=1 go test ./internal/selfupdate -run TestLiveReleaseVerification -count=1 -v
func TestLiveReleaseVerification(t *testing.T) {
	if os.Getenv("JEVLIN_LIVE_RELEASE") != "1" {
		t.Skip("set JEVLIN_LIVE_RELEASE=1 to verify against the published release")
	}
	v, err := ParseVersion("0.2.8")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), OperationTimeout)
	defer cancel()
	source := NewHTTPSource(nil)
	release, err := source.Release(ctx, &v)
	if err != nil {
		t.Fatal(err)
	}
	for name, asset := range release.Assets {
		t.Logf("release %s asset %s: %d bytes", release.Version.Tag(), name, asset.Size)
	}
	target, err := ArtifactFor(release.Version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	placeholder := filepath.Join(t.TempDir(), "bin", target.ExecutableName)
	if err := os.MkdirAll(filepath.Dir(placeholder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placeholder, []byte("placeholder"), 0o700); err != nil { // #nosec G306 -- a placeholder binary
		t.Fatal(err)
	}
	// A fictional older build, so the real v0.2.8 is the upgrade target.
	_, err = Updater{Source: source, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}.Prepare(ctx, placeholder, "0.2.7", &v)
	if KindOf(err) != KindReleaseInvalid || !strings.Contains(err.Error(), SignatureAssetName) {
		t.Fatalf("an unsigned release must be refused for its missing %s, got %v", SignatureAssetName, err)
	}
	t.Logf("refused unsigned %s: %v", release.Version.Tag(), err)
	if left, err := StagingLeftovers(filepath.Dir(placeholder)); err != nil || len(left) != 0 {
		t.Errorf("nothing may be staged from an unsigned release: %v %v", left, err)
	}
	if b, _ := os.ReadFile(placeholder); string(b) != "placeholder" { // #nosec G304 -- test path
		t.Error("the placeholder installed binary must be untouched")
	}
}
