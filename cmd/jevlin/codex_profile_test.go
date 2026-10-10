package main

// Codex writing into our region, and what status and install make of it.
//
// testdata/codex/ holds Codex's own output for these cases, captured on
// 2026-10-09 from codex-cli 0.158.0 on Linux against a scratch CODEX_HOME
// whose config.toml held the region and then [tui]
// (region-then-tui.toml, rootkey-region-then-tui.toml):
//
//	features-enable-memories.toml        `codex features enable memories`
//	features-disable-network_proxy.toml  `codex features disable network_proxy`
//	appserver-model-upsert.toml          app-server config/value/write
//	                                     {keyPath:"model", value:"gpt-probe",
//	                                     mergeStrategy:"upsert"}
//	appserver-model-upsert-rootkey.toml  the same, with approval_policy above
//	                                     the region
//
// They are Codex's bytes with one change: the scratch state directory is
// <STATE_DIR>, which each test replaces with its own. The region in them is
// the phase-1 rendering, without the comment lines the renderer now writes;
// the reader treats comments inside the markers as ours, so it reads the
// same. The maintainer saw the same shapes from Codex 0.160.0 on macOS.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runAgentsAt runs agentsMain with stdin, at whatever terminal ops says.
func runAgentsAt(t *testing.T, ops agentOps, stdin string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := agentsMain(ops, args, strings.NewReader(stdin), &out, &out, envOf(nil))
	return code, out.String()
}

// capturedCodexConfig is a Codex capture with this test's state dir in it,
// on a fake machine, beside a config naming that state dir and the
// capture's router.
func capturedCodexConfig(t *testing.T, name string) (m *fakeMachine, ops agentOps, cfgPath, state string) {
	t.Helper()
	onCodexOS(t, "linux")
	home := t.TempDir()
	state = filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(home, "jevlin.toml")
	doc := "[mining]\nstate_dir = " + mustTOMLString(state) + "\n\n[miner]\nenabled = false\nrouter_url = \"https://router-api.nyks.dev\"\n"
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ops = newFakeMachine("codex")
	m.files[codexConfigPath] = []byte(strings.ReplaceAll(codexConfigFixture(t, name), `"<STATE_DIR>"`, mustTOMLString(state)))
	return m, ops, cfgPath, state
}

// What Codex wrote inside the markers is the participant's and is kept —
// a root key just above the region, a table just below it — on install,
// and on uninstall it stays; a second install then writes nothing.
func TestWhatCodexWritesInsideTheRegionIsKept(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		keep    []string // keys Codex wrote, as dotted paths, with the value
		above   bool     // a root key: it must end up above the region
	}{
		{"features-enable-memories.toml", []string{"features.memories"}, false},
		{"appserver-model-upsert.toml", []string{"model"}, true},
		{"appserver-model-upsert-rootkey.toml", []string{"model", "approval_policy"}, true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			m, ops, cfgPath, _ := capturedCodexConfig(t, tc.fixture)
			before, _ := decodeTOMLDoc(string(m.files[codexConfigPath]))

			code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("install: exit %d\n%s", code, out)
			}
			got := string(m.files[codexConfigPath])
			doc, ok := decodeTOMLDoc(got)
			if !ok {
				t.Fatalf("install left a file that does not decode:\n%s", got)
			}
			for _, k := range tc.keep {
				path := strings.Split(k, ".")
				want, _ := lookupTOMLPath(before, path...)
				if have, _ := lookupTOMLPath(doc, path...); have != want || want == nil {
					t.Errorf("%s = %v after install, want %v:\n%s", k, have, want, got)
				}
			}
			r, had, why := readCodexRegion([]byte(got))
			if !had || why != "" || r.hasForeign() {
				t.Errorf("the region still holds what Codex wrote (had=%v why=%q):\n%s", had, why, got)
			}
			if tc.above && !strings.Contains(r.pre, tc.keep[0]+" = ") {
				t.Errorf("Codex's root key is not above the region:\n%s", got)
			}
			if !strings.Contains(out, "moving") {
				t.Errorf("the plan does not say what it moved:\n%s", out)
			}

			if _, again := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); string(m.files[codexConfigPath]) != got {
				t.Errorf("a second install changed the file:\n%s", again)
			}

			if code, rm := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, rm)
			}
			left, _ := decodeTOMLDoc(string(m.files[codexConfigPath]))
			for _, k := range append(tc.keep, "tui.screen_reader_detection_done") {
				path := strings.Split(k, ".")
				want, _ := lookupTOMLPath(before, path...)
				if have, _ := lookupTOMLPath(left, path...); have != want {
					t.Errorf("uninstall lost %s:\n%s", k, m.files[codexConfigPath])
				}
			}
			if holdsOurTable(string(m.files[codexConfigPath])) {
				t.Errorf("uninstall left a table of ours:\n%s", m.files[codexConfigPath])
			}
		})
	}
}

