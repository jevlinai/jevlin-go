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
	cut := strings.Replace(got, "\n[features.network_proxy]\nenabled = true\n", "\n", 1)
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

// Our profile with a network of its own loses that network when the
// participant keeps network_proxy off: here their [features] table turned
// it on, so ours wrote no proxy table, and then they turned it off again.
// A typed no to turning it back on keeps their setting and closes ours.
func TestOurProfileLosesItsNetworkWhenTheProxyStaysOff(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "features-network-proxy-true")
	got := string(m.files[codexConfigPath])
	off := strings.Replace(got, "network_proxy = true", "network_proxy = false", 1)
	if off == got || !openByUs(t, off) {
		t.Fatalf("the fixture does not reproduce our open network:\n%s", off)
	}
	m.files[codexConfigPath] = []byte(off)
	code, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	after := string(m.files[codexConfigPath])
	assertNotOpenByUs(t, "after a typed no", after)
	if code != exitOK || !strings.Contains(out, "drop its network") || !strings.Contains(after, "network_proxy = false") {
		t.Errorf("the typed no did not keep their setting and close ours (exit %d):\n%s\n%s", code, out, after)
	}
}

// Without its region — taken out by hand — a marked line is this
// installation's when its mark names the same config file, reached here
// through a link.
func TestAMarkWithoutItsRegionIsAttributedThroughALink(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "default-permissions-workspace")
	got := string(m.files[codexConfigPath])
	noRegion, had := removeMarkedBlock([]byte(got))
	if !had {
		t.Fatalf("no region to take out:\n%s", got)
	}
	m.files[codexConfigPath] = noRegion
	link := filepath.Join(t.TempDir(), "via-link.toml")
	if err := os.Symlink(cfgPath, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	code, out := runAgentsAt(t, ops, "", "uninstall", "-config", link, "-client", "codex", "-yes")
	if doc, ok := decodeTOMLDoc(string(m.files[codexConfigPath])); !ok || doc["default_permissions"] != ":workspace" {
		t.Errorf("the marked line was not put back through the link (exit %d):\n%s\n%s", code, out, m.files[codexConfigPath])
	}
}

// The region never goes while default_permissions would still name it: a
// line naming our profile that carries no mark — here the participant
// deleted ours — cannot be put back, so the region stays, and the plan
// says why. Codex refuses a default_permissions naming a profile that does
// not exist.
func TestUninstallNeverLeavesDefaultPermissionsNamingARemovedProfile(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "default-permissions-workspace")
	got := string(m.files[codexConfigPath])
	i := strings.Index(got, "  # jevlin agents install")
	j := strings.Index(got[i:], "\n")
	if i < 0 || j < 0 {
		t.Fatalf("no marked line:\n%s", got)
	}
	unmarked := got[:i] + got[i+j:]
	m.files[codexConfigPath] = []byte(unmarked)
	code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes")
	if string(m.files[codexConfigPath]) != unmarked {
		t.Errorf("uninstall removed the profile a default_permissions line still names (exit %d):\n%s\n%s", code, out, m.files[codexConfigPath])
	}
	if !strings.Contains(out, `default_permissions would still name "jevlin" once the block was gone`) {
		t.Errorf("the plan does not say why the block stayed:\n%s", out)
	}
}

// The by-hand text printed with no terminal can be followed as printed:
// applied to the participant's file, it is a file Codex loads, with our
// profile active and default_permissions set once.
func TestTheByHandTextCanBeFollowed(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	before := "default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n\n[tui]\nx = 1\n"
	m.files[codexConfigPath] = []byte(before)
	_, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if !strings.Contains(out, `change default_permissions = ":workspace" to default_permissions = "jevlin",`) {
		t.Fatalf("the by-hand text does not say which line to change:\n%s", out)
	}
	k := strings.Index(out, "and add these lines before your first table")
	if k < 0 {
		t.Fatalf("no lines to add:\n%s", out)
	}
	var added []string
	for _, l := range strings.Split(out[k:], "\n")[1:] {
		if !strings.HasPrefix(l, "    ") {
			break
		}
		added = append(added, strings.TrimPrefix(l, "    "))
	}
	followed := strings.Replace(before, `default_permissions = ":workspace"`, `default_permissions = "jevlin"`, 1)
	followed = strings.Replace(followed, "[tui]", strings.Join(added, "\n")+"\n[tui]", 1)
	if err := codexTOMLError(followed); err != nil {
		t.Errorf("following the by-hand text gives a file Codex refuses: %v\n%s", err, followed)
	}
	if doc, ok := decodeTOMLDoc(followed); !ok || doc["default_permissions"] != codexProfileName {
		t.Errorf("following it does not make jevlin's profile the default:\n%s", followed)
	}
}

