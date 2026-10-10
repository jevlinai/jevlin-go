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

	"os"
	"slices"
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

// A [features.network_proxy] table without `enabled` says nothing about
// whether Codex enforces a host list, so install refuses with a sentence
// that names it, rather than leaving the strict parser check to refuse
// the duplicate table that writing ours beside it would make.
func TestAProxyTableWithoutEnabledIsNamed(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	before := "[features.network_proxy]\nallow_local_binding = true\n"
	m.files[codexConfigPath] = []byte(before)
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code == exitOK || string(m.files[codexConfigPath]) != before {
		t.Errorf("install wrote over a proxy table it cannot read (exit %d):\n%s", code, out)
	}
	if !strings.Contains(out, "is a table without enabled = true or false") {
		t.Errorf("the refusal does not say what is wrong with the table:\n%s", out)
	}
}

// A yes to the switch turns on the extended chain's own domains as well as
// ours (live on 0.158.0), so the question, the plan and status name every
// host the merged profile allows, and say so plainly when that is every
// host.
func TestTheSwitchNamesEveryHostTheMergedProfileAllows(t *testing.T) {
	for _, tc := range []struct{ state, network, chain, proxy, status string }{
		{"own-profile-chain-allows-a-host", "agents-v1.nyks.dev, as.example.invalid, example.com and router.example.invalid only",
			`That list includes what your profile "work" allows`, "which makes Codex enforce that host list",
			"; hosts agents-v1.nyks.dev, as.example.invalid, example.com, router.example.invalid; network_proxy on"},
		// With "*" there is no host list: the question and status say the
		// network is open and where that comes from, not "that host list".
		{"own-profile-chain-allows-every-host", `every host, because your profile "work" allows "*"`,
			`That is your profile "work"'s "*"`, "through which Codex applies the profile's domain rules",
			`; network open to every host, because your profile "work" allows "*"; network_proxy on`},
	} {
		t.Run(tc.state, func(t *testing.T) {
			var st codexState
			for _, s := range codexStates() {
				if s.name == tc.state {
					st = s
				}
			}
			m, ops, cfgPath, _ := seedState(t, st)
			code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
			if code != exitOK {
				t.Fatalf("install: exit %d\n%s", code, out)
			}
			if !strings.Contains(out, "network access to "+tc.network) {
				t.Errorf("the question does not name every host the switch allows:\n%s", out)
			}
			if !strings.Contains(out, "and network to "+tc.network) {
				t.Errorf("the plan line does not name every host the switch allows:\n%s", out)
			}
			if !strings.Contains(out, tc.chain) {
				t.Errorf("the question does not say where the other hosts come from (want %q):\n%s", tc.chain, out)
			}
			if !strings.Contains(out, "[features.network_proxy] table, "+tc.proxy+"?") {
				t.Errorf("the question does not say what the proxy table does here (want %q):\n%s", tc.proxy, out)
			}
			_, status := runAgentsAt(t, ops, "", "status", "-config", cfgPath)
			if !strings.Contains(status, tc.status) {
				t.Errorf("status does not say what the profile allows (want %q):\n%s", tc.status, status)
			}
			_ = m
		})
	}
}

// Codex resolves each domain key by its nearest definition, ours first and
// then up the extends chain, and "*" the same way on its own. Each row is a
// verdict `codex sandbox` gave on 0.158.0 for jevlin's profile extending
// "work", which extends "base": a host it reached is open, one it refused
// is closed. Treating a deny anywhere in the chain as final, as this code
// once did, called rows a, c and f's example.com closed and named a
// shorter list than Codex enforces.
func TestTheMergedProfileResolvesEachHostByItsNearestDefinition(t *testing.T) {
	const ours = "router.example.invalid"
	for _, tc := range []struct {
		name, work, base string
		ours             []string
		open, closed     []string
	}{
		{"a: a parent's deny under a child's allow", `"example.com" = "allow"`, `"example.com" = "deny"`, nil, []string{"example.com"}, nil},
		{"b: a parent's allow under a child's deny", `"example.com" = "deny"`, `"example.com" = "allow"`, nil, nil, []string{"example.com"}},
		{"c: our allow over the chain's deny", `"example.com" = "deny"`, ``, []string{"example.com"}, []string{"example.com"}, nil},
		{"d: a parent's * under a child's deny", `"example.com" = "deny"`, `"*" = "allow"`, nil, []string{"iana.org"}, []string{"example.com"}},
		{"e: a child's * over a parent's deny", `"*" = "allow"`, `"example.com" = "deny"`, nil, []string{"iana.org"}, []string{"example.com"}},
		{"f: ours, the child's and the parent's deny", `"iana.org" = "allow"`, `"example.com" = "deny"`, []string{"example.com"}, []string{"example.com", "iana.org"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustDecode("[permissions.work]\nextends = \"base\"\n[permissions.work.network.domains]\n" + tc.work +
				"\n[permissions.base]\nextends = \":workspace\"\n[permissions.base.network.domains]\n" + tc.base + "\n")
			a := codexAllowedFor(doc, "work", append([]string{ours}, tc.ours...))
			reachable := func(h string) bool {
				return slices.Contains(a.hosts, h) || (a.every && !slices.Contains(a.except, h))
			}
			for _, h := range append([]string{ours}, tc.open...) {
				if !reachable(h) {
					t.Errorf("%s reads closed; Codex let it through (hosts %v, every %v, except %v)", h, a.hosts, a.every, a.except)
				}
			}
			for _, h := range tc.closed {
				if reachable(h) {
					t.Errorf("%s reads open; Codex refused it (hosts %v, every %v, except %v)", h, a.hosts, a.every, a.except)
				}
			}
			if s := a.sentence(); a.every && !strings.Contains(s, "every host but example.com") {
				t.Errorf("the sentence does not name the host the chain still denies: %s", s)
			}
		})
	}
}