// Uninstall straight from Codex's file, with no install between: what Codex
// wrote inside the markers is still never deleted.
func TestUninstallKeepsWhatCodexWroteInsideTheRegion(t *testing.T) {
	for _, fixture := range []string{"features-enable-memories.toml", "appserver-model-upsert.toml", "appserver-model-upsert-rootkey.toml"} {
		t.Run(fixture, func(t *testing.T) {
			m, ops, cfgPath, _ := capturedCodexConfig(t, fixture)
			before, _ := decodeTOMLDoc(string(m.files[codexConfigPath]))
			if code, out := runAgentsAt(t, ops, "", "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s", code, out)
			}
			left, ok := decodeTOMLDoc(string(m.files[codexConfigPath]))
			if !ok {
				t.Fatalf("uninstall left a file that does not decode:\n%s", m.files[codexConfigPath])
			}
			for _, p := range codexOurPaths() {
				deleteTOMLPath(before, p...)
			}
			if !tomlDocsEqual(before, left) {
				t.Errorf("uninstall changed something that was not ours\n got %v\nwant %v", left, before)
			}
		})
	}
}

// `codex features disable network_proxy` deletes our proxy table, and
// the profile is then open to every host. Status says so in those words;
// install puts the table back and says why.
func TestAProxyTableCodexDeletedIsNamedAndRestored(t *testing.T) {
	m, ops, cfgPath, _ := capturedCodexConfig(t, "features-disable-network_proxy.toml")

	code, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath)
	if code != exitOK {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "network_proxy is off (the [features.network_proxy] table is gone)") ||
		!strings.Contains(out, "every command Codex runs can reach any host") {
		t.Errorf("status does not say the network is open to every host:\n%s", out)
	}

	code, out = runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	if _, _, proxy, _ := profileOf(t, string(m.files[codexConfigPath])); !proxy {
		t.Errorf("install did not restore the proxy table:\n%s", m.files[codexConfigPath])
	}
	if !strings.Contains(out, "restore [features.network_proxy], which had been turned off: without it every command Codex runs could reach any host") {
		t.Errorf("the plan does not say why the table came back:\n%s", out)
	}
	if _, out = runAgentsAt(t, ops, "", "status", "-config", cfgPath); strings.Contains(out, "network_proxy is off") {
		t.Errorf("status still says the proxy is off after install:\n%s", out)
	}
}

// Status names the profile, its hosts against all three the
// config names, and a stale one.
func TestStatusComparesTheProfileWithEveryHostTheConfigNames(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	if code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	_, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath)
	if !strings.Contains(out, `permissions: profile "jevlin"; hosts agents-v1.nyks.dev, as.example.invalid, router.example.invalid; network_proxy on`) {
		t.Errorf("status does not name the profile and its hosts:\n%s", out)
	}
	if strings.Contains(out, "agents install refreshes it") {
		t.Errorf("a current profile is called stale:\n%s", out)
	}
	// Each of the three, changed in the config, makes the profile stale.
	for _, change := range []struct{ from, to string }{
		{`router_url = "https://router.example.invalid"`, `router_url = "https://router2.example.invalid"`},
		{`as_url = "https://as.example.invalid"`, `as_url = "https://as2.example.invalid"`},
		{"[mining]", "[platform]\nagents_api_url = \"https://agents2.example.invalid\"\n\n[mining]"},
	} {
		orig, err := os.ReadFile(cfgPath) // #nosec G304 -- this test's own config
		if err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(string(orig), change.from, change.to, 1)
		if changed == string(orig) {
			t.Fatalf("the config did not change at %q", change.from)
		}
		if err := os.WriteFile(cfgPath, []byte(changed), 0o600); err != nil { // #nosec G703 -- this test's own config under t.TempDir
			t.Fatal(err)
		}
		_, out := runAgentsAt(t, ops, "", "status", "-config", cfgPath)
		if !strings.Contains(out, "but the config now names") {
			t.Errorf("a changed %q did not make the profile stale:\n%s", change.from, out)
		}
		if err := os.WriteFile(cfgPath, orig, 0o600); err != nil { // #nosec G703 -- this test's own config under t.TempDir
			t.Fatal(err)
		}
	}
	_ = m
}

