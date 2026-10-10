package main

// What Codex does to the region when it edits features, captured from
// codex-cli 0.158.0 (testdata/codex/*-region-*.toml, *-proxy-last-*.toml):
// install, status and uninstall over each.
//
// The *-region-* files are the layout this client wrote before its proxy
// table moved last: Codex took the begin marker with the table. The
// *-proxy-last-* files are the layout it writes now: the markers stay, and
// only the table goes.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var homePlaceholder = regexp.MustCompile(`"<HOME>/([^"]*)"`)

// capturedWithHome loads a capture with this test's own jevlin home in
// place of <HOME>, beside a config whose directories and hosts are the ones
// the capture's region was rendered from.
func capturedWithHome(t *testing.T, name string) (m *fakeMachine, ops agentOps, cfgPath string) {
	t.Helper()
	cfgPath, home := sandboxTestConfig(t)
	text := homePlaceholder.ReplaceAllStringFunc(codexConfigFixture(t, name), func(q string) string {
		return mustTOMLString(filepath.Join(home, homePlaceholder.FindStringSubmatch(q)[1]))
	})
	m, ops = newFakeMachine("codex")
	m.files[codexConfigPath] = []byte(text)
	return m, ops, cfgPath
}

var capturedDefaults = map[string]string{"workspace": ":workspace", "own-profile": "work"}

// A lone end marker with our tables above it is our region. Status says it
// is damaged; install repairs it and puts the proxy table back; a second
// install writes nothing; uninstall from the damaged file removes it whole
// and puts the participant's default_permissions back.
func TestARegionThatLostItsBeginMarkerIsStillOurs(t *testing.T) {
	for short, dp := range capturedDefaults {
		t.Run(short, func(t *testing.T) {
			name := short + "-region-features-disable-network_proxy.toml"
			m, ops, cfgPath := capturedWithHome(t, name)
			damaged := string(m.files[codexConfigPath])
			if strings.Contains(damaged, agentsMarkerBegin) || !strings.Contains(damaged, agentsMarkerEnd) {
				t.Fatalf("the capture is not the damage it is named for:\n%s", damaged)
			}
			if _, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(out, "has lost one of its markers") {
				t.Errorf("status does not name the damage:\n%s", out)
			}

			code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
			got := string(m.files[codexConfigPath])
			if code != exitOK || !strings.Contains(out, "repair jevlin's block") {
				t.Fatalf("install did not repair the block (exit %d):\n%s", code, out)
			}
			if strings.Count(got, agentsMarkerBegin) != 1 || strings.Count(got, agentsMarkerEnd) != 1 {
				t.Errorf("the repaired file does not hold one block:\n%s", got)
			}
			if _, _, proxy, _ := profileOf(t, got); !proxy {
				t.Errorf("the proxy table was not put back:\n%s", got)
			}
			assertNotOpenByUs(t, "after the repair", got)
			if _, again := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); string(m.files[codexConfigPath]) != got {
				t.Errorf("a second install changed the repaired file:\n%s", again)
			}

			m.files[codexConfigPath] = []byte(damaged)
			if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, out)
			}
			left := string(m.files[codexConfigPath])
			if strings.Contains(left, "[permissions.jevlin") || strings.Contains(left, agentsMarkerEnd) {
				t.Errorf("uninstall left part of the damaged block:\n%s", left)
			}
			if doc, ok := decodeTOMLDoc(left); !ok || doc["default_permissions"] != dp {
				t.Errorf("default_permissions was not put back to %q:\n%s", dp, left)
			}
		})
	}
}

// With the proxy table last, Codex's `features disable` takes only the
// table: both markers stay, status says the network is open, and install
// puts the table back.
func TestTheProxyTableLastKeepsOurMarkers(t *testing.T) {
	for short := range capturedDefaults {
		t.Run(short, func(t *testing.T) {
			m, ops, cfgPath := capturedWithHome(t, short+"-proxy-last-features-disable-network_proxy.toml")
			got := string(m.files[codexConfigPath])
			if strings.Count(got, agentsMarkerBegin) != 1 || strings.Count(got, agentsMarkerEnd) != 1 || strings.Contains(got, "[features.network_proxy]") {
				t.Fatalf("the capture is not the shape it is named for:\n%s", got)
			}
			if _, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(out, "every command Codex runs can reach any host") || strings.Contains(out, "lost one of its markers") {
				t.Errorf("status does not read the intact block with its proxy gone:\n%s", out)
			}
			if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK || !strings.Contains(out, "restore [features.network_proxy]") {
				t.Errorf("install did not restore the table (exit %d):\n%s", code, out)
			}
			assertNotOpenByUs(t, "after install", string(m.files[codexConfigPath]))
		})
	}
}

// `codex features enable` with no [features] table of the participant's
// writes one; install keeps it, and uninstall keeps it and puts the
// participant's default_permissions back.
func TestAFeaturesTableCodexWroteIsKept(t *testing.T) {
	for short, dp := range capturedDefaults {
		for _, layout := range []string{"-region-", "-proxy-last-"} {
			t.Run(short+layout, func(t *testing.T) {
				m, ops, cfgPath := capturedWithHome(t, short+layout+"features-enable-memories.toml")
				if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
					t.Fatalf("install: exit %d\n%s", code, out)
				}
				got := string(m.files[codexConfigPath])
				if doc, _ := decodeTOMLDoc(got); doc == nil || mustLookup(doc, "features", "memories") != true {
					t.Errorf("install lost features.memories:\n%s", got)
				}
				assertNotOpenByUs(t, "after install", got)
				if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
					t.Fatalf("uninstall: exit %d\n%s", code, out)
				}
				left, _ := decodeTOMLDoc(string(m.files[codexConfigPath]))
				if mustLookup(left, "features", "memories") != true || left["default_permissions"] != dp || strings.Contains(string(m.files[codexConfigPath]), "jevlin agents install") {
					t.Errorf("uninstall did not leave the participant's file:\n%s", m.files[codexConfigPath])
				}
			})
		}
	}
}

