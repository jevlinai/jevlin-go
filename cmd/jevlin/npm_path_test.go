package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The package directory is where the real binary lives and never what PATH
// names. The fixtures are strings, so both layouts run on every OS.
func TestTheNpmBinDirIsDerivedFromThePackagePath(t *testing.T) {
	for _, c := range []struct{ name, exe, want string }{
		{"unix", "/usr/local/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin", "/usr/local/bin"},
		{"unix home prefix", "/home/u/.npm-global/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin", "/home/u/.npm-global/bin"},
		{"unix prefix with a space", "/home/u/my npm/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin", "/home/u/my npm/bin"},
		{"windows", `C:\Users\u\AppData\Roaming\npm\node_modules\@jevlinsearch\jevlin\bin\jevlin.exe`, `C:\Users\u\AppData\Roaming\npm`},
		{"windows case", `c:\Users\u\npm\Node_Modules\@JevlinSearch\Jevlin\bin\jevlin.exe`, `c:\Users\u\npm`},
		{"windows drive root", `C:\node_modules\@jevlinsearch\jevlin\bin\jevlin.exe`, `C:\`},
		{"windows unc", `\\srv\share\npm\node_modules\@jevlinsearch\jevlin\bin\jevlin.exe`, `\\srv\share\npm`},
		{"a project install", "/work/app/node_modules/@jevlinsearch/jevlin/bin/jevlin", ""},
		{"a native binary", "/home/u/.jevlin/bin/jevlin", ""},
		{"another package", "/usr/local/lib/node_modules/@other/jevlin/bin/jevlin", ""},
	} {
		if got := npmShimDirFromPackagePath(c.exe); got != c.want {
			t.Errorf("%s: npmShimDirFromPackagePath(%q) = %q, want %q", c.name, c.exe, got, c.want)
		}
	}
}

func TestTheLaunchersNpmBinDirWinsAndIsHeldToAPath(t *testing.T) {
	exe := "/usr/local/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin"
	for _, c := range []struct{ name, env, want string }{
		{"the launcher's answer", "/opt/npm/bin", "/opt/npm/bin"},
		{"a windows answer", `D:\tools\npm`, `D:\tools\npm`},
		{"not set", "", "/usr/local/bin"},
		{"relative", "npm/bin", "/usr/local/bin"},
		{"a control character", "/opt/npm\n/bin", "/usr/local/bin"},
	} {
		got := npmBinDir(exe, func(k string) string {
			if k == launchBinEnv {
				return c.env
			}
			return ""
		})
		if got != c.want {
			t.Errorf("%s: npmBinDir = %q, want %q", c.name, got, c.want)
		}
	}
}

// The launcher's own derivation, run by node on typed paths of both systems:
// the Windows layout cannot be installed here.
func TestTheLauncherNamesNpmsBinDirOnBothLayouts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the npm launcher")
	}
	script := `
const { launchBinDir } = require(process.argv[1])
const has = (...ps) => (f) => ps.includes(f)
const out = [
  launchBinDir("/usr/local/lib/node_modules/@jevlinsearch/jevlin", "linux", has("/usr/local/bin/jevlin")),
  launchBinDir("/usr/local/lib/node_modules/@jevlinsearch/jevlin", "linux", has()),
  launchBinDir("/work/app/node_modules/@jevlinsearch/jevlin", "linux", has("/work/bin/jevlin")),
  launchBinDir("C:\\Users\\u\\AppData\\Roaming\\npm\\node_modules\\@jevlinsearch\\jevlin", "win32", has("C:\\Users\\u\\AppData\\Roaming\\npm\\jevlin.cmd")),
  launchBinDir("C:\\Users\\u\\AppData\\Roaming\\npm\\node_modules\\@jevlinsearch\\jevlin", "win32", has()),
]
process.stdout.write(out.map(String).join("\n"))`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script, "../../npm/bin/jevlin.js") // #nosec G204 -- node from PATH running a fixed script
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	want := []string{
		"/usr/local/bin",
		"null", // the shim is not there: no guess is passed on
		"null", // a project install has no global command
		`C:\Users\u\AppData\Roaming\npm`,
		"null",
	}
	got := strings.Split(string(out), "\n")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("launchBinDir answers %q, want %q", got, want)
	}
}
