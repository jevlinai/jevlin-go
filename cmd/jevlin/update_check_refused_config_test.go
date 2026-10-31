package main

// A machine that took this release unattended has an earlier version's
// config, which every command refuses until setup runs (refusedConfigUpdate,
// update_check.go). The daily check and a rollback's hold must still find
// the installation, or the machine stops looking for releases, and a
// rollback to the earlier version is undone by the next day's check.

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

func earlierConfigBody(stateDir, extra string) string {
	return "[[provider]]\nname = \"search-router\"\nupstream = \"https://api.jevlin.ai\"\n\n" +
		"[mining]\nstate_dir = \"" + filepath.ToSlash(stateDir) + "\"\n" + extra
}

func TestHooksOnARefusedConfigStillStartTheDailyCheck(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "jevlin.toml")
	entries := []struct {
		args    []string
		payload map[string]any
	}{
		{[]string{"window", "session-start"}, map[string]any{"hook_event_name": "SessionStart"}},
		{[]string{"stop"}, map[string]any{"hook_event_name": "Stop"}},
		{[]string{"codex", "Stop"}, map[string]any{"hook_event_name": "Stop"}},
	}
	for _, c := range []struct {
		name, extra string
		want        bool
	}{
		{"the default mode", "", true},
		{"the file's own mode, off", "\n[update]\nmode = \"off\"\n", false},
	} {
		writeFileT(t, cfg, earlierConfigBody(filepath.Join(root, "state"), c.extra))
		if _, _, err := loadConfig(cfg, noEnv); err == nil {
			t.Fatal("the fixture's config loads; it must be one every command refuses")
		}
		for _, e := range entries {
			fs, ops := newFakeHookOps(map[string]string{})
			run := runRealHookMain(t, fs, ops, cfg, e.args, e.payload)
			want := c.want
			if e.args[0] == "codex" {
				// Nothing has established what runs a Codex hook on Windows,
				// so a Codex entry does nothing there (hookCodexOn).
				_, err := declaredShells(codexTarget{}, runtime.GOOS, channelHook)
				want = want && err == nil
			}
			if got := run.checks > 0; got != want {
				t.Errorf("%s, hook %v: started %d check(s), want a check %v", c.name, e.args, run.checks, want)
			}
		}
	}
}

// The root is the parent of the state directory the earlier file names,
// where that version kept its stamp, and else the file's own directory.
func TestARefusedConfigsUpdateRootIsItsInstallationDirectory(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "home", "jevlin.toml")
	elsewhere := filepath.Join(root, "elsewhere", "state")
	for _, c := range []struct{ body, want string }{
		{earlierConfigBody(elsewhere, ""), filepath.Dir(elsewhere)},
		// 0.6.x kept the stamp beside [miner] intake_dir, wherever state_dir was.
		{earlierConfigBody(elsewhere, "\n[miner]\nintake_dir = \""+filepath.ToSlash(filepath.Join(filepath.Dir(cfg), "intake"))+"\"\n"), filepath.Dir(cfg)},
		{"[[provider]]\nname = \"search-router\"\n", filepath.Dir(cfg)},
		{"not = [toml", filepath.Dir(cfg)},
	} {
		writeFileT(t, cfg, c.body)
		if got, _ := refusedConfigUpdate(cfg); got != c.want {
			t.Errorf("%q: root %q, want %q", c.body, got, c.want)
		}
	}
	if got, _ := refusedConfigUpdate(""); got != "" {
		t.Errorf("no config path gave root %q", got)
	}
}

// A rollback made while the config is an earlier version's still holds the
// release it left.
func TestARollbackOnARefusedConfigHoldsTheReleaseItLeft(t *testing.T) {
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.1" {
		t.Fatalf("the automatic upgrade: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	writeFileT(t, cfgPath, earlierConfigBody(filepath.Join(f.home, "state"), ""))
	f.build = "0.3.1"
	if code, out, errOut := f.run("-rollback"); code != exitOK || f.content(f.exe) != "0.3.0" {
		t.Fatalf("the rollback: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	if st := readStampT(t, stamp); st.Outcome != updateRolledBack || st.Held != "0.3.1" {
		t.Fatalf("the rollback on an earlier config held nothing: %+v", st)
	}
}

// The check a hook starts on a refused config runs: it looks the release
// up, writes its stamp, and under the default mode installs it, since a
// release that runs on this machine is what it needs.
func TestTheDailyCheckOnARefusedConfigLooksForARelease(t *testing.T) {
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	writeFileT(t, cfgPath, earlierConfigBody(filepath.Join(f.home, "state"), ""))
	if _, _, err := loadConfig(cfgPath, noEnv); err == nil {
		t.Fatal("the fixture's config loads; it must be one every command refuses")
	}
	code, out, errOut := f.runAuto(cfgPath)
	if code != exitOK || f.content(f.exe) != "0.3.1" {
		t.Fatalf("the check on a refused config: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	if st := readStampT(t, stamp); st.Outcome != updateUpgraded {
		t.Errorf("the stamp after the check: %+v", st)
	}
}

// The earlier file's own [update] mode = "off" still means no check.
func TestTheDailyCheckOnARefusedConfigKeepsItsModeOff(t *testing.T) {
	f, cfgPath, _ := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	writeFileT(t, cfgPath, earlierConfigBody(filepath.Join(f.home, "state"), "\n[update]\nmode = \"off\"\n"))
	if code, _, _ := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.0" {
		t.Fatalf("mode off on a refused config: exit %d, binary %s", code, f.content(f.exe))
	}
}

// A refused config's [update] mode reads as both formats read it.
func TestARefusedConfigsModeReadsAsBothFormatsReadIt(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), setupConfigFile)
	for mode, want := range map[string]string{"": config.UpdateApply, "apply": config.UpdateApply, "notify": config.UpdateNotify, "off": config.UpdateOff, "manual": config.UpdateNotify} {
		extra := ""
		if mode != "" {
			extra = "\n[update]\nmode = \"" + mode + "\"\n"
		}
		writeFileT(t, cfg, earlierConfigBody("/x/state", extra))
		if _, got := refusedConfigUpdate(cfg); got != want {
			t.Errorf("mode %q read as %q, want %q", mode, got, want)
		}
	}
}
