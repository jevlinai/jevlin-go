package main

// The Codex sandbox fix: a search's mining observation is written under the
// jevlin home, which Codex's default workspace-write sandbox denies. The
// installer now widens that sandbox, and a blocked write is reported loudly
// instead of silently. All identifiers here are synthetic.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntakeWriteBlockedClassifiesSandboxDenials(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"permission (Linux/Landlock EACCES)", fs.ErrPermission, true},
		{"wrapped permission", fmt.Errorf("mkdir: %w", fs.ErrPermission), true},
		{"read-only fs (macOS/Seatbelt EROFS)", errors.New("open x.tmp: read-only file system"), true},
		{"unrelated failure", io.ErrUnexpectedEOF, false},
	}
	for _, c := range cases {
		if got := intakeWriteBlocked(c.err); got != c.want {
			t.Errorf("%s: intakeWriteBlocked=%v want %v", c.name, got, c.want)
		}
	}
}

// A real filesystem denial (not a synthetic error) must classify as blocked,
// so the loud, actionable message fires rather than the generic one.
func TestWriteIntakeToUnwritableDirIsClassifiedBlocked(t *testing.T) {
	if !posixModes || os.Geteuid() == 0 {
		t.Skip("needs POSIX mode bits and a non-root uid to make a dir unwritable")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil { // #nosec G302 -- deliberately unwritable, to force a real permission denial
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) }) // #nosec G302 -- restore so t.TempDir cleanup can remove it

	_, err := writeIntake(filepath.Join(parent, "intake"), intakeRecord{RequestID: "r1", FinishedAt: time.Now()})
	if err == nil {
		t.Fatal("expected the write into a read-only parent to fail")
	}
	if !intakeWriteBlocked(err) {
		t.Errorf("a real permission denial was not classified as a sandbox block: %v", err)
	}
}

// onCodexOS plans Codex's sandbox for goos for the rest of this test, so a
// case about the permission profile holds on the Windows runners too, and a
// case about Windows' roots-only block holds everywhere.
func onCodexOS(t *testing.T, goos string) {
	t.Helper()
	was := codexSandboxOS
	codexSandboxOS = goos
	t.Cleanup(func() { codexSandboxOS = was })
}

