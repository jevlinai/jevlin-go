package main

// Our region and the participant's lines it implies are one unit, and our
// own network is never left open.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openByUs is the one state no operation may leave: our profile active,
// with a network of its own, and network_proxy off — which opens every host
// to every command Codex runs. A network that is open by the participant's
// own profile is theirs, and our profile then has no network table.
func openByUs(t *testing.T, file string) bool {
	t.Helper()
	doc, ok := decodeTOMLDoc(file)
	if !ok {
		t.Fatalf("the file does not decode:\n%s", file)
	}
	if dp, _ := doc["default_permissions"].(string); dp != codexProfileName {
		return false
	}
	on, _ := lookupTOMLPath(doc, "permissions", codexProfileName, "network", "enabled")
	if b, _ := on.(bool); !b {
		return false
	}
	switch v, _ := lookupTOMLPath(doc, "features", "network_proxy"); x := v.(type) {
	case bool:
		return !x
	case map[string]any:
		e, _ := x["enabled"].(bool)
		return !e
	}
	return true
}

func assertNotOpenByUs(t *testing.T, when, file string) {
	t.Helper()
	if openByUs(t, file) {
		t.Errorf("%s: jevlin's profile is active with its network on and network_proxy off, open to every host:\n%s", when, file)
	}
}

// The property over every starting state and every answer: install,
// reinstall, and uninstall.
func TestNoOperationLeavesOurNetworkOpen(t *testing.T) {
	for _, st := range codexStates() {
		for _, answer := range []string{"y", "n", ""} {
			t.Run(st.name+"/"+answer, func(t *testing.T) {
				st := st
				if st.answer != "" {
					st.answer = answer
				}
				m, ops, cfgPath, _, got, _, _ := stateInstall(t, st)
				assertNotOpenByUs(t, "after install", got)
				runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
				assertNotOpenByUs(t, "after a second install", string(m.files[codexConfigPath]))
				runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes")
				assertNotOpenByUs(t, "after uninstall", string(m.files[codexConfigPath]))
			})
		}
	}
}

// installedOn installs a starting state with a typed yes and returns the
// machine as it stands.
func installedOn(t *testing.T, name string) (m *fakeMachine, ops agentOps, cfgPath, before string) {
	t.Helper()
	for _, st := range codexStates() {
		if st.name == name {
			m, ops, cfgPath, before = seedState(t, st)
			if code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
				t.Fatalf("install: exit %d\n%s", code, out)
			}
			return m, ops, cfgPath, before
		}
	}
	t.Fatalf("no state %s", name)
	return nil, agentOps{}, "", ""
}

// After a yes, `codex features disable network_proxy` deletes our proxy
// table (the capture is in testdata/codex/). Status says the network is
// open, and the next install, with no question to ask, puts the table back.
func TestAProxyTableDeletedAfterASwitchIsRestored(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "own-profile-network-absent")
	got := string(m.files[codexConfigPath])
	cut := strings.Replace(got, "[features.network_proxy]\nenabled = true\n\n", "", 1)
	if cut == got {
		t.Fatalf("this case needs our proxy table, and the switch did not write one:\n%s", got)
	}
	m.files[codexConfigPath] = []byte(cut)
	if !openByUs(t, cut) {
		t.Fatalf("the fixture does not reproduce the open network:\n%s", cut)
	}
	if _, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(out, "every command Codex runs can reach any host; agents install turns it on") {
		t.Errorf("status does not say the network is open:\n%s", out)
	}
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code != exitOK || strings.Contains(out, "already installed") {
		t.Errorf("install did not restore the table (exit %d):\n%s", code, out)
	}
	assertNotOpenByUs(t, "after install", string(m.files[codexConfigPath]))
}

// When uninstall leaves our block — here because a key was added inside
// our profile — it leaves the participant's lines it changed with it: a
// network_proxy turned back off beside a block that stays would open it.
func TestUninstallLeavesTheLinesItChangedWithABlockItLeaves(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "own-profile-proxy-false")
	got := string(m.files[codexConfigPath])
	edited := strings.Replace(got, "[permissions.jevlin.network]\nenabled = true\n", "[permissions.jevlin.network]\nenabled = true\nmode = \"full\"\n", 1)
	if edited == got {
		t.Fatalf("this case needs our network table:\n%s", got)
	}
	m.files[codexConfigPath] = []byte(edited)
	code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes")
	if after := string(m.files[codexConfigPath]); after != edited {
		t.Errorf("uninstall changed a file whose block it left (exit %d):\n%s\n%s", code, out, after)
	}
	if !strings.Contains(out, "left the jevlin block in /home/u/.codex/config.toml, and the lines of yours it changed") {
		t.Errorf("the plan does not say the lines stay with the block:\n%s", out)
	}
}

