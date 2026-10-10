package main

// The question install asks before it changes a Codex setting of the
// participant's own, under every way it can end.
//
//   - typed yes: the change is made (TestCodexStartingStatesRenderTheirGoldens)
//   - typed no: nothing for Codex — no config change, no skill, no hooks —
//     and exit 0, because declining is an answer that changes nothing
//   - no line (an interrupt, a closed stdin): the whole command stops, writes
//     nothing for any host, and exits 2 (hard invariant 18)
//   - -yes: answers nothing here; it is asked anyway at a terminal
//   - no terminal: refused, the lines to add printed, and exit 2, as for any
//     question that could not be answered; what needed no answer is written

import (
	"bytes"
	"fmt"

	"strings"
	"testing"
)

// askingStates are the starting states whose install asks.
func askingStates(t *testing.T) []codexState {
	t.Helper()
	var out []codexState
	for _, st := range codexStates() {
		if st.answer != "" {
			out = append(out, st)
		}
	}
	if len(out) < 6 {
		t.Fatalf("only %d asking states; the table lost its cases", len(out))
	}
	return out
}

// seedState writes a state's before onto a fresh fake machine, at a terminal.
func seedState(t *testing.T, st codexState) (m *fakeMachine, ops agentOps, cfgPath, before string) {
	t.Helper()
	cfgPath, _ = sandboxTestConfig(t)
	m, ops = newFakeMachine("codex", "claude")
	m.terminal = true
	before = strings.ReplaceAll(st.before, "STATEROOTS", quotedRootsOf(t, cfgPath))
	m.files[codexConfigPath] = []byte(before)
	return m, ops, cfgPath, before
}

func fakeFilesOf(m *fakeMachine) map[string]string {
	out := map[string]string{}
	for k, v := range m.files {
		out[k] = string(v)
	}
	return out
}

func TestATypedNoInstallsNothingForCodexAndExitsZero(t *testing.T) {
	for _, st := range askingStates(t) {
		t.Run(st.name, func(t *testing.T) {
			m, ops, cfgPath, before := seedState(t, st)
			code, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
			if code != exitOK {
				t.Fatalf("a typed no exited %d, want 0\n%s", code, out)
			}
			if got := string(m.files[codexConfigPath]); got != before {
				t.Errorf("config.toml changed after a typed no:\n%s", got)
			}
			for p := range m.files {
				if strings.HasPrefix(p, "/home/u/.codex/") && p != codexConfigPath {
					t.Errorf("%s was written after a typed no", p)
				}
			}
			if !strings.Contains(out, "nothing installed for Codex") {
				t.Errorf("the plan does not say nothing was installed for Codex:\n%s", out)
			}
		})
	}
}

// An unanswered question stops the whole command: not only Codex, every
// host, because the participant was asked something and did not answer.
func TestAnUnansweredCodexQuestionWritesNothingAndExitsTwo(t *testing.T) {
	for _, st := range askingStates(t) {
		t.Run(st.name, func(t *testing.T) {
			endings(t, func(t *testing.T, stdin func(...string) *interruptReader) {
				m, ops, cfgPath, _ := seedState(t, st)
				before := fakeFilesOf(m)
				var out, errOut bytes.Buffer
				code := agentsMain(ops, []string{"install", "-config", cfgPath, "-yes"}, stdin(), &out, &errOut, envOf(nil))
				if code != exitUsage {
					t.Fatalf("an unanswered question exited %d, want 2\n%s%s", code, out.String(), errOut.String())
				}
				if after := fakeFilesOf(m); len(after) != len(before) {
					t.Errorf("files written for a question nobody answered: %d before, %d after", len(before), len(after))
				} else {
					for k, v := range before {
						if after[k] != v {
							t.Errorf("%s changed for a question nobody answered", k)
						}
					}
				}
				if !strings.Contains(errOut.String(), promptAbortedReason) {
					t.Errorf("stderr does not say the question went unanswered:\n%s", errOut.String())
				}
			})
		})
	}
}

// -yes is not an answer to a question about the participant's own
// settings: at a terminal it is still asked, and its answer still decides.
func TestYesDoesNotAnswerTheCodexQuestion(t *testing.T) {
	for _, st := range askingStates(t) {
		t.Run(st.name, func(t *testing.T) {
			m, ops, cfgPath, before := seedState(t, st)
			var out bytes.Buffer
			code := agentsMain(ops, []string{"install", "-config", cfgPath, "-client", "codex", "-yes"}, strings.NewReader("no\n"), &out, &out, envOf(nil))
			if code != exitOK || string(m.files[codexConfigPath]) != before {
				t.Errorf("-yes overrode a typed no (exit %d):\n%s", code, out.String())
			}
			if !strings.Contains(out.String(), "[y/N]: ") {
				t.Errorf("the question was not asked under -yes:\n%s", out.String())
			}
		})
	}
}

// With no terminal there is nobody to ask: Codex is refused, the lines to
// add by hand are printed, and the rest of the command goes on.
func TestWithNoTerminalTheCodexChangeIsRefusedAndPrinted(t *testing.T) {
	for _, st := range askingStates(t) {
		t.Run(st.name, func(t *testing.T) {
			m, ops, cfgPath, before := seedState(t, st)
			m.terminal = false
			code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
			if code != exitUsage {
				t.Fatalf("exit %d, want 2 for a question with nobody to answer it\n%s", code, out)
			}
			if string(m.files[codexConfigPath]) != before {
				t.Errorf("config.toml changed with nobody to ask:\n%s", m.files[codexConfigPath])
			}
			if !strings.Contains(out, "-yes does not answer it") || !strings.Contains(out, "= \"write\"") {
				t.Errorf("the refusal does not print the lines to add by hand:\n%s", out)
			}
			if _, ok := m.files["/home/u/.claude/skills/jevlin/SKILL.md"]; !ok {
				t.Errorf("another host was not installed beside the refused Codex:\n%s", out)
			}
		})
	}
}

