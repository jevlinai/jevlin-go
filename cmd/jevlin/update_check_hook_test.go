package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The real hook entry points start the daily check, through hookMain itself,
// main.go's `jevlin hook` path, with a config on disk naming the
// installation: Claude Code's session start and Stop, Cursor's stop, Codex's
// Stop and the JavaScript hosts' `hook start`. Before, every check test went
// through hookMainWith, a hand copy of the dispatch: emptying the real
// `case "start"` and dropping the installation directory from hookMain left
// the whole suite green while no production hook would ever start a check.
//
// And none of them starts one inside an agent's sandbox. A host started by
// Codex as a shell command runs its hooks there, with Codex's loopback proxy
// as its only route out; a check would ask that proxy for GitHub, a host
// Codex's profile does not list, and fail whatever Codex command is running
// (invariant 1). The flush, which reaches only hosts the profile lists,
// still starts.
func TestTheRealHooksStartTheCheckOnlyOutsideASandbox(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "jevlin.toml")
	writeFileT(t, cfg, "[miner]\nsessions_dir = \"/sessions\"\nintake_dir = \""+filepath.ToSlash(filepath.Join(root, "intake"))+"\"\n")
	entries := []struct {
		name    string
		args    []string
		payload map[string]any
	}{
		{"Claude Code session start", []string{"window", "session-start"}, map[string]any{"hook_event_name": "SessionStart"}},
		{"Claude Code Stop", []string{"flush"}, map[string]any{"hook_event_name": "Stop"}},
		{"Cursor stop", []string{"cursor", "stop"}, cursorPayloadFixture(t, "cursor-3.20.21-stop-runs-claude-flush.json")},
		{"opencode start", []string{"start", "opencode"}, map[string]any{}},
		{"Pi start", []string{"start", "pi"}, map[string]any{}},
	}
	if runtime.GOOS != "windows" {
		// Codex declares no hook runner on Windows, so its entry starts nothing there.
		entries = append(entries, struct {
			name    string
			args    []string
			payload map[string]any
		}{"Codex Stop", []string{"codex", "Stop"}, map[string]any{"hook_event_name": "Stop"}})
	}
	for _, env := range []struct {
		name  string
		env   map[string]string
		check bool
	}{
		{"no sandbox", map[string]string{}, true},
		{"a proxy elsewhere", map[string]string{"HTTPS_PROXY": "http://proxy.example.com:3128"}, true},
		{"Codex's loopback proxy", map[string]string{"HTTPS_PROXY": "http://127.0.0.1:18765"}, false},
		{"a lowercase loopback proxy", map[string]string{"https_proxy": "localhost:18765"}, false},
		{"Codex's sandbox marker", map[string]string{"CODEX_SANDBOX": "seatbelt"}, false},
	} {
		for _, e := range entries {
			t.Run(env.name+"/"+e.name, func(t *testing.T) {
				fs, ops := newFakeHookOps(env.env)
				var checks []string
				ops.spawnUpdateCheck = func(cfgPath string) error {
					checks = append(checks, cfgPath)
					return nil
				}
				runRealHookMain(t, fs, ops, cfg, e.args, e.payload)
				want := 0

				if env.check {
					want = 1
				}
				if len(checks) != want || (want == 1 && checks[0] != cfg) {
					t.Errorf("checks %v, want %d under %s", checks, want, cfg)
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Join(root, updateStampFile)); err == nil {
		t.Error("a hook wrote the stamp; only the check it starts may")
	}
}

// Pi's extension, rendered as install writes it, starts `hook start pi`
// when Pi fires session_start: the handler itself, run in Node, with a
// stand-in binary that records the command line it was started with.
// Before, only the install golden held it: deleting the handler's call
// failed nothing but the golden.
func TestPisSessionStartRunsHookStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a shell script; the handler's logic is the same JavaScript everywhere")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded Pi extension")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv.txt")
	bin := filepath.Join(dir, "jevlin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s ' \"$@\" > '"+argv+".tmp' && mv '"+argv+".tmp' '"+argv+"'\n"), 0o700); err != nil { // #nosec G306 -- a test stand-in that must be executable
		t.Fatal(err)
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const mod = await import('data:text/javascript;base64,'+Buffer.from(input.extension).toString('base64'));
 const handlers = {};
 mod.default({ on: (ev, fn) => { handlers[ev] = fn; } });
 if (!handlers.session_start) { process.stdout.write('no session_start handler'); process.exit(0); }
 await handlers.session_start({ type: 'session_start' }, {});
 const deadline = Date.now() + 5000;
 while (Date.now() < deadline && !fs.existsSync(input.argv)) await new Promise((r) => setTimeout(r, 50));
 process.stdout.write(fs.existsSync(input.argv) ? fs.readFileSync(input.argv, 'utf8') : 'nothing started');`
	input, err := json.Marshal(map[string]any{"extension": renderAgentScriptFor(piExtensionTS, shellPOSIX, "/etc/jevlin.toml", bin), "argv": argv})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extension: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "hook -config /etc/jevlin.toml start pi" {
		t.Errorf("session_start started %q, want `hook -config /etc/jevlin.toml start pi`", got)
	}
}