func mustLookup(doc tomlDoc, path ...string) any {
	v, _ := lookupTOMLPath(doc, path...)
	return v
}

// Codex reads our end marker as the leading comment of the participant's
// table right after our block, and removes it with that table (captured:
// `codex mcp remove foo`). The region with a lone begin marker is still
// ours, and recovering it must not take the participant's comment above
// their next table: status names the damage, install repairs it and keeps
// the comment, and uninstall removes the region whole and keeps it too.
func TestARegionThatLostItsEndMarkerKeepsTheParticipantsComment(t *testing.T) {
	const comment = "# trusted because I own it\n[projects.\"/x\"]"
	m, ops, cfgPath := capturedWithHome(t, "mcp-after-block-after-mcp-remove.toml")
	damaged := string(m.files[codexConfigPath])
	if !strings.Contains(damaged, agentsMarkerBegin) || strings.Contains(damaged, agentsMarkerEnd) || !strings.Contains(damaged, comment) {
		t.Fatalf("the capture is not the damage it is named for:\n%s", damaged)
	}
	if _, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath); !strings.Contains(out, "has lost one of its markers") {
		t.Errorf("status does not name the damage:\n%s", out)
	}
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	got := string(m.files[codexConfigPath])
	if code != exitOK || !strings.Contains(out, "repair jevlin's block") {
		t.Fatalf("install did not repair the block (exit %d):\n%s", code, out)
	}
	if !strings.Contains(got, comment) || strings.Count(got, agentsMarkerBegin) != 1 || strings.Count(got, agentsMarkerEnd) != 1 {
		t.Errorf("the repair lost the participant's comment, or is not one block:\n%s", got)
	}
	if _, again := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); string(m.files[codexConfigPath]) != got {
		t.Errorf("a second install changed the repaired file:\n%s", again)
	}

	m.files[codexConfigPath] = []byte(damaged)
	if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if left := string(m.files[codexConfigPath]); left != "model = \"gpt-5\"\n\n"+comment+"\ntrust_level = \"trusted\"\n" {
		t.Errorf("uninstall did not leave exactly the participant's lines:\n%q", left)
	}
}

// Codex's app server, switching back to ":workspace", rewrites the value
// of our marked default_permissions line and keeps our mark (captured on
// 0.158.0). That line is still ours: install asks the one question and on
// a yes sets it again, keeping what it was before our first change, which
// uninstall puts back; with no terminal the by-hand text shows that very
// change. A mark of another installation's is refused before anything is
// asked. Before, the yes was refused after it was given ("already carries
// a jevlin mark", exit 1), and the by-hand text said the changes could not
// be shown.
func TestALineCodexRewroteUnderOurMarkIsMarkedAgain(t *testing.T) {
	const fixture = "workspace-switched-appserver-default-permissions.toml"
	m, ops, cfgPath := capturedWithHome(t, fixture)
	captured := string(m.files[codexConfigPath])
	first := strings.SplitAfterN(captured, "\n", 2)[0]
	if !strings.HasPrefix(first, `default_permissions = ":workspace"  # jevlin agents install (`) || !strings.HasSuffix(first, "; was: default_permissions = \":workspace\"\n") {
		t.Fatalf("the capture is not the rewrite it is named for: %q", first)
	}

	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if code != exitUsage || strings.Contains(out, "cannot be shown") {
		t.Fatalf("with no terminal: exit %d, want %d, with the change shown:\n%s", code, exitUsage, out)
	}
	if !strings.Contains(out, "replace the line\n"+strings.TrimSuffix(first, "\n")+"\nwith\ndefault_permissions = \"jevlin\"  # jevlin agents install (") {
		t.Errorf("the by-hand text does not show the change a yes makes:\n%s", out)
	}

	m.terminal = true
	code, out = runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	got := string(m.files[codexConfigPath])
	if code != exitOK || !strings.Contains(out, `default_permissions = ":workspace" becomes "jevlin" (marked; agents uninstall puts ":workspace" back)`) {
		t.Fatalf("a yes was not taken (exit %d):\n%s", code, out)
	}
	line := strings.SplitAfterN(got, "\n", 2)[0]
	if !strings.HasPrefix(line, `default_permissions = "jevlin"  # jevlin agents install (`) || !strings.HasSuffix(line, "; was: default_permissions = \":workspace\"\n") || strings.Count(line, "#") != 1 {
		t.Errorf("the line is not marked again with what it first was: %q", line)
	}
	if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-client", "codex", "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if left := string(m.files[codexConfigPath]); !strings.HasPrefix(left, "default_permissions = \":workspace\"\nmodel = \"gpt-5\"\n") || strings.Contains(left, "jevlin") {
		t.Errorf("uninstall did not put the participant's line back:\n%s", left)
	}

	// The same line under another installation's mark.
	m.files[codexConfigPath] = []byte(strings.Replace(captured, mustTOMLString(cfgPath), mustTOMLString("/elsewhere/jevlin.toml"), 1))
	code, out = runAgentsAt(t, ops, "y\n", "install", "-config", cfgPath, "-client", "codex", "-yes")
	if code == exitOK || strings.Contains(out, "[y/N]") || !strings.Contains(out, "carries a jevlin mark that is not this installation's") {
		t.Errorf("another installation's mark was not refused before the question (exit %d):\n%s", code, out)
	}
}
