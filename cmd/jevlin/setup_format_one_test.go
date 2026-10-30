package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

// An installation an earlier version made, as 0.6.1 left it: a config with
// no schema line, host files rendered by that version (captured from its
// own `agents install`, under cmd/jevlin/testdata/setup/v061, with the
// binary, config and installation paths templated), and the identity —
// state/agent.json with fields that version kept and this one does not,
// credentials.json beside it. Running this version's setup over it is the
// whole move from that version, so what it must do is held here: set the
// config aside under a name of its own, write format 1, keep the
// registration and the key (no register call), rewrite the host files so
// nothing of the old rendering is left, and touch nothing else the earlier
// version left in the installation.

// v061Fixture reads one captured 0.6.1 artifact with its paths filled in for
// this sandbox.
func v061Fixture(t *testing.T, s *setupSandbox, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "setup", "v061", name)) // #nosec G304 -- a fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	r := strings.NewReplacer("<BIN>", s.exe, "<CFG>", s.cfgPath(), "<INSTALL>", s.home, "<HOME>", s.userHome)
	return []byte(r.Replace(string(b)))
}

// plantEarlierInstallation lays the 0.6.1 installation out in the sandbox
// and primes the platform stub with its identity, so a status or /me call
// for the stored key answers as the real platform would for a registration
// made then. It returns the legacy config's bytes.
func plantEarlierInstallation(t *testing.T, s *setupSandbox) []byte {
	t.Helper()
	write := func(path string, body []byte, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, mode); err != nil {
			t.Fatal(err)
		}
	}
	legacy := v061Fixture(t, s, "jevlin.toml")
	write(s.cfgPath(), legacy, 0o600)
	// The agent record that version wrote carries fields this one does not
	// read; they are ignored, not refused, and the agent id is kept.
	write(filepath.Join(s.home, "state", "agent.json"), []byte(`{"agent_id":"agent-old","status":"claimed","scopes":["search"],`+
		`"last_enrollment_slot":"slot-3","last_enrollment_at":"2026-10-20T10:00:00Z"}`+"\n"), 0o600)
	if err := writeCredentials(filepath.Join(s.home, credentialsFile), credentials{
		APIKey: "sr-old", Router: s.env["JEVLIN_ROUTER_URL"], AgentsAPI: s.platform.srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	s.platform.mu.Lock()
	s.platform.agentByKey["sr-old"] = "agent-old"
	s.platform.statusByAgent["agent-old"] = "claimed"
	s.platform.scopesByAgent["agent-old"] = []string{"search"}
	s.platform.mu.Unlock()
	// Files only the earlier version used, in the installation directory
	// and in a directory of its own. This version neither reads nor names
	// them.
	write(filepath.Join(s.home, "earlier-version.json"), []byte(`{"v":1}`+"\n"), 0o600)
	write(filepath.Join(s.home, "earlier", "record.json"), []byte(`{"x":1}`+"\n"), 0o600)
	// The host files that version's `agents install` wrote.
	for name, path := range map[string]string{
		"claude.settings.json": filepath.Join(s.userHome, ".claude", "settings.json"),
		"codex.config.toml":    filepath.Join(s.userHome, ".codex", "config.toml"),
		"codex.hooks.json":     filepath.Join(s.userHome, ".codex", "hooks.json"),
		"cursor.hooks.json":    filepath.Join(s.userHome, ".cursor", "hooks.json"),
		"hermes.config.yaml":   filepath.Join(s.userHome, ".hermes", "config.yaml"),
	} {
		write(path, v061Fixture(t, s, name), 0o600)
	}
	return legacy
}

func TestSetupOverAnEarlierInstallationReplacesTheConfigAndKeepsTheRegistration(t *testing.T) {
	s := newSetupSandbox(t)
	legacy := plantEarlierInstallation(t, s)
	earlierOnly := map[string][]byte{}
	for _, rel := range []string{"earlier-version.json", filepath.Join("earlier", "record.json")} {
		earlierOnly[rel] = s.readFile(filepath.Join(s.home, rel))
	}

	code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-with", "claude", "-with", "codex", "-with", "cursor", "-with", "hermes")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// The config: the old file set aside byte for byte, a format-1 file in
	// its place that loads, and the run saying so.
	aside := s.cfgPath() + ".old"
	if got := s.readFile(aside); string(got) != string(legacy) {
		t.Errorf("the set-aside config is not the old file byte for byte:\n%s", got)
	}
	if !strings.Contains(out, "Set "+s.cfgPath()+" aside as "+aside) || !strings.Contains(out, "configuration format 1") {
		t.Errorf("the run did not say what it did with the config:\n%s", out)
	}
	fresh := s.readFile(s.cfgPath())
	if !strings.HasPrefix(string(fresh), "schema = 1\n") {
		t.Errorf("the new config is not format 1:\n%s", fresh)
	}
	if _, _, err := config.Load([]string{"-config", s.cfgPath()}, s.getenv); err != nil {
		t.Errorf("the new config does not load: %v", err)
	}

	// The registration: kept, never re-registered.
	if register, status, me := s.platform.counts(); register != 0 || status+me == 0 {
		t.Errorf("register=%d status=%d me=%d; the registration must be kept and verified, never made again", register, status, me)
	}
	store, err := auth.OpenStoreExisting(filepath.Join(s.home, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := store.LoadAgentRegistration(); err != nil || !ok || rec.AgentID != "agent-old" {
		t.Errorf("agent record after setup: %+v ok=%v err=%v; want agent-old kept", rec, ok, err)
	}
	if c, err := readCredentials(filepath.Join(s.home, credentialsFile)); err != nil || c.APIKey != "sr-old" {
		t.Errorf("credentials after setup: %+v err=%v; want the stored key kept", c, err)
	}

	// The host files: this version's rendering, with nothing of the old one
	// left. The Stop hook that version installed under another name is the
	// one place the two renderings differ in a word, and the Codex profile's
	// roots are where they differ in a directory.
	claude := string(s.readFile(filepath.Join(s.userHome, ".claude", "settings.json")))
	if strings.Contains(claude, "' flush\"") || !strings.Contains(claude, "' stop\"") {
		t.Errorf("Claude Code's settings keep the old Stop hook or lack the new one:\n%s", claude)
	}
	codexCfg := string(s.readFile(filepath.Join(s.userHome, ".codex", "config.toml")))
	for _, old := range []string{"/intake\"", "/spool\""} {
		if strings.Contains(codexCfg, old) {
			t.Errorf("Codex's profile still grants a directory only the earlier version used (%s):\n%s", old, codexCfg)
		}
	}
	for _, want := range []string{filepath.Join(s.home, "state"), filepath.Join(s.home, "sessions")} {
		if !strings.Contains(codexCfg, want) {
			t.Errorf("Codex's profile does not grant %s:\n%s", want, codexCfg)
		}
	}
	for _, path := range []string{
		filepath.Join(s.userHome, ".codex", "hooks.json"),
		filepath.Join(s.userHome, ".cursor", "hooks.json"),
		filepath.Join(s.userHome, ".hermes", "config.yaml"),
	} {
		if body := string(s.readFile(path)); !strings.Contains(body, s.exe) || !strings.Contains(body, s.cfgPath()) {
			t.Errorf("%s no longer names this installation:\n%s", path, body)
		}
	}

	// What the earlier version left and this one does not know: untouched.
	for rel, before := range earlierOnly {
		if after := s.readFile(filepath.Join(s.home, rel)); string(after) != string(before) {
			t.Errorf("%s was changed", rel)
		}
	}
}

// A dry run over the same installation says what it would do with the
// config and changes nothing, in the installation or in the host files.
func TestSetupDryRunOverAnEarlierInstallationChangesNothing(t *testing.T) {
	s := newSetupSandbox(t)
	plantEarlierInstallation(t, s)
	before := snapshotTree(t, s.userHome)

	code, out, errOut := s.run(nil, true, "-dry-run", "-yes", "-no-profile", "-with", "claude", "-with", "codex")
	if code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "(dry run) would set "+s.cfgPath()+" aside as "+s.cfgPath()+".old") {
		t.Errorf("the dry run did not say it would set the config aside:\n%s", out)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, s.userHome)) {
		t.Error("the dry run changed a file")
	}
	if s.connectCalls != 0 {
		t.Error("the dry run ran connect")
	}
}

// A second setup over the replaced installation finds format 1 and leaves
// everything as it is: the set-aside file is not set aside again.
func TestSetupAfterTheReplacementIsANoOpForTheConfig(t *testing.T) {
	s := newSetupSandbox(t)
	plantEarlierInstallation(t, s)
	if code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents"); code != exitOK {
		t.Fatalf("first run: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	before := snapshotTree(t, s.home)
	code, out, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents")
	if code != exitOK {
		t.Fatalf("second run: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "already configuration format 1") {
		t.Errorf("the second run did not find format 1:\n%s", out)
	}
	if lexists(s.cfgPath() + ".old.1") {
		t.Error("the second run set a config aside again")
	}
	after := snapshotTree(t, s.home)
	delete(before, filepath.Join(s.home, "state", "connect.lock"))
	delete(after, filepath.Join(s.home, "state", "connect.lock"))
	if !reflect.DeepEqual(before, after) {
		t.Error("the second run changed the installation")
	}
}
