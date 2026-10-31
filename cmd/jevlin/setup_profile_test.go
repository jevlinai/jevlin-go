package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTheCurrentShellFixIsSpelledForTheShell(t *testing.T) {
	for _, c := range []struct {
		name string
		d    shellDialect
		dir  string
		want string
	}{
		{"posix", dialectPOSIX, "/usr/local/bin", `export PATH="$PATH:/usr/local/bin"`},
		{"posix, a directory with a space", dialectPOSIX, "/home/Jane Doe/npm/bin", `export PATH="$PATH:/home/Jane Doe/npm/bin"`},
		{"posix, characters a shell would expand", dialectPOSIX, "/x/$HOME/`id`/\"q\"", `export PATH="$PATH:/x/\$HOME/\` + "`id\\`" + `/\"q\""`},
		{"fish", dialectFish, "/usr/local/bin", `set -gx PATH $PATH '/usr/local/bin'`},
		{"fish, a quote", dialectFish, "/x/it's", `set -gx PATH $PATH '/x/it\'s'`},
		{"powershell", dialectPowerShell, `C:\Users\u\AppData\Roaming\npm`, `$env:Path += ";C:\Users\u\AppData\Roaming\npm"`},
		{"powershell, a dollar and a quote", dialectPowerShell, `C:\x$y"z`, "$env:Path += \";C:\\x`$y`\"z\""},
	} {
		if got := currentShellFix(c.d, c.dir); got != c.want {
			t.Errorf("%s: currentShellFix = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestTheProfileLinesAreEachShellsOwn(t *testing.T) {
	posix := envLinesFor(dialectPOSIX, "/opt/n b/bin", "/h/jevlin.toml")
	if len(posix) != 2 || !strings.HasPrefix(posix[0], `case ":$PATH:" in *:'/opt/n b/bin':*)`) || posix[1] != "export JEVLIN_CONFIG='/h/jevlin.toml'" {
		t.Errorf("posix lines: %q", posix)
	}
	fish := envLinesFor(dialectFish, "/opt/n b/bin", "/h/jevlin.toml")
	want := []string{"contains -- '/opt/n b/bin' $PATH; or set -gx PATH $PATH '/opt/n b/bin'", "set -gx JEVLIN_CONFIG '/h/jevlin.toml'"}
	if strings.Join(fish, "\n") != strings.Join(want, "\n") {
		t.Errorf("fish lines: %q, want %q", fish, want)
	}
	// No directory, no PATH line.
	for _, d := range []shellDialect{dialectPOSIX, dialectFish} {
		if got := envLinesFor(d, "", "/h/jevlin.toml"); len(got) != 1 || !strings.Contains(got[0], "JEVLIN_CONFIG") {
			t.Errorf("dialect %d without a directory: %q", d, got)
		}
	}
}

// What uninstall reads back from a block must understand a fish block as it
// does a POSIX one, or a fish profile's block would be left behind.
func TestAFishBlockNamesItsConfigToUninstall(t *testing.T) {
	cfg := "/h/it's/jevlin.toml"
	for _, d := range []shellDialect{dialectPOSIX, dialectFish} {
		block := envLinesFor(d, "/opt/npm/bin", cfg)
		if !profileBlockNamesConfig(block, cfg) {
			t.Errorf("dialect %d: the block does not name its own config: %q", d, block)
		}
		if profileBlockNamesConfig(block, "/h/other.toml") {
			t.Errorf("dialect %d: the block names another config", d)
		}
	}
	files := profileFiles("/h", func(k string) string { return "" })
	found := false
	for _, f := range files {
		found = found || f == filepath.Join("/h", ".config", "fish", "config.fish")
	}
	if !found {
		t.Errorf("uninstall does not look in fish's config: %q", files)
	}
}

func TestDirOnPath(t *testing.T) {
	list := strings.Join([]string{"/usr/bin", "", "/opt/npm/bin/", "/x"}, string(filepath.ListSeparator))
	if !dirOnPath(list, "/opt/npm/bin") || !dirOnPath(list, "/usr/bin") {
		t.Error("an entry on PATH was not found")
	}
	if dirOnPath(list, "/opt/npm") || dirOnPath(list, "") || dirOnPath("", "/x") {
		t.Error("a directory not on PATH was found")
	}
}