// The hosts are the router's, the AS's when as_url is set, and the
// platform agents API's, and nothing else — in particular never
// platform.base_url, which nothing dials (invariant 12).
func TestTheAllowedHostsAreDerivedFromTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		want      string
	}{
		{"router only, platform defaulted", "[miner]\nrouter_url = \"https://r.example.invalid/v1\"\n", "agents-v1.nyks.dev|r.example.invalid"},
		{"with an AS", "[mining]\nas_url = \"https://AS.example.invalid:8443\"\nchain_id = \"c\"\nslot_id = 1\n[miner]\nrouter_url = \"https://r.example.invalid\"\n", "agents-v1.nyks.dev|as.example.invalid|r.example.invalid"},
		{"a platform of its own, base_url not dialed", "[platform]\nbase_url = \"https://portal.example.invalid\"\nagents_api_url = \"https://api.example.invalid\"\n[miner]\nrouter_url = \"https://r.example.invalid\"\n", "api.example.invalid|r.example.invalid"},
		{"a loopback router is never listed", "[miner]\nrouter_url = \"http://127.0.0.1:18780\"\n", "agents-v1.nyks.dev"},
		{"nor a loopback AS or platform", "[platform]\nbase_url = \"http://localhost:9\"\nagents_api_url = \"http://[::1]:9\"\n[mining]\nas_url = \"http://127.0.0.2:9\"\nchain_id = \"c\"\nslot_id = 1\n[miner]\nrouter_url = \"https://r.example.invalid\"\n", "r.example.invalid"},
		{"one host serving two roles", "[mining]\nas_url = \"https://r.example.invalid\"\nchain_id = \"c\"\nslot_id = 1\n[miner]\nrouter_url = \"https://r.example.invalid\"\n", "agents-v1.nyks.dev|r.example.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "jevlin.toml")
			if err := os.WriteFile(p, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := loadConfig(p, noEnv)
			if err != nil {
				t.Fatalf("config: %v", err)
			}
			if got := strings.Join(codexAllowedHosts(cfg), "|"); got != tc.want {
				t.Errorf("hosts = %s, want %s", got, tc.want)
			}
		})
	}
}

// default_permissions naming another installation's profile is left to
// it, said so, and the participant is not asked to add anything to it.
func TestAnotherInstallationsProfileIsNotAskedAbout(t *testing.T) {
	m := newTwoCodexInstallations(t)
	m.install(m.first)
	before := m.codexConfig()
	m.ops.isTerminal = func() bool { return true }
	code, out := m.agents("install", "-config", m.second, "-client", "codex")
	if strings.Contains(out, "[y/N]") {
		t.Errorf("the participant was asked to add entries to another installation's profile:\n%s", out)
	}
	if !strings.Contains(out, m.leftSentence()) {
		t.Errorf("the plan does not say whose the profile is (exit %d):\n%s", code, out)
	}
	m.unchangedSince(before, "the second installation's install at a terminal")
}

// The net under install, on the one shape where the line work alone would
// lose a key: a region found below a table header, holding a root key Codex
// wrote after our default_permissions. Moving the region up takes the
// marker-to-marker range, and the key with it; in place, that key reads as
// the table above's. The decoded comparison sees the key gone and refuses,
// so the file is left exactly as it was and the plan says why.
func TestInstallNeverLosesAKeyItCannotPlace(t *testing.T) {
	m, ops, cfgPath, _ := capturedCodexConfig(t, "appserver-model-upsert.toml")
	below := "[tui]\nscreen_reader_detection_done = true\n\n" + strings.Replace(string(m.files[codexConfigPath]), "\n[tui]\nscreen_reader_detection_done = true\n", "", 1)
	m.files[codexConfigPath] = []byte(below)
	if r, had, why := readCodexRegion([]byte(below)); !had || why != "" || r.atTop || r.foreignRoot == "" {
		t.Fatalf("this case needs a readable region below a header holding a root key of Codex's (had=%v why=%q):\n%s", had, why, below)
	}
	_, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if got := string(m.files[codexConfigPath]); got != below {
		t.Errorf("install rewrote a file where it could not place a key:\n%s", got)
	}
	if !strings.Contains(out, "would change the meaning of something else in the file") {
		t.Errorf("the plan does not say why nothing was written:\n%s", out)
	}
}

// A loopback service is said, not listed: listing it would let every
// sandboxed command reach any port on this machine through Codex's proxy,
// and the search still could not use it, since Go never proxies a loopback
// target.
func TestALoopbackRouterIsNamedInThePlanAndNotListed(t *testing.T) {
	onCodexOS(t, "linux")
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "jevlin.toml")
	doc := "[mining]\nstate_dir = " + mustTOMLString(state) + "\n\n[miner]\nenabled = false\nrouter_url = \"http://127.0.0.1:18780\"\n"
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ops := newFakeMachine("codex")
	code, out := runAgentsAt(t, ops, "", "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	if _, hosts, _, _ := profileOf(t, string(m.files[codexConfigPath])); containsString(hosts, "127.0.0.1") {
		t.Errorf("the profile lists a loopback host: %q", hosts)
	}
	if !strings.Contains(out, "the router (127.0.0.1) is on this machine's loopback, which no command in Codex's sandbox can reach") {
		t.Errorf("the plan does not say the loopback router cannot be reached:\n%s", out)
	}
}
