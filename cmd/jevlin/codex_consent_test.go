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
//   - no terminal: refused, the lines to add printed, exit 1

import (
	"bytes"

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
			if code != exitTransport {
				t.Fatalf("exit %d, want 1 for a refused host\n%s", code, out)
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

// The question shows what it would add. A participant asked about their own
// profile is told every line, in the form it is written.
func TestTheProfileQuestionListsTheEntries(t *testing.T) {
	st := codexStates()[3] // own-profile-network-absent
	if st.name != "own-profile-network-absent" {
		t.Fatalf("the table moved: %s", st.name)
	}
	_, ops, cfgPath, _ := seedState(t, st)
	_, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex")
	for _, want := range []string{
		`names a permission profile of yours, "work", in default_permissions.`,
		"[permissions.work.filesystem]",
		"[permissions.work.network.domains]",
		`"router.example.invalid" = "allow"`,
		"[features.network_proxy]",
		`Add these entries to your profile "work"? [y/N]: `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the question does not show %q:\n%s", want, out)
		}
	}
}

// A participant profile with its network already open and the proxy off is
// unrestricted by their choice: only the filesystem lines are asked
// for, and the proxy is not turned on, which would cut their network down to
// jevlin's hosts.
func TestAnOpenProfileIsAskedOnlyForItsRoots(t *testing.T) {
	var st codexState
	for _, s := range codexStates() {
		if s.name == "own-profile-network-open" {
			st = s
		}
	}
	_, ops, cfgPath, _ := seedState(t, st)
	_, out := runAgentsAt(t, ops, "n\n", "install", "-config", cfgPath, "-client", "codex")
	if !strings.Contains(out, "[permissions.work.filesystem]") {
		t.Fatalf("the roots were not asked for:\n%s", out)
	}
	for _, unwanted := range []string{"network.domains", "network_proxy", "[permissions.work.network]"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("an open profile was asked for %q:\n%s", unwanted, out)
		}
	}
}
