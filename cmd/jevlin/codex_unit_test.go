package main

// Our region and the participant's lines it implies are one unit, and our
// own network is never left open.

import (
	"fmt"
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
	if code != exitTransport || !strings.Contains(out, "refused: Codex: left the jevlin block") || !strings.Contains(out, codexStillUnder) {
		t.Errorf("an uninstall that left our block exited %d, or did not say what it left:\n%s", code, out)
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
	if code != exitTransport || !strings.Contains(out, codexStillUnder) {
		t.Errorf("an uninstall that left our block exited %d, or did not say what it left:\n%s", code, out)
	}
}

// The by-hand text printed with no terminal can be followed as printed —
// every line copied from its first character, as the text says — from each
// starting state that reaches it: a file with no block of ours, an
// installed file that has since gained a line needing a yes, and the old
// marked [sandbox_workspace_write] block. Followed literally, it gives the
// file a yes at a terminal writes, byte for byte, so the result is this
// installation's: a later install at a terminal asks nothing and writes
// the skill, and uninstall takes it out.
func TestTheByHandTextCanBeFollowed(t *testing.T) {
	oldBlock := ""
	for _, st := range codexStates() {
		if st.name == "old-block" {
			oldBlock = st.before
		}
	}
	for _, tc := range []struct {
		name  string
		start func(t *testing.T, m *fakeMachine, ops agentOps, cfgPath string) string
		instr string // the instruction that handles our block
		bytes bool   // following it gives the very bytes a yes writes
	}{
		{"no block", func(t *testing.T, m *fakeMachine, ops agentOps, cfgPath string) string {
			return "default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n\n[tui]\nx = 1\n"
		}, "and add these lines", true},
		{"an installed file that gained a line", func(t *testing.T, m *fakeMachine, ops agentOps, cfgPath string) string {
			m.files[codexConfigPath] = []byte("model = \"gpt-5\"\n\n[tui]\nx = 1\n")
			if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
				t.Fatalf("first install: exit %d\n%s", code, out)
			}
			return "sandbox_mode = \"workspace-write\"\n" + string(m.files[codexConfigPath])
		}, "and replace jevlin's block", true},
		{"a block that lost its end marker", func(t *testing.T, m *fakeMachine, ops agentOps, cfgPath string) string {
			// The 0.158.0 capture of `codex mcp remove`, in this
			// installation's home, with a line that needs a yes.
			home := filepath.Dir(cfgPath)
			text := homePlaceholder.ReplaceAllStringFunc(codexConfigFixture(t, "mcp-after-block-after-mcp-remove.toml"), func(q string) string {
				return mustTOMLString(filepath.Join(home, homePlaceholder.FindStringSubmatch(q)[1]))
			})
			return "sandbox_mode = \"workspace-write\"\n" + text
		}, "and delete jevlin's block", false},
		{"the old block", func(t *testing.T, m *fakeMachine, ops agentOps, cfgPath string) string {
			return "sandbox_mode = \"workspace-write\"\n" + strings.ReplaceAll(oldBlock, "STATEROOTS", quotedRootsOf(t, cfgPath))
		}, "and delete jevlin's block", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			m, ops := newFakeMachine("codex")
			before := tc.start(t, m, ops, cfgPath)

			// What a yes at a terminal writes, for comparison.
			m.files[codexConfigPath] = []byte(before)
			m.terminal = true
			if code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
				t.Fatalf("install with a yes: exit %d\n%s", code, out)
			}
			yes := string(m.files[codexConfigPath])

			m.files[codexConfigPath] = []byte(before)
			m.terminal = false
			_, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
			const lead = "By hand, with every line below copied exactly as printed, from its first character:\n"
			i := strings.Index(out, lead)
			if i < 0 {
				t.Fatalf("no by-hand text:\n%s", out)
			}
			text := out[i+len(lead):]
			// The text ends where the plan's own indented lines resume.
			var lines []string
			for _, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, "  ") || l == "nothing to do" || strings.HasPrefix(l, "wrote ") {
					break
				}
				lines = append(lines, l)
			}
			for len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			text = strings.Join(lines, "\n")
			if !strings.Contains(text, "\n"+tc.instr) {
				t.Errorf("the text does not say %q:\n%s", tc.instr, text)
			}
			// Followed from the file as this run left it: a run with no
			// terminal still closes our old block's open network.
			followed := followByHand(t, string(m.files[codexConfigPath]), strings.Split(text, "\n"))
			if err := codexTOMLError(followed); err != nil {
				t.Fatalf("following the by-hand text gives a file Codex refuses: %v\n%s", err, followed)
			}
			if tc.bytes && followed != yes {
				t.Fatalf("following the by-hand text does not give the file a yes writes\n got %q\nwant %q", followed, yes)
			}
			// Deleting the old block line for line leaves its blank lines
			// where they were, which install tidies; what the file says is
			// the same either way.
			if !tomlDocsEqual(mustDecode(followed), mustDecode(yes)) {
				t.Fatalf("following the by-hand text does not give the settings a yes writes\n got %q\nwant %q", followed, yes)
			}

			m.files[codexConfigPath] = []byte(followed)
			m.terminal = true
			if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK || strings.Contains(out, "[y/N]") {
				t.Fatalf("install over the file finished by hand did not take it as ours (exit %d):\n%s", code, out)
			}
			if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; !ok {
				t.Errorf("the skill was not written over the file finished by hand")
			}
			if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK || strings.Contains(string(m.files[codexConfigPath]), agentsMarkerBegin) {
				t.Fatalf("uninstall did not take the block out (exit %d):\n%s\n%s", code, out, m.files[codexConfigPath])
			}
		})
	}
}