// The unit holds however this installation's uninstall finds its config:
// with no config at all, through a link to the same file, and from a new
// path naming the same directories. In every case the region and the
// default_permissions line naming it go together.
func TestUninstallTakesTheBlockAndItsLinesTogether(t *testing.T) {
	for _, how := range []string{"no config", "a link to the config", "the config moved"} {
		t.Run(how, func(t *testing.T) {
			m, ops, cfgPath, before := installedOn(t, "own-profile-network-absent")
			args := []string{"uninstall", "-client", "codex", "-yes"}
			switch how {
			case "a link to the config":
				link := filepath.Join(t.TempDir(), "via-link.toml")
				if err := os.Symlink(cfgPath, link); err != nil {
					t.Skipf("no symlinks here: %v", err)
				}
				args = append(args, "-config", link)
			case "the config moved":
				moved := filepath.Join(t.TempDir(), "moved.toml")
				b, err := os.ReadFile(cfgPath) // #nosec G304 -- this test's own config
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(moved, b, 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
					t.Fatal(err)
				}
				args = append(args, "-config", moved)
			}
			code, out := runAgentsAt(t, ops, "", args...)
			got := string(m.files[codexConfigPath])
			if strings.Contains(got, "[permissions.jevlin") {
				t.Fatalf("the block survived (exit %d):\n%s\n%s", code, out, got)
			}
			if got != before {
				t.Errorf("the participant's file did not come back as it was (exit %d)\n--- got ---\n%s\n--- want ---\n%s\n%s", code, got, before, out)
			}
		})
	}
}

// A line the participant changed after install is their newer choice:
// uninstall does not put the old one back over it.
func TestUninstallLeavesALineChangedAfterInstall(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "default-permissions-workspace")
	got := string(m.files[codexConfigPath])
	narrowed := strings.Replace(got, "default_permissions = \"jevlin\"  # jevlin agents install", "default_permissions = \":read-only\"  # jevlin agents install", 1)
	if narrowed == got {
		t.Fatalf("this case needs our marked default_permissions line:\n%s", got)
	}
	m.files[codexConfigPath] = []byte(narrowed)
	code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes")
	after := string(m.files[codexConfigPath])
	if doc, ok := decodeTOMLDoc(after); !ok || doc["default_permissions"] != ":read-only" {
		t.Errorf("uninstall widened a sandbox the participant had narrowed (exit %d):\n%s\n%s", code, out, after)
	}
	if !strings.Contains(out, "default_permissions in /home/u/.codex/config.toml was changed after jevlin set it, so it is left as you have it") {
		t.Errorf("the plan does not say why the line stayed:\n%s", out)
	}
}

// Our old block is ours: whatever the answer to a question about the
// participant's own settings — a no, no answer, no terminal, or the upgrade
// re-render's -yes with no terminal — the old block loses its open network.
func TestTheOldBlockIsClosedWhateverTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		stdin    string
		args     []string
		exit     int
	}{
		{"typed no", true, "n\n", []string{"-yes"}, exitOK},
		{"unanswered", true, "", nil, exitUsage},
		{"no terminal", false, "", []string{"-yes"}, exitUsage},
		{"no terminal, no -yes", false, "", nil, exitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			m, ops := newFakeMachine("codex")
			m.terminal = tc.terminal
			old := "default_permissions = \":workspace\"\n\n" + agentsMarkerBegin + "\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [" + quotedRootsOf(t, cfgPath) + "]\n" + agentsMarkerEnd + "\n"
			m.files[codexConfigPath] = []byte(old)
			args := append([]string{"install", "-config", cfgPath, "-client", "codex"}, tc.args...)
			code, out := runAgentsAt(t, ops, tc.stdin, args...)
			if code != tc.exit {
				t.Errorf("exit %d, want %d\n%s", code, tc.exit, out)
			}
			got := string(m.files[codexConfigPath])
			if strings.Contains(got, "network_access") {
				t.Errorf("the old block kept its open network:\n%s\n%s", got, out)
			}
			if r, had, why := readCodexRegion([]byte(got)); !had || why != "" || !r.legacy || len(r.roots) == 0 {
				t.Errorf("the old block lost its roots (had=%v why=%q):\n%s", had, why, got)
			}
			if !strings.Contains(got, "default_permissions = \":workspace\"") || strings.Contains(got, "[permissions.jevlin") {
				t.Errorf("the participant's own setting changed without a yes:\n%s", got)
			}
			if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; ok {
				t.Errorf("the skill was written without a yes:\n%s", out)
			}
		})
	}
}
