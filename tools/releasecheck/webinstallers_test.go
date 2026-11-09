package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The scripts jevlin.ai serves trust exactly the updater's keys: the
// repository's own files, and refusals for every way a tag's script could
// disagree or be written in a shape this check does not read.
func TestTheWebInstallersTrustExactlyTheUpdatersKeys(t *testing.T) {
	keysSource := mustRead(t, filepath.Join("..", "..", releaseKeysFile))
	realSh := string(mustRead(t, filepath.Join("..", "..", shellInstallerFile)))
	realPs := string(mustRead(t, filepath.Join("..", "..", powershellInstallerFile)))
	if n, err := CheckWebInstallersTrustTheReleaseKeys([]byte(realSh), []byte(realPs), keysSource); err != nil || n < 1 {
		t.Fatalf("the repository's own installers and %s: %d key(s), %v", releaseKeysFile, n, err)
	}

	updaterKeys, err := releaseKeysFromSource(keysSource)
	if err != nil {
		t.Fatal(err)
	}
	_, testPub := testInstallerKey()
	reversed := make([]string, 0, len(updaterKeys))
	for i := len(updaterKeys) - 1; i >= 0; i-- {
		reversed = append(reversed, updaterKeys[i])
	}

	shWith := func(value string) []byte {
		if !shellKeysLine.MatchString(realSh) {
			t.Fatalf("fixture: %s has no RELEASE_PUBLIC_KEYS line to replace", shellInstallerFile)
		}
		return []byte(shellKeysLine.ReplaceAllLiteralString(realSh, "RELEASE_PUBLIC_KEYS="+value))
	}
	psWith := func(value string) []byte {
		if !psKeysLine.MatchString(realPs) {
			t.Fatalf("fixture: %s has no $releaseKeys line to replace", powershellInstallerFile)
		}
		return []byte(psKeysLine.ReplaceAllLiteralString(realPs, "    $releaseKeys = "+value))
	}
	shList := func(keys ...string) string { return `"` + strings.Join(keys, " ") + `"` }
	psList := func(keys ...string) string {
		quoted := make([]string, len(keys))
		for i, k := range keys {
			quoted[i] = `"` + k + `"`
		}
		return "@(" + strings.Join(quoted, ", ") + ")"
	}

	if _, err := CheckWebInstallersTrustTheReleaseKeys(shWith(shList(reversed...)), psWith(psList(reversed...)), keysSource); err != nil {
		t.Errorf("the same keys in another order were refused: %v", err)
	}

	for name, c := range map[string]struct {
		sh, ps []byte
		want   string
	}{
		"install.sh trusts another key":  {shWith(shList(testPub)), []byte(realPs), shellInstallerFile + " at the tag trusts"},
		"install.ps1 trusts another key": {[]byte(realSh), psWith(psList(testPub)), powershellInstallerFile + " at the tag trusts"},
		"install.sh trusts one key more": {shWith(shList(append([]string{testPub}, reversed...)...)), []byte(realPs), shellInstallerFile + " at the tag trusts"},
		"install.ps1 trusts one more":    {[]byte(realSh), psWith(psList(append([]string{testPub}, reversed...)...)), powershellInstallerFile + " at the tag trusts"},
		"install.sh, a key unquoted":     {shWith(updaterKeys[0]), []byte(realPs), "is not one quoted list"},
		"install.sh, a variable":         {shWith(`"$KEYS"`), []byte(realPs), "is not one quoted list"},
		"install.ps1, a bare string":     {[]byte(realSh), psWith(`"` + updaterKeys[0] + `"`), "is not one @(...)"},
		"install.sh, a second line":      {[]byte(realSh + "RELEASE_PUBLIC_KEYS=" + shList(testPub) + "\n"), []byte(realPs), "no single"},
		"install.sh, no line":            {[]byte(strings.Replace(realSh, "\nRELEASE_PUBLIC_KEYS=", "\nKEYS=", 1)), []byte(realPs), "no single"},
		"install.ps1, no line":           {[]byte(realSh), []byte(strings.Replace(realPs, "$releaseKeys =", "$keys =", 1)), "no single"},
	} {
		if _, err := CheckWebInstallersTrustTheReleaseKeys(c.sh, c.ps, keysSource); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v; want a refusal saying %q", name, err, c.want)
		}
	}
}

// A tag whose website installer trusts another key than its updater is
// refused before anything is built.
func TestPreflightRefusesATagWhoseWebInstallerTrustsAnotherKey(t *testing.T) {
	_, testPub := testInstallerKey()
	for _, file := range []string{shellInstallerFile, powershellInstallerFile} {
		t.Run(file, func(t *testing.T) {
			r := newReleaseRepo(t, "0.2.8")
			src := string(mustRead(t, filepath.Join("..", "..", file)))
			var other string
			if file == shellInstallerFile {
				other = shellKeysLine.ReplaceAllLiteralString(src, `RELEASE_PUBLIC_KEYS="`+testPub+`"`)
			} else {
				other = psKeysLine.ReplaceAllLiteralString(src, `    $releaseKeys = @("`+testPub+`")`)
			}
			if other == src {
				t.Fatalf("fixture: %s has no key line to replace", file)
			}
			write(t, r.dir, file, other)
			commit(t, r.dir, "the website installer trusts another key")
			r.annotate("v0.2.8")

			if _, err := r.preflight("v0.2.8"); err == nil || !strings.Contains(err.Error(), file+" at the tag trusts") {
				t.Fatalf("a tag whose %s trusts another key: err %v", file, err)
			}
		})
	}
}