// followByHand does what the by-hand text says, reading each line as
// printed: the instruction lines are sentences, and every other line is
// file content, copied from its first character.
func followByHand(t *testing.T, file string, text []string) string {
	t.Helper()
	instr := func(l string) bool {
		for _, p := range []string{"replace the line", "with", "and add these lines", "and replace jevlin's block", "and delete jevlin's block", "then add these lines", "then put back these lines"} {
			if strings.HasPrefix(l, p) {
				return true
			}
		}
		return false
	}
	content := func(k int) (string, int) {
		var c []string
		for k < len(text) && !instr(text[k]) {
			c = append(c, text[k])
			k++
		}
		return strings.Join(c, "\n") + "\n", k
	}
	// "before your first table, above any comment lines directly over it"
	firstTable := func(s string) int {
		lines := strings.SplitAfter(s, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "[") {
				for i > 0 && strings.HasPrefix(lines[i-1], "#") {
					i--
				}
				return len(strings.Join(lines[:i], ""))
			}
		}
		return len(s)
	}
	for k := 0; k < len(text); {
		l := text[k]
		switch {
		case l == "replace the line":
			old, next := content(k + 1)
			if next >= len(text) || text[next] != "with" {
				t.Fatalf("a replace without its with: %q", text[k:])
			}
			nw, after := content(next + 1)
			if strings.Count(file, old) != 1 {
				t.Fatalf("the line to replace is not once in the file: %q\n%s", old, file)
			}
			file = strings.Replace(file, old, nw, 1)
			k = after
		case strings.HasPrefix(l, "and replace jevlin's block"):
			block, next := content(k + 1)
			from := strings.Index(file, agentsMarkerBegin+"\n")
			to := strings.Index(file, agentsMarkerEnd+"\n")
			if from < 0 || to < from {
				t.Fatalf("no block to replace:\n%s", file)
			}
			file = file[:from] + block + file[to+len(agentsMarkerEnd)+1:]
			k = next
		case strings.HasPrefix(l, "and delete jevlin's block"):
			old, next := content(k + 1)
			if strings.Count(file, old) != 1 {
				t.Fatalf("the block to delete is not once in the file: %q\n%s", old, file)
			}
			file = strings.Replace(file, old, "", 1)
			k = next
		case strings.HasPrefix(l, "and add these lines"), strings.HasPrefix(l, "then add these lines"):
			block, next := content(k + 1)
			at := firstTable(file)
			file = file[:at] + block + file[at:]
			k = next
		case strings.HasPrefix(l, "then put back these lines"):
			lines, next := content(k + 1)
			switch {
			case strings.HasSuffix(l, "directly above the new one:"):
				at := strings.Index(file, agentsMarkerBegin+"\n")
				file = file[:at] + lines + file[at:]
			case strings.HasSuffix(l, "directly below the new one:"):
				at := strings.Index(file, agentsMarkerEnd+"\n") + len(agentsMarkerEnd) + 1
				file = file[:at] + lines + file[at:]
			default:
				t.Fatalf("a put-back that does not say where: %q", l)
			}
			k = next
		default:
			t.Fatalf("an instruction the follower does not know: %q", l)
		}
	}
	return file
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

// A line uninstall leaves because the participant changed it loses our
// mark, so it is theirs: a later switch asks about it and goes through,
// rather than refusing it as a line jevlin already marked.
func TestALineLeftByUninstallLosesOurMark(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "own-profile-network-absent")
	got := string(m.files[codexConfigPath])
	edited := strings.Replace(got, "default_permissions = \"jevlin\"  # jevlin agents install", "default_permissions = \"work\"  # jevlin agents install", 1)
	if edited == got {
		t.Fatalf("this case needs our marked line:\n%s", got)
	}
	m.files[codexConfigPath] = []byte(edited)
	if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	left := string(m.files[codexConfigPath])
	if strings.Contains(left, "jevlin agents install") || !strings.Contains(left, "default_permissions = \"work\"\n") {
		t.Fatalf("the line kept our mark, or lost its value:\n%s", left)
	}
	if code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK || strings.Contains(out, "already carries a jevlin mark") {
		t.Errorf("a later switch was refused (exit %d):\n%s", code, out)
	}
}