// The by-hand text says default_permissions must come before any table
// only when the block it prints carries that key: from ":workspace" or a
// profile of the participant's, the key stays on their own line, and the
// note would be about a line the block does not have.
func TestTheByHandKeyNoteOnlyWhereTheBlockCarriesTheKey(t *testing.T) {
	const note = "(default_permissions must come before any table)"
	for _, tc := range []struct {
		before string
		note   bool
	}{
		{"default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n", false},
		{workProfile + "\n[features]\nnetwork_proxy = false\n", false},
		{"sandbox_mode = \"workspace-write\"\nmodel = \"gpt-5\"\n", true},
	} {
		cfgPath, _ := sandboxTestConfig(t)
		m, ops := newFakeMachine("codex")
		m.files[codexConfigPath] = []byte(tc.before)
		_, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
		if !strings.Contains(out, "By hand") {
			t.Fatalf("no by-hand text over %q:\n%s", tc.before, out)
		}
		if strings.Contains(out, note) != tc.note {
			t.Errorf("over %q the key note is there: %v, want %v:\n%s", tc.before, !tc.note, tc.note, out)
		}
	}
}

// Codex applies a wildcard deny the chain resolves to over any exact
// allow, at any level, jevlin's own included: "*.d" closes every subdomain
// of d at any depth, "**.d" those and d itself, case aside. Each row is a
// verdict `codex sandbox` gave on 0.158.0 with jevlin's profile allowing
// the hosts exactly and extending "mine".
func TestAWildcardDenyClosesWhatAnExactAllowOpens(t *testing.T) {
	for _, tc := range []struct {
		name, mine   string
		ours         []string
		open, closed []string
	}{
		{"a parent's *. deny over our allow", `"*.example.com" = "deny"`, []string{"www.example.com", "example.com"}, []string{"example.com"}, []string{"www.example.com"}},
		{"a parent's exact deny under our allow", `"www.example.com" = "deny"`, []string{"www.example.com", "example.com"}, []string{"www.example.com", "example.com"}, nil},
		{"*. deny beside an exact allow in one profile", "\"*.example.com\" = \"deny\"\n\"www.example.com\" = \"allow\"", []string{"www.example.com", "example.com"}, []string{"example.com"}, []string{"www.example.com"}},
		{"a parent's **. deny", `"**.example.com" = "deny"`, []string{"www.example.com", "example.com"}, nil, []string{"www.example.com", "example.com"}},
		{"*. reaches every depth, case aside", `"*.AmazonAWS.com" = "deny"`, []string{"s3.amazonaws.com", "s3.us-east-1.amazonaws.com"}, nil, []string{"s3.amazonaws.com", "s3.us-east-1.amazonaws.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustDecode("[permissions.mine]\nextends = \":workspace\"\n[permissions.mine.network.domains]\n" + tc.mine + "\n")
			a := codexAllowedFor(doc, "mine", tc.ours)
			for _, h := range tc.open {
				if !slices.Contains(a.hosts, h) {
					t.Errorf("%s reads closed; Codex let it through (hosts %v, blocked %v)", h, a.hosts, a.blocked)
				}
			}
			for _, h := range tc.closed {
				if slices.Contains(a.hosts, h) || !strings.Contains(a.blockedSentence(), h+", which your profile \"mine\" denies") {
					t.Errorf("%s is not named as closed; Codex refused it (hosts %v, blocked %v)", h, a.hosts, a.blocked)
				}
			}
		})
	}
}

