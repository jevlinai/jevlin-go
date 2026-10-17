package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The launcher's classification, on the layout npm itself makes. The table
// test beside setup's launch check feeds launchKind paths someone typed;
// this one packs ./npm, installs the tarball with npm, globally and into a
// project, and runs the installed launcher, which reports the marker it
// hands the binary. Typed paths are what let every real global install of
// the scoped package go out classified unknown: npm puts a scoped package a
// directory deeper than the fixtures said.
//
// npm runs offline, with scripts off (so install.js downloads nothing) and
// the registry pointed at a closed loopback port, so nothing here can reach
// a real host. The binary the launcher starts is node itself, through
// JEVLIN_BINARY, asked to print JEVLIN_LAUNCH.
func TestNpmInstallsLaunchWithTheMarkerTheirLayoutSays(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the npm launcher")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal("npm is required to verify the npm launcher")
	}
	source, err := filepath.Abs(filepath.Join("..", "..", "npm"))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	env := npmTestEnv(work)

	npmRun := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, npm, args...) // #nosec G204 -- npm from PATH with arguments this test builds
		cmd.Env = env
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	packed := filepath.Join(work, "packed")
	if err := os.MkdirAll(packed, 0o700); err != nil {
		t.Fatal(err)
	}
	npmRun("pack", source, "--pack-destination", packed)
	tarballs, err := filepath.Glob(filepath.Join(packed, "*.tgz"))
	if err != nil || len(tarballs) != 1 {
		t.Fatalf("npm pack left %v (%v), want one tarball", tarballs, err)
	}

	global := filepath.Join(work, "global")
	project := filepath.Join(work, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	npmRun("install", "--global", "--prefix", global, tarballs[0])
	npmRun("install", "--prefix", project, tarballs[0])

	globalModules := filepath.Join(global, "lib", "node_modules")
	if runtime.GOOS == "windows" {
		globalModules = filepath.Join(global, "node_modules")
	}
	for _, c := range []struct {
		name, modules, want string
	}{
		{"a global install", globalModules, "npm:global"},
		{"a project install", filepath.Join(project, "node_modules"), "npm:local"},
	} {
		launcher := filepath.Join(append([]string{c.modules}, strings.Split(npmPackageName, "/")...)...)
		launcher = filepath.Join(launcher, "bin", "jevlin.js")
		if _, err := os.Stat(launcher); err != nil {
			t.Fatalf("%s: npm put no launcher at %s: %v", c.name, launcher, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmd := exec.CommandContext(ctx, node, launcher, "-e", "process.stdout.write(process.env.JEVLIN_LAUNCH || '')") // #nosec G204 -- node from PATH running the launcher npm installed here
		cmd.Env = append(env, "JEVLIN_BINARY="+node)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s: the launcher failed: %v\n%s", c.name, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != c.want {
			t.Errorf("%s: the launcher marked it %q, want %q", c.name, got, c.want)
		}
	}
}

// npmTestEnv is the environment npm and the launcher run under: npm's own
// settings removed from the inherited environment, so a developer's prefix
// or registry cannot leak in, then the ones this test needs.
func npmTestEnv(work string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToLower(key), "npm_config_") || key == "JEVLIN_BINARY" || key == "JEVLIN_LAUNCH" {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"npm_config_offline=true",
		"npm_config_ignore_scripts=true",
		"npm_config_audit=false",
		"npm_config_fund=false",
		"npm_config_update_notifier=false",
		"npm_config_registry=http://127.0.0.1:9/",
		"npm_config_cache="+filepath.Join(work, "npm-cache"),
		"npm_config_userconfig="+filepath.Join(work, "npmrc"),
	)
}