// Codex refuses a file whose jevlin profile extends a profile that no
// longer exists (live on 0.158.0: "permissions profile `jevlin` extends
// undefined profile"). Status says so, and install says which profile and
// writes nothing for Codex.
func TestAnExtendedProfileThatIsGoneIsNamed(t *testing.T) {
	m, ops, cfgPath, _ := installedOn(t, "own-profile-network-absent")
	got := string(m.files[codexConfigPath])
	i := strings.Index(got, "[permissions.work]")
	if i < 0 {
		t.Fatalf("this case needs the participant's profile:\n%s", got)
	}
	gone := got[:i]
	m.files[codexConfigPath] = []byte(gone)
	if _, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(out, `extends "work", which no [permissions] table defines; Codex refuses this file and does not start`) {
		t.Errorf("status does not name the missing profile:\n%s", out)
	}
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code == exitOK || !strings.Contains(out, `jevlin's profile in /home/u/.codex/config.toml extends your profile "work", which no [permissions] table defines any more`) {
		t.Errorf("install does not say which profile is gone (exit %d):\n%s", code, out)
	}
	if string(m.files[codexConfigPath]) != gone {
		t.Errorf("install changed a file it cannot complete:\n%s", m.files[codexConfigPath])
	}
}

// The safety write closes our old block whatever its line endings, on
// every path that makes one, on both planners: an unanswered or declined
// Codex question (where a question is asked), no terminal without -yes,
// and an unanswered Proceed?. The one line goes with its own "\r\n".
func TestTheOldBlockIsClosedUnderEitherLineEnding(t *testing.T) {
	type path struct {
		name     string
		terminal bool
		stdin    string
		args     []string
		ask      bool // needs a question of the participant's to ask
	}
	paths := []path{
		{"typed no at the Codex question", true, "n\n", []string{"-yes"}, true},
		{"unanswered Codex question", true, "", []string{"-yes"}, true},
		{"no terminal, no -yes", false, "", nil, false},
		{"Proceed? unanswered", true, "", nil, false},
		{"Proceed? typed no", true, "n\n", nil, false},
	}
	for _, eol := range []string{"\n", "\r\n"} {
		for _, goos := range []string{"linux", "windows"} {
			for _, pt := range paths {
				if pt.ask && goos == "windows" {
					continue // Windows asks no question about the participant's settings
				}
				t.Run(fmt.Sprintf("%q/%s/%s", eol, goos, pt.name), func(t *testing.T) {
					cfgPath, _ := sandboxTestConfig(t)
					onCodexOS(t, goos)
					m, ops := newFakeMachine("codex")
					m.terminal = pt.terminal
					head := "model = \"gpt-5\"\n"
					if pt.ask {
						head = "default_permissions = \":workspace\"\n"
					}
					block := head + agentsMarkerBegin + "\n[sandbox_workspace_write]\nnetwork_access = true\nwritable_roots = [" + quotedRootsOf(t, cfgPath) + "]\nexclude_slash_tmp = true\nexclude_tmpdir_env_var = true\n" + agentsMarkerEnd + "\n"
					before := strings.ReplaceAll(block, "\n", eol)
					m.files[codexConfigPath] = []byte(before)
					_, out := runAgentsAt(t, ops, pt.stdin, append([]string{"install", "-config", cfgPath, "-client", "codex"}, pt.args...)...)
					want := strings.Replace(before, "network_access = true"+eol, "", 1)
					if got := string(m.files[codexConfigPath]); got != want {
						t.Errorf("the old block was not closed by its one line\n--- got ---\n%q\n--- want ---\n%q\n%s", got, want, out)
					}
				})
			}
		}
	}
}

// A typed no at Proceed? declines every write but the close, says so, and
// exits 0 — still apart from no answer, which exits 2.
func TestATypedNoAtProceedClosesTheOldBlockAndSaysSo(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	before := openOldBlock(t, cfgPath)
	m.files[codexConfigPath] = []byte(before)
	code, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex")
	if code != exitOK || !strings.Contains(out, "left everything else as it was; only jevlin's own block in Codex's config was closed") {
		t.Errorf("exit %d; a typed no did not close and say so:\n%s", code, out)
	}
	if got := string(m.files[codexConfigPath]); got != strings.Replace(before, "network_access = true\n", "", 1) {
		t.Errorf("a typed no left the old block open:\n%s", got)
	}
}