// The participant's wildcard deny over the router's host: no yes could
// make a search from Codex work, so install refuses for Codex before
// asking and writes nothing for it. Over another of the search hosts it
// asks as before, and the question and status name the host Codex blocks.
func TestAWildcardDenyOfTheSearchHosts(t *testing.T) {
	mine := func(deny string) string {
		return "default_permissions = \"mine\"\n\n[permissions.mine]\nextends = \":workspace\"\n\n[permissions.mine.network]\nenabled = false\n\n[permissions.mine.network.domains]\n" + deny + "\n\"example.com\" = \"allow\"\n"
	}
	t.Run("the router", func(t *testing.T) {
		cfgPath, _ := sandboxTestConfig(t)
		m, ops := newFakeMachine("codex")
		m.terminal = true
		before := mine(`"*.example.invalid" = "deny"`)
		m.files[codexConfigPath] = []byte(before)
		code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
		if code == exitOK || strings.Contains(out, "[y/N]") || string(m.files[codexConfigPath]) != before {
			t.Errorf("install did not refuse before asking (exit %d):\n%s", code, out)
		}
		if !strings.Contains(out, `denies "*.example.invalid", which Codex applies over jevlin's allow of the router, router.example.invalid`) {
			t.Errorf("the refusal does not name the deny and the router:\n%s", out)
		}
		if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; ok {
			t.Errorf("the skill was written for a Codex whose search cannot reach the router")
		}
	})
	t.Run("another search host", func(t *testing.T) {
		cfgPath, _ := sandboxTestConfig(t)
		m, ops := newFakeMachine("codex")
		m.terminal = true
		m.files[codexConfigPath] = []byte(mine(`"**.as.example.invalid" = "deny"`))
		code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
		const named = `Codex blocks as.example.invalid, which your profile "mine" denies with "**.as.example.invalid"`
		if code != exitOK || !strings.Contains(out, named) || strings.Contains(out, "network access to agents-v1.nyks.dev, as.example.invalid") {
			t.Errorf("the question does not take the blocked host out of the list and name it (exit %d):\n%s", code, out)
		}
		if _, status := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(status, named) || strings.Contains(status, "the router is among them") {
			t.Errorf("status does not name the blocked host:\n%s", status)
		}
	})
}

// Every deny key holding "*" or "?" is a glob to Codex, and case and one
// trailing dot are ignored on both sides. Each row is a verdict `codex
// sandbox` gave on 0.158.0 with jevlin's profile allowing www.example.com
// and example.com exactly and extending "mine", which denies the pattern.
func TestEveryDenyPatternCodexAppliesIsSeen(t *testing.T) {
	for _, tc := range []struct {
		pattern               string
		wwwClosed, apexClosed bool
	}{
		{"*example.com", true, true},
		{"www.example.*", true, false},
		{"*.example.com.", true, false},
		{"**example.com", true, true},
		{"w*.example.com", true, false},
		{"*.example.co?", true, false},
		{"?ww.example.com", true, false},
		{"*.example.com", true, false},
		{"**.example.com", true, true},
		{"*.EXAMPLE.COM", true, false},
		{"**.com", true, true},
		{"**.www.example.com", true, false},
		{".example.com", false, false},
		{"*.www.example.com", false, false},
		{"WWW.EXAMPLE.COM.", false, false},
	} {
		doc := mustDecode("[permissions.mine]\nextends = \":workspace\"\n[permissions.mine.network.domains]\n" + mustTOMLString(tc.pattern) + " = \"deny\"\n")
		a := codexAllowedFor(doc, "mine", []string{"www.example.com", "example.com"})
		for host, closed := range map[string]bool{"www.example.com": tc.wwwClosed, "example.com": tc.apexClosed} {
			if slices.Contains(a.hosts, host) == closed {
				t.Errorf("%q: %s reads closed = %v; Codex said %v (hosts %v)", tc.pattern, host, !closed, closed, a.hosts)
			}
		}
	}
}

// The router's host is compared as Codex compares it: a router_url
// spelled with capitals and a trailing dot is still closed by a deny of
// "*.example.invalid".
func TestTheRouterHostIsComparedAsCodexComparesIt(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	spelled := strings.Replace(string(b), `router_url = "https://router.example.invalid"`, `router_url = "https://Router.Example.Invalid.:8443/v1"`, 1)
	if spelled == string(b) {
		t.Fatal("the test config has no router_url to respell")
	}
	if err := os.WriteFile(cfgPath, []byte(spelled), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ops := newFakeMachine("codex")
	m.terminal = true
	before := "default_permissions = \"mine\"\n\n[permissions.mine]\nextends = \":workspace\"\n\n[permissions.mine.network.domains]\n\"*.example.invalid\" = \"deny\"\n"
	m.files[codexConfigPath] = []byte(before)
	code, out := runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if code == exitOK || strings.Contains(out, "[y/N]") || string(m.files[codexConfigPath]) != before || !strings.Contains(out, "so a search from Codex could never reach it") {
		t.Errorf("a deny of the router's host, spelled otherwise, was not refused (exit %d):\n%s", code, out)
	}
}
