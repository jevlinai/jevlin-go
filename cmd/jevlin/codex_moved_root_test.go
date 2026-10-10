package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moveStateDir points the config's state_dir at a new directory outside
// its home, as a participant who moves it does, and returns that path.
func moveStateDir(t *testing.T, cfgPath, home string) string {
	t.Helper()
	b, err := os.ReadFile(cfgPath) // #nosec G304 -- the test's own config, in t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "state-moved")
	if err := os.MkdirAll(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	old := "state_dir = " + mustTOMLString(filepath.ToSlash(filepath.Join(home, "state")))
	text := strings.Replace(string(b), old, "state_dir = "+mustTOMLString(filepath.ToSlash(moved)), 1)
	if text == string(b) {
		t.Fatalf("the config has no %s to move:\n%s", old, b)
	}
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil { // #nosec G703 -- the test's own config, in t.TempDir()
		t.Fatal(err)
	}
	return moved
}

// After a participant moves state_dir, the block this installation wrote
// grants the old directory and not the new one, and every root is still
// under the home that holds this installation's config. It is still this
// installation's: install rewrites it with the directories the config
// names now and says the old one goes, and uninstall removes it. Both used
// to answer that it belonged to "the installation configured by" this
// very config, leave it, and exit 0 — so neither could ever fix it. The
// same holds for the profile, for the earlier version's block on Linux,
// and for the Windows block.
func TestAMovedStateDirLeavesTheBlockThisInstallations(t *testing.T) {
	for _, tc := range []struct{ name, goos string }{
		{"the profile", "linux"},
		{"the old block", "linux"},
		{"the Windows block", "windows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, home := sandboxTestConfig(t)
			onCodexOS(t, tc.goos)
			m, ops := newFakeMachine("codex")
			if tc.name == "the old block" {
				var st codexState
				for _, s := range codexStates() {
					if s.name == "old-block" {
						st = s
					}
				}
				m.files[codexConfigPath] = []byte(strings.ReplaceAll(st.before, "STATEROOTS", quotedRootsOf(t, cfgPath)))
			} else {
				m.files[codexConfigPath] = []byte("model = \"gpt-5\"\n")
				if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
					t.Fatalf("first install: exit %d\n%s", code, out)
				}
			}
			oldState := filepath.Join(home, "state")
			stale := string(m.files[codexConfigPath])
			if !strings.Contains(stale, mustTOMLString(oldState)) {
				t.Fatalf("the block does not grant the state dir it is about to lose:\n%s", stale)
			}
			moved := moveStateDir(t, cfgPath, home)

			code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
			got := string(m.files[codexConfigPath])
			if code != exitOK || strings.Contains(out, "belongs to") {
				t.Fatalf("install did not take the block as this installation's (exit %d):\n%s", code, out)
			}
			if !strings.Contains(got, mustTOMLString(moved)) || strings.Contains(got, mustTOMLString(oldState)) {
				t.Errorf("the block does not grant the moved state dir in place of the old one:\n%s", got)
			}
			if !strings.Contains(out, "which this installation's config no longer names") || !strings.Contains(out, oldState) {
				t.Errorf("the plan does not say the old state dir is dropped:\n%s", out)
			}

			m.files[codexConfigPath] = []byte(stale)
			code, out = runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes")
			if code != exitOK || strings.Contains(string(m.files[codexConfigPath]), agentsMarkerBegin) {
				t.Errorf("uninstall did not remove this installation's block (exit %d):\n%s\n%s", code, out, m.files[codexConfigPath])
			}
		})
	}
}

// installedByAWithADenyAdded is installation A's profile, installed with a
// yes over a ":workspace" file, with a deny the participant then added
// inside it: a block no installation can rewrite or remove.
func installedByAWithADenyAdded(t *testing.T) (m *fakeMachine, ops agentOps, cfgA, cfgB, edited string) {
	t.Helper()
	cfgA, _ = sandboxTestConfig(t)
	cfgB, _ = sandboxTestConfig(t)
	m, ops = newFakeMachine("codex")
	m.terminal = true
	m.files[codexConfigPath] = []byte("default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n\n[mcp_servers.foo]\ncommand = \"/bin/echo\"\n")
	if code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgA, "-client", "codex", "-yes"); code != exitOK {
		t.Fatalf("A's install: exit %d\n%s", code, out)
	}
	m.terminal = false
	got := string(m.files[codexConfigPath])
	const domains = "[permissions.jevlin.network.domains]\n"
	edited = strings.Replace(got, domains, domains+"\"pypi.org\" = \"deny\"\n", 1)
	if edited == got {
		t.Fatalf("no domains table to add to:\n%s", got)
	}
	m.files[codexConfigPath] = []byte(edited)
	return m, ops, cfgA, cfgB, edited
}

// Installation A's block, which the participant changed so that no client
// can take it out, is still A's: B's install and uninstall name A and
// leave it, exit 0, and neither refuses over it nor tells the participant
// to remove it by hand. B's uninstall exited 1 with "remove the block by
// hand", and following that advice left A's marked default_permissions
// line naming a profile that was gone, which Codex refuses.
func TestAnotherInstallationsUnreadableBlockIsNamedNotRefused(t *testing.T) {
	m, ops, cfgA, cfgB, edited := installedByAWithADenyAdded(t)
	for _, verb := range []string{"uninstall", "install"} {
		code, out := runAgentsAt(t, ops, "", verb, "-config", cfgB, "-client", "codex", "-yes")
		if code != exitOK || strings.Contains(out, "refused") || strings.Contains(out, "by hand") {
			t.Errorf("B's %s refused over A's block (exit %d):\n%s", verb, code, out)
		}
		if !strings.Contains(out, "belongs to the installation configured by") || !strings.Contains(out, filepath.Base(filepath.Dir(cfgA))) {
			t.Errorf("B's %s does not name A:\n%s", verb, out)
		}
		if string(m.files[codexConfigPath]) != edited {
			t.Errorf("B's %s changed the file:\n%s", verb, m.files[codexConfigPath])
		}
	}
}