// Installing into a CRLF file writes our block in CRLF too, on either
// planner, so the file is not left with mixed line endings; a second
// install writes nothing, and uninstall gives the file back byte for byte.
// A block an earlier build wrote in LF into a CRLF file is rewritten once
// in CRLF and then left — read from the participant's lines, not the
// file's first, which is our own begin marker when the file starts with a
// table.
func TestABlockIsWrittenInTheFilesLineEnding(t *testing.T) {
	for _, tc := range []struct{ goos, name, before string }{
		{"linux", "root keys first", "model = \"gpt-5\"\r\n\r\n[tui]\r\nx = 1\r\n"},
		{"linux", "a table first", "[tui]\r\nx = 1\r\n"},
		{"windows", "root keys first", "model = \"gpt-5\"\r\n\r\n[tui]\r\nx = 1\r\n"},
		// No table and no final line ending: the block goes at the end,
		// and the line before it is ended in CRLF, the block's own ending.
		{"linux", "no table and no final line ending", "model = \"gpt-5\"\r\nx = 1"},
		{"windows", "no table and no final line ending", "model = \"gpt-5\"\r\nx = 1"},
	} {
		t.Run(tc.goos+"/"+tc.name, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			onCodexOS(t, tc.goos)
			m, ops := newFakeMachine("codex")
			before := tc.before
			m.files[codexConfigPath] = []byte(before)
			install := func() string {
				t.Helper()
				if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
					t.Fatalf("install: exit %d\n%s", code, out)
				}
				return string(m.files[codexConfigPath])
			}
			lfOnly := func(s string) int { return strings.Count(s, "\n") - strings.Count(s, "\r\n") }
			got := install()
			if !strings.Contains(got, agentsMarkerBegin+"\r\n") || lfOnly(got) != 0 {
				t.Fatalf("the block is not in the file's CRLF (%d LF-only lines):\n%q", lfOnly(got), got)
			}
			if again := install(); again != got {
				t.Errorf("a second install changed the file:\n%q", again)
			}

			// An LF block in a CRLF file, as an earlier build wrote it.
			m.files[codexConfigPath] = []byte(strings.Replace(got, got[strings.Index(got, agentsMarkerBegin):strings.Index(got, agentsMarkerEnd)+len(agentsMarkerEnd)+2],
				strings.ReplaceAll(got[strings.Index(got, agentsMarkerBegin):strings.Index(got, agentsMarkerEnd)+len(agentsMarkerEnd)+2], "\r\n", "\n"), 1))
			if lfOnly(string(m.files[codexConfigPath])) == 0 {
				t.Fatal("the mixed file was not made")
			}
			if fixed := install(); fixed != got {
				t.Errorf("the LF block was not rewritten in CRLF:\n%q", fixed)
			}
			if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, out)
			}
			// The one byte sequence install may add is the line ending a
			// last line without one needed; uninstall cannot know it added it.
			want := before
			if !strings.HasSuffix(want, "\n") {
				want += "\r\n"
			}
			if left := string(m.files[codexConfigPath]); left != want {
				t.Errorf("uninstall did not give the file back\n got %q\nwant %q", left, want)
			}
		})
	}
}

// Codex's app server writes sandbox_mode inside our markers, under our
// default_permissions line (captured on 0.158.0). The by-hand text deletes
// the block and puts that line back above the new one already commented
// out and marked, as a yes writes it; it said to edit the line, then
// deleted it with the block, then put it back unchanged, so a participant
// who followed it was told the same thing on every run. Followed
// literally, the next install at a terminal asks nothing.
func TestTheByHandTextPutsBackTheLineAsChanged(t *testing.T) {
	m, ops, cfgPath := capturedWithHome(t, "appserver-sandbox-mode-inside-block.toml")
	before := string(m.files[codexConfigPath])
	if i := strings.Index(before, "sandbox_mode = \"workspace-write\"\n"); i < strings.Index(before, agentsMarkerBegin) {
		t.Fatalf("the capture is not the shape it is named for:\n%s", before)
	}
	_, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
	const lead = "By hand, with every line below copied exactly as printed, from its first character:\n"
	i := strings.Index(out, lead)
	if i < 0 {
		t.Fatalf("no by-hand text:\n%s", out)
	}
	var lines []string
	for _, l := range strings.Split(out[i+len(lead):], "\n") {
		if strings.HasPrefix(l, "  ") || l == "nothing to do" || strings.HasPrefix(l, "wrote ") {
			break
		}
		lines = append(lines, l)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	followed := followByHand(t, string(m.files[codexConfigPath]), lines)
	if !strings.Contains(followed, "# sandbox_mode = \"workspace-write\"  # jevlin agents install (") || strings.Contains(followed, "\nsandbox_mode = ") {
		t.Fatalf("following the text left sandbox_mode active:\n%s", followed)
	}
	m.files[codexConfigPath] = []byte(followed)
	m.terminal = true
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK || strings.Contains(out, "[y/N]") {
		t.Errorf("the next install still asks (exit %d):\n%s", code, out)
	}
}
