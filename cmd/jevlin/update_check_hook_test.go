package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
				run := runRealHookMain(t, fs, ops, cfg, e.args, e.payload)
				if run.flushes != 1 {
					t.Errorf("flushes %d, want 1", run.flushes)
				}
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