// The question shows everything it would write: the switch, what jevlin's
// profile adds, the participant's lines that change, and the profile itself.
func TestTheSwitchQuestionShowsEverythingItWrites(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"own-profile-network-absent", []string{
			`Switch Codex to jevlin's profile, which extends your profile "work" and adds write access to `,
			`network access to agents-v1.nyks.dev, as.example.invalid and router.example.invalid only`,
			`the [features.network_proxy] table, which makes Codex enforce that host list`,
			`default_permissions = "work" becomes "jevlin"`,
			`extends = "work"`,
			`[permissions.jevlin.network.domains]`,
			`Switch? [y/N]: `,
		}},
		{"default-permissions-workspace", []string{
			`Switch Codex to jevlin's profile, which extends Codex's built-in ":workspace" profile`,
			`default_permissions = ":workspace" becomes "jevlin"`,
			`extends = ":workspace"`,
			`[permissions.jevlin.filesystem]`,
		}},
		{"sandbox-mode-workspace-write", []string{
			`sandbox_mode = "workspace-write" is commented out`,
			`default_permissions = "jevlin"`,
		}},
		{"features-network-proxy-false", []string{
			`network_proxy = false becomes true in your [features] table`,
			`[permissions.jevlin.network.domains]`,
		}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			var st codexState
			for _, s := range codexStates() {
				if s.name == tc.state {
					st = s
				}
			}
			if st.name == "" {
				t.Fatalf("no state %s", tc.state)
			}
			_, ops, cfgPath, _ := seedState(t, st)
			_, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex")
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("the question does not show %q:\n%s", w, out)
				}
			}
		})
	}
}

// A participant profile with its network already open and the proxy off is
// unrestricted by their choice: jevlin's profile extends it with the roots
// only, adds no hosts and does not turn the proxy on, which would cut their
// network down to jevlin's hosts.
func TestAnOpenProfileIsAskedOnlyForItsRoots(t *testing.T) {
	var st codexState
	for _, s := range codexStates() {
		if s.name == "own-profile-network-open" {
			st = s
		}
	}
	_, ops, cfgPath, _ := seedState(t, st)
	_, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex")
	if !strings.Contains(out, "adds write access to ") || !strings.Contains(out, "Your profile's network is already open") {
		t.Fatalf("the question does not offer the roots alone:\n%s", out)
	}
	for _, unwanted := range []string{"network.domains", "[features.network_proxy]", "network access to", "network_proxy = false becomes"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("an open profile was offered %q:\n%s", unwanted, out)
		}
	}
}

// An empty line is not a yes. The question's default is no, written [y/N],
// and Enter alone takes the default.
func TestAnEmptyLineAtTheCodexQuestionIsANo(t *testing.T) {
	for _, st := range askingStates(t) {
		t.Run(st.name, func(t *testing.T) {
			m, ops, cfgPath, before := seedState(t, st)
			code, out := runAgentsAt(t, ops, "\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
			if code != exitOK || string(m.files[codexConfigPath]) != before {
				t.Errorf("Enter alone changed the participant's settings (exit %d):\n%s", code, out)
			}
		})
	}
}

// A sandbox_mode Codex wrote inside our markers is the participant's
// setting like any other, and is asked about on the run that finds it, not
// moved out silently to be asked about on the next.
func TestASandboxModeInsideOurBlockIsAskedAbout(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.terminal = true
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	installed := string(m.files[codexConfigPath])
	i := strings.Index(installed, "default_permissions")
	seeded := installed[:i] + "sandbox_mode = \"workspace-write\"\n" + installed[i:]
	m.files[codexConfigPath] = []byte(seeded)
	code, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-yes")
	if !strings.Contains(out, `sandbox_mode = "workspace-write" is commented out`) {
		t.Fatalf("the sandbox_mode inside our block was not asked about (exit %d):\n%s", code, out)
	}
	if code != exitOK {
		t.Errorf("a typed no exited %d", code)
	}
	if _, out = runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-yes"); !strings.Contains(string(m.files[codexConfigPath]), "# sandbox_mode = \"workspace-write\"  # jevlin agents install") {
		t.Errorf("a typed yes did not comment it out:\n%s\n%s", out, m.files[codexConfigPath])
	}
}

// A dry run shows the question a real run would ask, plans the yes, writes
// nothing, and exits 0, at a terminal or not.
func TestADryRunPrintsTheCodexQuestionAndExitsZero(t *testing.T) {
	for _, st := range askingStates(t) {
		for _, terminal := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/terminal=%v", st.name, terminal), func(t *testing.T) {
				m, ops, cfgPath, _ := seedState(t, st)
				m.terminal = terminal
				before := fakeFilesOf(m)
				code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-dry-run")
				if code != exitOK {
					t.Errorf("a dry run exited %d\n%s", code, out)
				}
				if !strings.Contains(out, "Switch? [y/N]: (dry run: not asked; the plan below is what a yes would write)") || !strings.Contains(out, "write  ~/.codex/config.toml") {
					t.Errorf("the dry run does not show the question and the yes it plans:\n%s", out)
				}
				if after := fakeFilesOf(m); fmt.Sprint(after) != fmt.Sprint(before) {
					t.Errorf("a dry run wrote files")
				}
			})
		}
	}
}