// Closing our old block takes out its network_access = true line and
// nothing else: keys a participant added there, such as the exclude_* keys
// that keep /tmp out of the sandbox's writable set, stay byte for byte.
// Re-rendering the block from its roots used to drop them silently, and a
// /tmp that had been read-only became writable (live under codex exec).
func TestClosingTheOldBlockKeepsItsOtherKeys(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	block := agentsMarkerBegin + "\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [" + quotedRootsOf(t, cfgPath) + "]\nexclude_slash_tmp = true\nexclude_tmpdir_env_var = true\n" + agentsMarkerEnd + "\n"
	before := "sandbox_mode = \"workspace-write\"\n\n" + block
	m.files[codexConfigPath] = []byte(before)
	code, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	want := strings.Replace(before, "network_access = true\n", "", 1)
	if got := string(m.files[codexConfigPath]); got != want {
		t.Errorf("closing the old block changed more than its network_access line\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !strings.Contains(out, "remove its network_access = true line, and keep every other line of it as it is") {
		t.Errorf("the plan does not say what it removed:\n%s", out)
	}
}

// An old block whose network_access is already false is closed: it is not
// rewritten, and status does not call it open.
func TestAClosedOldBlockIsLeftAndCalledClosed(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	before := "sandbox_mode = \"workspace-write\"\n\n" + agentsMarkerBegin + "\n[sandbox_workspace_write]\nnetwork_access = false\nwritable_roots = [" + quotedRootsOf(t, cfgPath) + "]\n" + agentsMarkerEnd + "\n"
	m.files[codexConfigPath] = []byte(before)
	_, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if got := string(m.files[codexConfigPath]); got != before || strings.Contains(out, "network_access = true") {
		t.Errorf("a closed old block was rewritten:\n%s\n%s", out, got)
	}
	_, status := runAgentsAt(t, ops, "", "status", "-config", cfgPath)
	if strings.Contains(status, "open network to any host") || !strings.Contains(status, "grants the writable roots and no network") {
		t.Errorf("status misreads a closed old block:\n%s", status)
	}
}

// openOldBlock is our old block with network_access = true, alone in the
// file: a state with nothing of the participant's to ask about.
func openOldBlock(t *testing.T, cfgPath string) string {
	t.Helper()
	return "model = \"gpt-5\"\n" + agentsMarkerBegin + "\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [" + quotedRootsOf(t, cfgPath) + "]\n" + agentsMarkerEnd + "\n"
}

// A run that commits nothing else still closes our old open block: with no
// terminal and no -yes, and when Proceed? goes unanswered. The plan's full
// migration is the participant's to accept; its safety form is not.
func TestARunThatWritesNothingStillClosesTheOldBlock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
	}{
		{"no terminal, no -yes", false},
		{"Proceed? unanswered", true},
	} {
		for _, goos := range []string{"linux", "windows"} {
			t.Run(tc.name+"/"+goos, func(t *testing.T) {
				cfgPath, _ := sandboxTestConfig(t)
				onCodexOS(t, goos)
				m, ops := newFakeMachine("codex")
				m.terminal = tc.terminal
				before := openOldBlock(t, cfgPath)
				m.files[codexConfigPath] = []byte(before)
				code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex")
				if code != exitUsage {
					t.Errorf("exit %d, want 2\n%s", code, out)
				}
				if got := string(m.files[codexConfigPath]); got != strings.Replace(before, "network_access = true\n", "", 1) {
					t.Errorf("the old block was not closed, or more changed:\n%s\n%s", out, got)
				}
				if !strings.Contains(out, "only jevlin's own block in Codex's config was closed, and nothing else was changed") {
					t.Errorf("the run does not say what it closed:\n%s", out)
				}
				if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; ok {
					t.Errorf("the skill was written by a run that committed nothing else")
				}
			})
		}
	}
}

// After an unanswered Codex question, the run says what it closed rather
// than that nothing changed.
func TestAnUnansweredQuestionSaysWhatItClosed(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	m.files[codexConfigPath] = []byte("default_permissions = \":workspace\"\n" + strings.TrimPrefix(openOldBlock(t, cfgPath), "model = \"gpt-5\"\n"))
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if code != exitUsage || strings.Contains(out, "nothing was changed") || !strings.Contains(out, "only jevlin's own block in Codex's config was closed") {
		t.Errorf("exit %d; the message does not match what was written:\n%s", code, out)
	}
	if strings.Contains(string(m.files[codexConfigPath]), "network_access") {
		t.Errorf("the old block was not closed:\n%s", m.files[codexConfigPath])
	}
}

// An unanswerable question exits 2 even when nothing else is planned.
func TestAnUnanswerableQuestionExitsTwoWithNothingElseToDo(t *testing.T) {
	for _, args := range [][]string{{"-yes"}, nil} {
		t.Run(strings.Join(args, ""), func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			m, ops := newFakeMachine("codex")
			before := "default_permissions = \":workspace\"\n"
			m.files[codexConfigPath] = []byte(before)
			code, out := runAgentsAt(t, ops, "", append([]string{"install", "-config", cfgPath, "-client", "codex"}, args...)...)
			if code != exitUsage || string(m.files[codexConfigPath]) != before {
				t.Errorf("exit %d, want 2 with nothing written:\n%s", code, out)
			}
		})
	}
}