// sandboxTestConfig writes a real config (loadConfig reads the real fs) whose
// mining/miner dirs all sit under one jevlin home, and returns that home.
func sandboxTestConfig(t *testing.T) (cfgPath, home string) {
	t.Helper()
	onCodexOS(t, "linux")
	home = t.TempDir()
	for _, d := range []string{"state", "spool", "intake", "sessions"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	doc := fmt.Sprintf(`
[mining]
enabled = true
as_url = "https://as.example.invalid"
chain_id = "twilight-1"
slot_id = 7
state_dir = %q
spool_dir = %q

[miner]
enabled = true
router_url = "https://router.example.invalid"
intake_dir = %q
sessions_dir = %q
`, filepath.ToSlash(filepath.Join(home, "state")), filepath.ToSlash(filepath.Join(home, "spool")),
		filepath.ToSlash(filepath.Join(home, "intake")), filepath.ToSlash(filepath.Join(home, "sessions")))
	cfgPath = filepath.Join(home, "jevlin.toml")
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, home
}

// profileOf decodes the jevlin profile out of a Codex config: the roots it
// grants, the hosts it allows, and whether the proxy table and the key are
// there. Decoded, never matched as bytes, so a quoting difference between
// OSes cannot pass or fail it.
func profileOf(t *testing.T, file string) (roots, hosts []string, proxy bool, active string) {
	t.Helper()
	doc, ok := decodeTOMLDoc(file)
	if !ok {
		t.Fatalf("config.toml does not decode:\n%s", file)
	}
	roots = keysWithValue(doc, "write", "permissions", codexProfileName, "filesystem")
	hosts = keysWithValue(doc, "allow", "permissions", codexProfileName, "network", "domains")
	if v, ok := lookupTOMLPath(doc, "features", "network_proxy", "enabled"); ok {
		proxy, _ = v.(bool)
	}
	active, _ = doc["default_permissions"].(string)
	return roots, hosts, proxy, active
}

func TestCodexInstallWritesTheProfileAndUninstallRemovesIt(t *testing.T) {
	cfgPath, home := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	// Codex already has an unrelated setting the tool must preserve.
	original := "model = \"gpt-5\"\n"
	m.files["/home/u/.codex/config.toml"] = []byte(original)

	if code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	got := string(m.files["/home/u/.codex/config.toml"])
	roots, hosts, proxy, active := profileOf(t, got)
	if active != codexProfileName {
		t.Errorf("default_permissions = %q, want %q:\n%s", active, codexProfileName, got)
	}
	if !proxy {
		t.Errorf("the network proxy is not on, so the host list would not be enforced:\n%s", got)
	}
	// The four directories themselves, sorted — never the home.
	want := []string{filepath.Join(home, "intake"), filepath.Join(home, "sessions"), filepath.Join(home, "spool"), filepath.Join(home, "state")}
	if strings.Join(roots, "|") != strings.Join(want, "|") {
		t.Errorf("roots = %q, want %q", roots, want)
	}
	// The hosts the config names, and no other.
	if strings.Join(hosts, "|") != "agents-v1.nyks.dev|as.example.invalid|router.example.invalid" {
		t.Errorf("hosts = %q, want the platform, the AS and the router", hosts)
	}
	// The old block's open network is gone for good.
	if strings.Contains(got, "network_access") || strings.Contains(got, "["+codexSandboxTable+"]") {
		t.Errorf("the old open-network table is still written:\n%s", got)
	}
	// The home itself must never be a writable root: it holds jevlin.toml,
	// credentials.json and wallet/, and a sandboxed command that can rewrite
	// the config can redirect the credentials to any https host on the next
	// run. Asserted on its own so the guard has a test that goes red by
	// itself.
	if dirsInclude(roots, home) {
		t.Errorf("the jevlin home itself is a writable root:\n%s", got)
	}

	// Idempotent: a second install changes nothing.
	before := got
	if code, _, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatal("second install failed")
	}
	if after := string(m.files["/home/u/.codex/config.toml"]); after != before {
		t.Errorf("second install was not idempotent:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// Uninstall removes only our region; the file is what the participant had.
	if code, _, _ := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatal("uninstall failed")
	}
	if left := string(m.files["/home/u/.codex/config.toml"]); left != original {
		t.Errorf("uninstall did not return the file to the participant's bytes:\n%q", left)
	}
}

// A [sandbox_workspace_write] table of the participant's own is a sandbox
// they configured, and Codex does not combine it with a profile: nothing is
// installed for Codex, the table is left exactly as it was, and the profile
// is printed to adopt by hand.
func TestCodexInstallLeavesCodexAloneBesideTheParticipantsSandboxTable(t *testing.T) {
	cfgPath := func() string { c, _ := sandboxTestConfig(t); return c }()
	m, ops := newFakeMachine("codex")
	foreign := "[sandbox_workspace_write]\nnetwork_access = false\nwritable_roots = [\"/tmp/mine\"]\n"
	m.files["/home/u/.codex/config.toml"] = []byte(foreign)

	// A refusal is a partial failure: non-zero exit (as with any refused surface).
	code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitTransport {
		t.Fatalf("install exit: %d want exitTransport\n%s", code, out)
	}
	if !strings.Contains(out, "has a [sandbox_workspace_write] table of your own") || !strings.Contains(out, "default_permissions = \"jevlin\"") {
		t.Errorf("expected a refusal with the profile to adopt by hand, got:\n%s", out)
	}
	if string(m.files["/home/u/.codex/config.toml"]) != foreign {
		t.Errorf("the user's own sandbox table was modified:\n%s", m.files["/home/u/.codex/config.toml"])
	}
	if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; ok {
		t.Error("the skill was written though nothing was to be installed for Codex")
	}
}

// On Windows, where no profile has been seen to work, the old rule stands
// for the participant's own table: it is left and the roots are printed to
// add by hand; the skill and hooks go on as before.
func TestCodexWindowsInstallRefusesForeignSandboxTable(t *testing.T) {
	cfgPath := func() string { c, _ := sandboxTestConfig(t); return c }()
	onCodexOS(t, "windows")
	m, ops := newFakeMachine("codex")
	foreign := "[sandbox_workspace_write]\nnetwork_access = false\nwritable_roots = [\"/tmp/mine\"]\n"
	m.files["/home/u/.codex/config.toml"] = []byte(foreign)
	code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitTransport {
		t.Fatalf("install exit: %d want exitTransport\n%s", code, out)
	}
	if !strings.Contains(out, "already defines [sandbox_workspace_write]") || strings.Contains(out, "network_access = true") {
		t.Errorf("expected a refusal with a roots-only snippet, got:\n%s", out)
	}
	if string(m.files["/home/u/.codex/config.toml"]) != foreign {
		t.Errorf("the user's own sandbox table was modified:\n%s", m.files["/home/u/.codex/config.toml"])
	}
}

// Windows gets the writable roots and no network, and the plan says a
// search there needs Codex's approval to reach the router.
func TestCodexWindowsInstallWritesRootsWithoutNetwork(t *testing.T) {
	cfgPath, home := sandboxTestConfig(t)
	onCodexOS(t, "windows")
	m, ops := newFakeMachine("codex")
	code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	got := string(m.files["/home/u/.codex/config.toml"])
	if strings.Contains(got, "network_access") || strings.Contains(got, "default_permissions") || strings.Contains(got, "[permissions.") {
		t.Errorf("Windows got network or a profile:\n%s", got)
	}
	if r, had, why := readCodexRegion([]byte(got)); !had || why != "" || !r.legacy || !dirsInclude(r.roots, filepath.Join(home, "state")) {
		t.Errorf("Windows did not get its roots (had=%v why=%q):\n%s", had, why, got)
	}
	if !strings.Contains(out, codexWindowsNetworkSentence) || !strings.Contains(out, codexWindowsWhy) {
		t.Errorf("the plan does not say a search there needs Codex's approval:\n%s", out)
	}
}

// With [miner] off the miner records nothing — but the detached claim resume
// still writes the state dir after every served search, so the profile must
// exist with the state dir as its one writable root, and not the others.
func TestCodexInstallWithMinerOffStillWritesStateDirRoot(t *testing.T) {
	onCodexOS(t, "linux")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf("[mining]\nstate_dir = %q\n\n[miner]\nenabled = false\n",
		filepath.ToSlash(filepath.Join(home, "state")))
	cfgPath := filepath.Join(home, "jevlin.toml")
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ops := newFakeMachine("codex")
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	got := string(m.files["/home/u/.codex/config.toml"])
	if got == "" {
		t.Fatal("no profile written though the claim resume writes the state dir")
	}
	roots, hosts, proxy, _ := profileOf(t, got)
	if !proxy || len(hosts) == 0 {
		t.Errorf("no enforced host list (proxy %v, hosts %q):\n%s", proxy, hosts, got)
	}
	// The state dir alone.
	if len(roots) != 1 || !samePath(roots[0], filepath.Join(home, "state")) {
		t.Errorf("the roots are not the state dir alone: %q\n%s", roots, got)
	}
}

// No readable config at all: there is no state dir to name, so no block is
// written — just the note.
func TestCodexInstallWithNoReadableConfigWritesNoBlock(t *testing.T) {
	m, ops := newFakeMachine("codex")
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	if b, ok := m.files["/home/u/.codex/config.toml"]; ok {
		t.Errorf("wrote a config.toml with no readable config:\n%s", b)
	}
}

// A layout that nests the installation's own directory (credentials.json,
// flush.lock) or the config's directory inside a directory the block would
// grant gets no block at all, and install says why: hard invariant 19 rests on
// those two never being writable from the sandbox.
func TestCodexInstallRefusesALayoutThatNestsTheInstallationInARoot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layout func(home string) (cfgPath string, doc string)
	}{
		{"intake_dir inside state_dir", func(home string) (string, string) {
			st := filepath.Join(home, "state")
			return filepath.Join(home, "jevlin.toml"), nestedDoc(st, filepath.Join(home, "spool"), filepath.Join(st, "intake"), filepath.Join(home, "sessions"))
		}},
		{"the config inside state_dir", func(home string) (string, string) {
			st := filepath.Join(home, "state")
			return filepath.Join(st, "jevlin.toml"), nestedDoc(st, filepath.Join(home, "spool"), filepath.Join(home, "intake"), filepath.Join(home, "sessions"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfgPath, doc := tc.layout(home)
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			m, ops := newFakeMachine("codex")
			code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("install: %d\n%s", code, out)
			}
			if b := m.files["/home/u/.codex/config.toml"]; strings.Contains(string(b), "writable_roots") {
				t.Fatalf("granted writable roots to a layout that nests the installation:\n%s", b)
			}
			if !strings.Contains(out, "not widening the sandbox") {
				t.Fatalf("install did not say why it wrote no block:\n%s", out)
			}
		})
	}
}

func nestedDoc(state, spool, intake, sessions string) string {
	return fmt.Sprintf(`
[mining]
enabled = true
as_url = "https://as.example.invalid"
chain_id = "twilight-1"
slot_id = 7
state_dir = %q
spool_dir = %q

[miner]
enabled = true
router_url = "https://router.example.invalid"
intake_dir = %q
sessions_dir = %q
`, filepath.ToSlash(state), filepath.ToSlash(spool), filepath.ToSlash(intake), filepath.ToSlash(sessions))
}
