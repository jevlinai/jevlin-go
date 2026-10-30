package main

// uninstall (uninstall.go): the conservative reverse of setup, the separate
// -binary and -purge-state, and the refusals that keep a purge from happening
// by accident, underneath a running operation, or anywhere but an
// installation.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// ── harness ─────────────────────────────────────────────────────────────

// uninstallDeps runs uninstall against the setup sandbox: the same agents,
// profile, user environment and home a setup in it wrote.
func (s *setupSandbox) uninstallDeps(stdin io.Reader, interactive bool) (uninstallDeps, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	return uninstallDeps{
		stdin:       stdin,
		stdout:      &out,
		stderr:      &errOut,
		getenv:      s.getenv,
		userHome:    s.userHome,
		executable:  func() (string, error) { return s.exe, nil },
		interactive: interactive,
		agents:      s.agentOps(interactive),
		targets:     installTargets,
		userEnv:     s.userEnv,
		windows:     runtime.GOOS == "windows",
	}, &out, &errOut
}

func (s *setupSandbox) uninstall(t *testing.T, stdin io.Reader, interactive bool, args ...string) (int, string, string) {
	t.Helper()
	d, out, errOut := s.uninstallDeps(stdin, interactive)
	code := uninstallMain(d, args)
	return code, out.String(), errOut.String()
}

// installed is a sandbox after a real, complete setup that set up Claude
// Code, Codex and Pi, wrote the profile block (Windows: the user environment)
// and holds a registration, a stored key, a session, a preference, a
// registration journal and the state directory an adoption set aside from
// an unfinished registration. Every one of them is in the purge set, which
// is why they are planted: a default run keeps each byte for byte and a
// purge removes each.
func installed(t *testing.T) *setupSandbox {
	t.Helper()
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	s.onPath["claude"] = true
	s.onPath["codex"] = true
	if code, out, errOut := s.run(nil, false, "-yes", "-with", "claude", "-with", "codex", "-with", "pi"); code != exitOK {
		t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for rel, body := range map[string]string{
		filepath.Join("sessions", "s-1.json"): `{}`,
		preferFile:                            "builtin\n",
		filepath.Join(stateAsidePrefix+"-20260101", "x.key"): "half",
		// Beside credentials.json: a registration journal, which can hold a
		// platform key, so a default uninstall keeps it byte for byte and a
		// purge removes it.
		registrationJournalFile: `{"v":1}`,
	} {
		writeFileT(t, filepath.Join(s.home, rel), body)
	}
	s.onPath = map[string]bool{} // uninstall must not depend on detection
	return s
}

// TestDryRunGroupsEveryHostsLinesUnderItsOwnHeading guards a defect from the
// Windows soak: the dry run printed opencode's, Pi's and Hermes' removals
// under the "Cursor" heading. The cause was that a removal carried only a
// path while a write carried its host, and the printer emitted a heading only
// when a write's host changed — so a host with nothing to rewrite, only files
// to delete, never got a heading and its lines fell under the previous one.
//
// The assertion walks the output and attributes every file line to the last
// heading above it, which is exactly how a participant reads it.
func TestDryRunGroupsEveryHostsLinesUnderItsOwnHeading(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	// Cursor writes hooks.json and deletes a skill; opencode only deletes;
	// Pi only deletes, two files. That ordering is the soak's.
	if code, out, errOut := s.run(nil, false, "-yes", "-with", "cursor", "-with", "opencode", "-with", "pi"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	s.onPath = map[string]bool{}

	code, out, errOut := s.uninstall(t, nil, false, "-dry-run")
	if code != exitOK {
		t.Fatalf("uninstall -dry-run exited %d\n%s\n%s", code, out, errOut)
	}

	paths := s.paths()
	wantHeadingOf := map[string]string{
		tilde(s.userHome, paths.cursorHooks):               "Cursor",
		tilde(s.userHome, filepath.Dir(paths.cursorSkill)): "Cursor",
		tilde(s.userHome, paths.opencodePlugin):            "opencode",
		tilde(s.userHome, filepath.Dir(paths.piSkill)):     "Pi",
		tilde(s.userHome, paths.piExtension):               "Pi",
	}
	heading := ""
	seen := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "    write  "), strings.HasPrefix(line, "    remove "):
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) >= 2 {
				seen[fields[1]] = heading
			}
		case strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   "):
			heading = strings.TrimSpace(line)
		}
	}
	for path, want := range wantHeadingOf {
		got, ok := seen[path]
		if !ok {
			t.Errorf("the dry run never listed %s:\n%s", path, out)
			continue
		}
		if got != want {
			t.Errorf("%s is printed under the %q heading, want %q:\n%s", path, got, want, out)
		}
	}
}

// participantBytes is every participant byte under the installation, minus
// the empty lock files coordination makes: what a run that is not a purge
// must leave exactly as it found it.
func participantBytes(t *testing.T, s *setupSandbox) map[string]fileSig {
	t.Helper()
	out := map[string]fileSig{}
	for _, p := range participantState(s) {
		if !lexists(p) {
			continue
		}
		for path, sig := range snapshotTree(t, p) {
			out[path] = sig
		}
	}
	return withoutLocks(out)
}

func participantState(s *setupSandbox) []string {
	h := func(rel string) string { return filepath.Join(s.home, rel) }
	return []string{h("state"), h("sessions"), h(credentialsFile),
		h(registrationJournalFile), h(claimRecordFile),
		h(setupConfigFile), h(preferFile), h(stateAsidePrefix + "-20260101")}
}

func snapshotState(t *testing.T, s *setupSandbox) map[string]map[string]fileSig {
	t.Helper()
	out := map[string]map[string]fileSig{}
	for _, p := range participantState(s) {
		if lexists(p) {
			out[p] = snapshotTree(t, p)
		}
	}
	return out
}

// setConfigKey rewrites the one line of setup's config that sets key.
func setConfigKey(t *testing.T, s *setupSandbox, key, value string) {
	t.Helper()
	cfg, err := os.ReadFile(s.cfgPath())
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=.*$`)
	if len(line.FindAll(cfg, -1)) != 1 {
		t.Fatalf("setup's config must set %s exactly once:\n%s", key, cfg)
	}
	writeFileT(t, s.cfgPath(), line.ReplaceAllLiteralString(string(cfg), fmt.Sprintf("%s = %q", key, value)))
	if key == "sessions_dir" {
		if c, _, err := loadConfig(s.cfgPath(), s.getenv); err != nil || c.Search.SessionsDir != value {
			t.Fatalf("the fixture config must name the outside sessions_dir: %v", err)
		}
	}
}

// withoutLocks drops the empty lock files a lifecycle exclusion creates in
// order to hold them: coordination, not participant state. Every other byte
// of a snapshot is compared.
func withoutLocks(snap map[string]fileSig) map[string]fileSig {
	out := map[string]fileSig{}
	for p, sig := range snap {
		if strings.HasSuffix(p, ".lock") && sig.sum == emptySum {
			continue
		}
		out[p] = sig
	}
	return out
}

var emptySum = sha256.Sum256(nil)

// snapshotHeld is snapshotTree for a sandbox in which the test itself holds a
// lock. Windows refuses to open a file another handle holds with share mode 0
// (tryLockFile's lock), so a lock file is never opened: its existence and mode
// are recorded from Lstat, and an empty one — every lock file — gets the empty
// file's digest, which is what withoutLocks compares. Every other file is
// read and compared as snapshotTree does.
func snapshotHeld(t *testing.T, root string) map[string]fileSig {
	t.Helper()
	out := map[string]fileSig{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		sig := fileSig{mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			sig.link, _ = os.Readlink(path)
		case info.IsDir():
			sig.dir = true
		case strings.HasSuffix(path, ".lock") && info.Size() == 0:
			sig.sum = emptySum
		case strings.HasSuffix(path, ".lock"):
			sig.link = fmt.Sprintf("<lock file of %d bytes, not opened>", info.Size())
		default:
			b, err := os.ReadFile(path) // #nosec G304 G122 -- walking this test's own sandbox
			if err != nil {
				return err
			}
			sig.sum = sha256.Sum256(b)
		}
		out[path] = sig
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fileContains(t *testing.T, path, needle string) bool {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- sandbox paths
	return err == nil && bytes.Contains(b, []byte(needle))
}

// ── default uninstall ───────────────────────────────────────────────────

func TestUninstallRemovesEveryTargetEvenWhenNoLongerDetected(t *testing.T) {
	s := installed(t)
	paths := s.paths()
	for _, p := range []string{paths.claudeSkill, paths.codexSkill, paths.piSkill, paths.piExtension} {
		if !lexists(p) {
			t.Fatalf("setup did not install %s", p)
		}
	}
	code, out, errOut := s.uninstall(t, nil, false, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall exited %d\n%s\n%s", code, out, errOut)
	}
	for _, p := range []string{filepath.Dir(paths.claudeSkill), filepath.Dir(paths.codexSkill), filepath.Dir(paths.piSkill), paths.piExtension} {
		if lexists(p) {
			t.Errorf("uninstall left %s although the target is no longer detected", p)
		}
	}
	if fileContains(t, paths.claudeSettings, s.exe) {
		t.Error("uninstall left Claude Code hooks that run this installation's binary")
	}
	if fileContains(t, paths.codexConfig, agentsMarkerBegin) {
		t.Error("uninstall left the Codex sandbox block")
	}
}

// uninstallIntegrationTarget is a target of the kind nothing installs through yet.
type uninstallIntegrationTarget struct{ file string }

func (uninstallIntegrationTarget) ID() string       { return "fake-integration" }
func (uninstallIntegrationTarget) Label() string    { return "Fake integration" }
func (uninstallIntegrationTarget) Kind() targetKind { return targetIntegration }
func (uninstallIntegrationTarget) Detect(agentOps, agentPaths, func(string) string) string {
	return ""
}
func (uninstallIntegrationTarget) PlanInstall(agentOps, agentPaths, binEntry, func(string) string, *agentPlan) {
}
func (f uninstallIntegrationTarget) PlanUninstall(ops agentOps, _ agentPaths, _ binEntry, _ func(string) string, p *agentPlan) {
	if pathExists(ops, f.file) {
		planRemove(p, f.Label(), f.file)
	}
}
func (uninstallIntegrationTarget) Status(agentOps, agentPaths, binEntry) targetStatus {
	return targetStatus{}
}

func TestUninstallReachesATargetOfEveryKind(t *testing.T) {
	s := installed(t)
	file := filepath.Join(s.userHome, ".fake-integration", "jevlin.conf")
	// Naming the installation is what every artifact this client writes does
	// from H5 on (TestEveryArtifactNamesTheInstallationThatWroteIt); an
	// artifact that names none cannot be attributed and is left alone, so a
	// fixture that left it out would be testing the wrong branch.
	writeFileT(t, file, "installed by an integration target\nINSTALL_CONFIG = "+strconv.Quote(s.cfgPath())+"\n")
	d, out, errOut := s.uninstallDeps(nil, false)
	d.targets = append(append([]installTarget{}, installTargets...), uninstallIntegrationTarget{file: file})
	if code := uninstallMain(d, []string{"-yes"}); code != exitOK {
		t.Fatalf("uninstall exited %d\n%s\n%s", code, out, errOut)
	}
	if lexists(file) {
		t.Error("uninstall must run PlanUninstall for every registered target of every kind, not only hosts")
	}
}

func TestDefaultUninstallPreservesEveryParticipantByte(t *testing.T) {
	s := installed(t)
	before := snapshotState(t, s)
	code, out, errOut := s.uninstall(t, nil, false, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall exited %d\n%s\n%s", code, out, errOut)
	}
	if after := snapshotState(t, s); !reflect.DeepEqual(before, after) {
		t.Error("a default uninstall changed participant state (identity, key, config, preferences, or what an earlier version left)")
	}
	if !lexists(s.exe) {
		t.Error("a default uninstall must not remove the binary")
	}
	for _, want := range []string{"Your participant state remains at " + s.home, "-config " + s.cfgPath(), "setup -home " + s.home, "would register this machine anew"} {
		if !strings.Contains(out, want) {
			t.Errorf("the closing message must say %q:\n%s", want, out)
		}
	}
	// The order of those two is the whole point. setup -home is the way
	// back; connect on its own is the thing not to do. The old wording put
	// the warning last, where it read as the next step.
	if i, j := strings.Index(out, "To keep using this installation"), strings.Index(out, "Do not run `jevlin connect` on its own"); i < 0 || j < 0 || j < i {
		t.Errorf("the closing message must offer setup -home before it warns off connect (at %d and %d):\n%s", i, j, out)
	}
	if runtime.GOOS == "windows" {
		if pathHasEntry(s.userEnv.values["Path"], filepath.Join(s.home, "bin")) || s.userEnv.values["JEVLIN_CONFIG"] != "" {
			t.Errorf("the user environment setup set was not reverted: %v", s.userEnv.values)
		}
	} else if fileContains(t, s.profilePath(), profileMarkerStart) {
		t.Error("the profile block for this installation was not removed")
	}
}

func TestUninstallWithoutATerminalNeedsYesAndDryRunChangesNothing(t *testing.T) {
	s := installed(t)
	before := snapshotTree(t, s.root)
	if code, _, errOut := s.uninstall(t, nil, false); code != exitUsage || !strings.Contains(errOut, "pass -yes") {
		t.Errorf("uninstall without a terminal or -yes must refuse: exit %d, %q", code, errOut)
	}
	if code, out, errOut := s.uninstall(t, nil, false, "-dry-run", "-binary", "-purge-state"); code != exitUsage && code != exitOK {
		t.Errorf("dry run: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := s.uninstall(t, nil, false, "-dry-run"); code != exitOK || !strings.Contains(out, "nothing was changed") {
		t.Errorf("a dry run exits 0 and says it changed nothing: %d\n%s", code, out)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, s.root)) {
		t.Error("a refused or dry-run uninstall changed files")
	}
}

func TestUninstallLeavesAnotherInstallationsIntegrations(t *testing.T) {
	s := installed(t)
	paths := s.paths()
	other := s.root
	d, out, errOut := s.uninstallDeps(nil, false)
	d.executable = func() (string, error) { return filepath.Join(other, "elsewhere", "jevlin"), nil }
	if code := uninstallMain(d, []string{"-yes", "-home", filepath.Join(other, "another-home")}); code != exitOK {
		t.Fatalf("uninstall exited %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(paths.claudeSkill) || !lexists(paths.codexSkill) {
		t.Error("skills that run another installation's binary must be left in place")
	}
	if !strings.Contains(out.String(), "left in place; it belongs to the installation configured by "+s.cfgPath()) {
		t.Errorf("uninstall must report what it left and why:\n%s", out.String())
	}
}

// ── the shell profile ───────────────────────────────────────────────────

func TestRemoveProfileBlock(t *testing.T) {
	block := profileBlock([]string{"export JEVLIN_CONFIG='/h/jevlin.toml'"})
	for name, tc := range map[string]struct {
		in, want string
		found    bool
		bad      bool
	}{
		"none":        {in: "alias ll=ls\n", want: "alias ll=ls\n"},
		"one":         {in: "a\n" + block + "b\n", want: "a\nb\n", found: true},
		"crlf":        {in: "a\r\n" + strings.ReplaceAll(block, "\n", "\r\n") + "b\r\n", want: "a\r\nb\r\n", found: true},
		"no newline":  {in: "a\n" + strings.TrimSuffix(block, "\n"), want: "a\n", found: true},
		"two blocks":  {in: block + block, bad: true},
		"crossed":     {in: profileMarkerEnd + "\n" + profileMarkerStart + "\n", bad: true},
		"start alone": {in: "a\n" + profileMarkerStart + "\nb\n", bad: true},
	} {
		next, lines, found, err := removeProfileBlock([]byte(tc.in))
		switch {
		case tc.bad:
			if !errors.Is(err, errProfileMalformed) {
				t.Errorf("%s: want errProfileMalformed, got %v", name, err)
			}
		case err != nil || found != tc.found || string(next) != tc.want:
			t.Errorf("%s: got %q found=%v err=%v, want %q found=%v", name, next, found, err, tc.want, tc.found)
		case found && !profileBlockNamesConfig(lines, "/h/jevlin.toml"):
			t.Errorf("%s: the removed block's lines must be returned", name)
		}
	}
}

func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the shell profile is POSIX's; Windows reverts the user environment")
	}
}

func TestUninstallProfileBlocks(t *testing.T) {
	posixOnly(t)
	s := newSetupSandbox(t)
	writeFileT(t, s.cfgPath(), "")
	ours := profileBlock([]string{"export JEVLIN_CONFIG=" + shellQuote(s.cfgPath())})
	unquoted := profileBlock([]string{"export JEVLIN_CONFIG=" + s.cfgPath()})
	theirs := profileBlock([]string{"export JEVLIN_CONFIG=" + shellQuote("/some/other/jevlin.toml")})

	cases := []struct {
		name, zshrc, bashrc string
		wantZ, wantB        string
	}{
		{"ours, unrelated bytes kept", "export A=1\n" + ours + "# tail\n", "", "export A=1\n# tail\n", ""},
		{"an unquoted block is not one setup wrote, and is left", unquoted, "", unquoted, ""},
		{"the shell changed since setup: .bashrc too", "", "x\n" + ours, "", "x\n"},
		{"another installation's block is left", theirs, "", theirs, ""},
		{"malformed markers are left untouched", ours + ours, "", ours + ours, ""},
	}
	for _, tc := range cases {
		for name, body := range map[string]string{".zshrc": tc.zshrc, ".bashrc": tc.bashrc} {
			p := filepath.Join(s.userHome, name)
			_ = os.Remove(p)
			if body != "" {
				writeFileT(t, p, body)
				if err := os.Chmod(p, 0o640); err != nil { // #nosec G302 -- a profile mode the test must see preserved, in its own sandbox
					t.Fatal(err)
				}
			}
		}
		code, out, errOut := s.uninstall(t, nil, false, "-yes")
		if code != exitOK {
			t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, out, errOut)
		}
		for name, want := range map[string]string{".zshrc": tc.wantZ, ".bashrc": tc.wantB} {
			p := filepath.Join(s.userHome, name)
			got, err := os.ReadFile(p) // #nosec G304 -- sandbox profile
			if want == "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if string(got) != want {
				t.Errorf("%s: %s = %q, want %q", tc.name, name, got, want)
			}
			if info, err := os.Stat(p); err == nil && info.Mode().Perm() != 0o640 {
				t.Errorf("%s: %s mode changed to %v", tc.name, name, info.Mode().Perm())
			}
		}
		if strings.Contains(tc.name, "malformed") && !strings.Contains(out, "by hand") {
			t.Errorf("%s: manual cleanup instructions must be printed:\n%s", tc.name, out)
		}
	}
}

func TestUninstallEditsASymlinkedProfileThroughTheLink(t *testing.T) {
	posixOnly(t)
	s := newSetupSandbox(t)
	target := filepath.Join(s.userHome, "dotfiles", "zshrc")
	writeFileT(t, target, "keep\n"+profileBlock([]string{"export JEVLIN_CONFIG=" + shellQuote(s.cfgPath())}))
	link := filepath.Join(s.userHome, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := s.uninstall(t, nil, false, "-yes"); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Error("the profile symlink must stay a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" { // #nosec G304 -- sandbox profile
		t.Errorf("the linked file must lose only the block: %q", b)
	}
}

// ── the Windows user environment, compare-and-revert ────────────────────

func writeEnvJournalT(t *testing.T, home string, j envJournal) string {
	t.Helper()
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, setupEnvJournalFile)
	writeFileT(t, path, string(b))
	return path
}

// windowsUninstall runs a default uninstall through the Windows branch with
// the fake user environment, on any OS.
func windowsUninstall(t *testing.T, s *setupSandbox, env userEnvironment) (int, string, string) {
	t.Helper()
	d, out, errOut := s.uninstallDeps(nil, false)
	d.windows = true
	d.userEnv = env
	d.executable = func() (string, error) { return filepath.Join(s.home, "bin", "jevlin.exe"), nil }
	code := uninstallMain(d, []string{"-yes"})
	return code, out.String(), errOut.String()
}

func TestWindowsUninstallRevertsOnlyWhatSetupStillOwns(t *testing.T) {
	binDir := func(s *setupSandbox) string { return filepath.Join(s.home, "bin") }
	cases := []struct {
		name      string
		journal   func(s *setupSandbox) envJournal
		env       func(s *setupSandbox) map[string]string
		want      func(s *setupSandbox) map[string]string
		wantGone  bool
		wantCeded bool
	}{
		{
			name: "setup added Path and created JEVLIN_CONFIG: both reverted, other entries kept in order",
			journal: func(s *setupSandbox) envJournal {
				return envJournal{Version: 1, Path: envJournalPath{Entry: binDir(s), AddedBySetup: true}, JevlinConfig: envJournalConfig{ValueSet: s.cfgPath()}}
			},
			env: func(s *setupSandbox) map[string]string {
				return map[string]string{"Path": `C:\a;` + strings.ToUpper(binDir(s)) + `\;C:\b`, "JEVLIN_CONFIG": s.cfgPath()}
			},
			want:     func(s *setupSandbox) map[string]string { return map[string]string{"Path": `C:\a;C:\b`} },
			wantGone: true,
		},
		{
			name: "the Path entry was there before setup: it stays",
			journal: func(s *setupSandbox) envJournal {
				return envJournal{Version: 1, Path: envJournalPath{Entry: binDir(s), AddedBySetup: false}, JevlinConfig: envJournalConfig{ValueSet: s.cfgPath()}}
			},
			env: func(s *setupSandbox) map[string]string {
				return map[string]string{"Path": binDir(s), "JEVLIN_CONFIG": s.cfgPath()}
			},
			want:     func(s *setupSandbox) map[string]string { return map[string]string{"Path": binDir(s)} },
			wantGone: true,
		},
		{
			name: "JEVLIN_CONFIG still setup's: the previous value is restored",
			journal: func(s *setupSandbox) envJournal {
				return envJournal{Version: 1, Path: envJournalPath{Entry: binDir(s)}, JevlinConfig: envJournalConfig{ValueSet: s.cfgPath(), PreviousPresent: true, PreviousValue: `D:\mine.toml`}}
			},
			env:      func(s *setupSandbox) map[string]string { return map[string]string{"JEVLIN_CONFIG": s.cfgPath()} },
			want:     func(s *setupSandbox) map[string]string { return map[string]string{"JEVLIN_CONFIG": `D:\mine.toml`} }, // #nosec G101 -- an environment variable name and a fixture path, no credential
			wantGone: true,
		},
		{
			name: "JEVLIN_CONFIG changed by the participant after setup: left, reported",
			journal: func(s *setupSandbox) envJournal {
				return envJournal{Version: 1, Path: envJournalPath{Entry: binDir(s)}, JevlinConfig: envJournalConfig{ValueSet: s.cfgPath(), PreviousPresent: true, PreviousValue: `D:\old.toml`}}
			},
			env: func(s *setupSandbox) map[string]string {
				return map[string]string{"JEVLIN_CONFIG": `E:\chosen-later.toml`} // #nosec G101 -- an environment variable name and a fixture path, no credential
			},
			want: func(s *setupSandbox) map[string]string {
				return map[string]string{"JEVLIN_CONFIG": `E:\chosen-later.toml`} // #nosec G101 -- an environment variable name and a fixture path, no credential
			},
			wantGone:  true,
			wantCeded: true,
		},
	}
	for _, tc := range cases {
		s := newSetupSandbox(t)
		journal := writeEnvJournalT(t, s.home, tc.journal(s))
		env := newFakeUserEnv()
		for k, v := range tc.env(s) {
			env.values[k] = v
		}
		code, out, errOut := windowsUninstall(t, s, env)
		if code != exitOK {
			t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, out, errOut)
		}
		if !reflect.DeepEqual(env.values, tc.want(s)) {
			t.Errorf("%s: environment %v, want %v", tc.name, env.values, tc.want(s))
		}
		if lexists(journal) == tc.wantGone {
			t.Errorf("%s: journal present=%v after a completed revert", tc.name, lexists(journal))
		}
		if tc.wantCeded && !strings.Contains(out, "it was changed after setup") {
			t.Errorf("%s: a participant-owned JEVLIN_CONFIG must be reported:\n%s", tc.name, out)
		}
	}
}

func TestWindowsUninstallGuessesNothingWithoutATrustedJournal(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		s := newSetupSandbox(t)
		journal := filepath.Join(s.home, setupEnvJournalFile)
		if corrupt {
			writeFileT(t, journal, "{not json")
		} else if err := os.MkdirAll(s.home, 0o700); err != nil {
			t.Fatal(err)
		}
		env := newFakeUserEnv()
		env.values["Path"] = `C:\a;` + filepath.Join(s.home, "bin")
		env.values["JEVLIN_CONFIG"] = s.cfgPath()
		want := map[string]string{"Path": env.values["Path"], "JEVLIN_CONFIG": env.values["JEVLIN_CONFIG"]}
		code, out, errOut := windowsUninstall(t, s, env)
		if code != exitOK {
			t.Fatalf("corrupt=%v: exit %d\n%s\n%s", corrupt, code, out, errOut)
		}
		if !reflect.DeepEqual(env.values, want) {
			t.Errorf("corrupt=%v: with no trusted journal the environment must not be edited: %v", corrupt, env.values)
		}
		if !strings.Contains(out, "remove "+filepath.Join(s.home, "bin")+" from your user Path") {
			t.Errorf("corrupt=%v: precise manual instructions must be printed:\n%s", corrupt, out)
		}
		if corrupt && !lexists(journal) {
			t.Error("an untrusted journal must be kept, not deleted")
		}
	}
}

type failingUserEnv struct{ *fakeUserEnv }

func (failingUserEnv) Set(string, string) error { return errors.New("registry write refused") }

func TestWindowsUninstallKeepsTheJournalWhenARevertFails(t *testing.T) {
	s := newSetupSandbox(t)
	journal := writeEnvJournalT(t, s.home, envJournal{Version: 1,
		Path:         envJournalPath{Entry: filepath.Join(s.home, "bin"), AddedBySetup: true},
		JevlinConfig: envJournalConfig{ValueSet: s.cfgPath()}})
	env := failingUserEnv{newFakeUserEnv()}
	env.values["Path"] = filepath.Join(s.home, "bin")
	code, _, errOut := windowsUninstall(t, s, env)
	if code != exitTransport {
		t.Errorf("a failed revert is a retryable failure, exit %d: %s", code, errOut)
	}
	if !lexists(journal) {
		t.Error("the journal must survive a failed revert so the next uninstall can finish it")
	}
}

// ── -binary ─────────────────────────────────────────────────────────────

func TestUninstallBinaryRemovesOnlyTheInstallationsOwnCopy(t *testing.T) {
	posixOnly(t)
	s := newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", "jevlin")
	writeFileT(t, s.exe, "the binary")
	if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	stateBefore := participantBytes(t, s)

	code, out, errOut := s.uninstall(t, nil, false, "-yes", "-binary")
	if code != exitOK {
		t.Fatalf("uninstall -binary exited %d\n%s\n%s", code, out, errOut)
	}
	for _, p := range []string{s.exe, filepath.Join(s.home, "bin")} {
		if lexists(p) {
			t.Errorf("uninstall -binary left %s", p)
		}
	}
	if !reflect.DeepEqual(stateBefore, participantBytes(t, s)) || !lexists(s.cfgPath()) || !lexists(filepath.Join(s.home, "state")) {
		t.Error("uninstall -binary must not remove or change participant state")
	}
}

func TestUninstallBinaryRefusesACopyItDoesNotOwn(t *testing.T) {
	posixOnly(t)
	cases := []struct {
		name, marker string
		layout       func(s *setupSandbox) string
		want         string
	}{
		{"npm global", "npm:global", func(s *setupSandbox) string { return s.exe }, "npm uninstall -g @jevlinsearch/jevlin"},
		{"npm local", "npm:local", func(s *setupSandbox) string { return s.exe }, "project's dependencies"},
		{"npm unknown", "npm:unknown", func(s *setupSandbox) string { return s.exe }, "package manager"},
		{"node_modules run directly", "", func(s *setupSandbox) string {
			return filepath.Join(s.root, "node_modules", "jevlin", "bin", "jevlin")
		}, "package manager"},
		{"ambiguous npm-like layout", "", func(s *setupSandbox) string {
			exe := filepath.Join(s.home, "bin", "jevlin")
			writeFileT(t, filepath.Join(s.home, "package.json"), `{"name":"@jevlinsearch/jevlin"}`)
			return exe
		}, "unclear"},
		{"a native binary that is not the installation's copy", "", func(s *setupSandbox) string { return s.exe }, "is not this installation's own binary"},
		{"an installation with no binary of its own", "", func(s *setupSandbox) string {
			_ = os.Remove(filepath.Join(s.home, "bin", "jevlin"))
			return s.exe
		}, "has no binary of its own"},
	}
	for _, tc := range cases {
		s := newSetupSandbox(t)
		writeInstallation(t, s.home, "identity", "config")
		writeFileT(t, filepath.Join(s.home, "bin", "jevlin"), "owned")
		exe := tc.layout(s)
		writeFileT(t, exe, "running")
		s.env["JEVLIN_LAUNCH"] = tc.marker
		before := snapshotTree(t, s.root)
		d, out, errOut := s.uninstallDeps(nil, false)
		d.executable = func() (string, error) { return exe, nil }
		code := uninstallMain(d, []string{"-yes", "-binary"})
		if code != exitUsage || !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%s: want a refusal naming %q, got exit %d\n%s\n%s", tc.name, tc.want, code, out.String(), errOut.String())
		}
		if !reflect.DeepEqual(before, snapshotTree(t, s.root)) {
			t.Errorf("%s: a refused -binary must change nothing", tc.name)
		}
	}
}

// withOwnBinaryAndLeftovers is a set-up installation whose binary is H's own
// copy, with a .previous, the updater's staging leftovers and a file that is
// not Jevlin's beside it.
func withOwnBinaryAndLeftovers(t *testing.T, binaryName string) (s *setupSandbox, unrelated string, leftovers []string) {
	t.Helper()
	s = newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", binaryName)
	writeFileT(t, s.exe, "the binary")
	if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	bin := filepath.Dir(s.exe)
	writeFileT(t, s.exe+".previous", "the previous binary")
	for _, name := range []string{".jevlin.candidate-123", ".jevlin.snapshot-456", ".jevlin.displaced-789"} {
		leftovers = append(leftovers, filepath.Join(bin, name))
		writeFileT(t, filepath.Join(bin, name), "staging")
	}
	unrelated = filepath.Join(bin, "not-jevlins.txt")
	writeFileT(t, unrelated, "someone else's file")
	return s, unrelated, leftovers
}

// removedAfter reports whether out says first was removed or moved aside
// before second was removed.
func removedAfter(out, first, second string) bool {
	i := strings.Index(out, "removed "+first+"\n")
	if j := strings.Index(out, "moved "+first+" aside"); i < 0 {
		i = j
	}
	k := strings.Index(out, "removed "+second+"\n")
	return i >= 0 && k > i
}

func TestUninstallBinaryRemovesPreviousAndStagingLeftoversButNothingElse(t *testing.T) {
	posixOnly(t)
	s, unrelated, leftovers := withOwnBinaryAndLeftovers(t, "jevlin")
	stateBefore := participantBytes(t, s)
	resolved, err := selfupdate.ResolveExecutable(s.exe)
	if err != nil {
		t.Fatal(err)
	}
	lock := resolved + updateLockSuffix
	code, out, errOut := s.uninstall(t, nil, false, "-yes", "-binary")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if lexists(lock) || !removedAfter(out, s.exe, lock) {
		t.Errorf("the update lock goes only after the binary has left its path:\n%s", out)
	}
	for _, p := range append(leftovers, s.exe, s.exe+".previous") {
		if lexists(p) {
			t.Errorf("-binary left %s", p)
		}
	}
	if !lexists(unrelated) {
		t.Error("-binary must not remove anything in the directory that is not Jevlin's")
	}
	if !reflect.DeepEqual(stateBefore, participantBytes(t, s)) {
		t.Error("-binary must not touch participant state")
	}
}

func TestUninstallBinaryOnWindowsMovesTheRunningBinaryAside(t *testing.T) {
	s, unrelated, leftovers := withOwnBinaryAndLeftovers(t, "jevlin.exe")
	stateBefore := participantBytes(t, s)
	d, out, errOut := s.uninstallDeps(nil, false)
	d.windows = true
	if code := uninstallMain(d, []string{"-yes", "-binary"}); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if lexists(s.exe) {
		t.Error("the owned binary must be out of its name")
	}
	var aside string
	entries, _ := os.ReadDir(filepath.Dir(s.exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".jevlin.displaced-") {
			aside = filepath.Join(filepath.Dir(s.exe), e.Name())
		}
	}
	if aside == "" {
		t.Fatalf("the binary was not moved aside: %v", entries)
	}
	if b, _ := os.ReadFile(aside); string(b) != "the binary" { // #nosec G304 -- sandbox path
		t.Errorf("the moved-aside file must be the binary, holds %q", b)
	}
	if !strings.Contains(out.String(), "moved "+s.exe+" aside to "+aside) || !strings.Contains(out.String(), "Left behind") ||
		strings.Contains(out.String(), "removed "+s.exe+"\n") {
		t.Errorf("the residual path must be reported honestly, never as removed:\n%s", out.String())
	}
	for _, p := range append(leftovers, s.exe+".previous") {
		if lexists(p) {
			t.Errorf("-binary left %s", p)
		}
	}
	if !lexists(unrelated) || !reflect.DeepEqual(stateBefore, participantBytes(t, s)) {
		t.Error("-binary on Windows touches nothing else: the unrelated file and participant state stay")
	}
	resolvedDir, _ := filepath.EvalSymlinks(filepath.Dir(s.exe))
	lock := filepath.Join(resolvedDir, filepath.Base(s.exe)) + updateLockSuffix
	if lexists(lock) || !removedAfter(out.String(), s.exe, lock) {
		t.Errorf("the update lock goes only after the binary was moved out of its path:\n%s", out.String())
	}
}

// When the binary cannot leave its path, its update lock stays too.
func TestUninstallBinaryLeavesTheUpdateLockWhenTheBinaryStays(t *testing.T) {
	posixOnly(t)
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	s, _, _ := withOwnBinaryAndLeftovers(t, "jevlin")
	resolved, err := selfupdate.ResolveExecutable(s.exe)
	if err != nil {
		t.Fatal(err)
	}
	lock := resolved + updateLockSuffix
	writeFileT(t, lock, "")
	bin := filepath.Dir(s.exe)
	if err := os.Chmod(bin, 0o500); err != nil { // #nosec G302 -- a deliberately unwritable fixture directory
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(bin, 0o700) }() // #nosec G302 -- restore for cleanup
	code, out, _ := s.uninstall(t, nil, false, "-yes", "-binary")
	if code == exitOK {
		t.Errorf("a binary that could not be removed is a failure:\n%s", out)
	}
	if !lexists(s.exe) || !lexists(lock) {
		t.Errorf("the binary stayed, so its update lock must stay: binary %v, lock %v", lexists(s.exe), lexists(lock))
	}
	// The directory is unwritable, so the lock file would survive a removal
	// attempt too; the report line is the proof the lock was kept on purpose.
	if !strings.Contains(out, "left "+lock+": the binary is still at "+s.exe) {
		t.Errorf("the kept lock must be reported as kept:\n%s", out)
	}
}

func TestUninstallBinaryRefusesAnUpgradeOfTheSameBinaryBeforeChangingAnything(t *testing.T) {
	name := "jevlin"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	s := newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", name)
	writeFileT(t, s.exe, "the binary")
	if code, out, errOut := s.run(nil, false, "-yes", "-with", "claude", "-with", "codex"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	writeFileT(t, s.exe+".previous", "the previous binary")
	for _, leftover := range []string{".jevlin.candidate-1", ".jevlin.snapshot-2", ".jevlin.displaced-3"} {
		writeFileT(t, filepath.Join(filepath.Dir(s.exe), leftover), "an upgrade's staging")
	}
	resolved, err := selfupdate.ResolveExecutable(s.exe)
	if err != nil {
		t.Fatal(err)
	}
	holdLockFile(t, resolved+updateLockSuffix)
	setForegroundWait(t, 50*time.Millisecond)

	before := snapshotHeld(t, s.root)
	envBefore := fmt.Sprint(s.userEnv.values)
	code, out, errOut := s.uninstall(t, nil, false, "-yes", "-binary")
	if code != exitTransport || !strings.Contains(errOut, "an upgrade of "+s.exe+" is running") {
		t.Errorf("a running upgrade of this binary must refuse -binary: exit %d\n%s\n%s", code, out, errOut)
	}
	if !reflect.DeepEqual(withoutLocks(before), withoutLocks(snapshotHeld(t, s.root))) {
		t.Error("the binary, .previous, every leftover, the integrations and the participant state must be byte-identical")
	}
	if fmt.Sprint(s.userEnv.values) != envBefore {
		t.Errorf("the user environment must be unchanged: %s -> %v", envBefore, s.userEnv.values)
	}
}

// ── -purge-state ────────────────────────────────────────────────────────

func TestPurgeRemovesStateAfterTheExactTypedConfirmation(t *testing.T) {
	s := installed(t)
	outside := filepath.Join(s.root, "external-sessions")
	writeFileT(t, filepath.Join(outside, "keep.json"), "{}")
	setConfigKey(t, s, "sessions_dir", outside)

	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Type the installation path to permanently remove this participant state:\n  "+s.home+"\n") {
		t.Errorf("the confirmation prompt must ask for the installation path and show it:\n%s", out)
	}
	for _, p := range participantState(s) {
		if lexists(p) {
			t.Errorf("purge left %s", p)
		}
	}
	if !lexists(filepath.Join(outside, "keep.json")) || !strings.Contains(out, outside) {
		t.Error("a configured directory outside the installation must survive and be reported")
	}
	// The gate survives a purge and the closing output names it. It is now named
	// in the same list as every other lock still there, so the search is scoped
	// to that section rather than to the whole output, which also prints the
	// full path of everything removed.
	gateIdx := strings.Index(out, leftoverHeading)
	if !lexists(lifecycleGatePath(s.home)) || gateIdx < 0 || !strings.Contains(out[gateIdx:], lifecycleGatePath(s.home)) {
		t.Error("the lifecycle gate survives a purge, and the closing output names it as safe to delete")
	}
	if !lexists(s.exe) {
		t.Error("-purge-state must not remove the binary")
	}
	if !strings.Contains(out, "revoked only at the console") {
		t.Error("the closing output must say the platform grant is revoked only at the console")
	}
}

func TestPurgeStateLeavesTheInstallationsOwnBinary(t *testing.T) {
	s := newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", "jevlin")
	writeFileT(t, s.exe, "the binary")
	if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK || lexists(filepath.Join(s.home, "state")) {
		t.Fatalf("purge: exit %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(s.exe) {
		t.Error("-purge-state must not imply -binary: the installation's own binary survives it")
	}
}

// installedWithOwnBinary is a set-up installation whose binary is at its own
// H/bin/jevlin, so a configured path can collide with the binary directory.
func installedWithOwnBinary(t *testing.T) *setupSandbox {
	t.Helper()
	s := newSetupSandbox(t)
	s.exe = filepath.Join(s.home, "bin", "jevlin")
	writeFileT(t, s.exe, "the binary")
	if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	return s
}

// purgeRefusedForConfig runs a purge the participant fully confirms, and
// checks that the configured path's conflict refused it before anything of
// the participant's or the software's changed.
func purgeRefusedForConfig(t *testing.T, s *setupSandbox, key, value string) {
	t.Helper()
	setConfigKey(t, s, key, value)
	before := snapshotHeld(t, s.root)
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if !lexists(s.exe) {
		t.Fatalf("%s = %s: the purge deleted the binary\n%s\n%s", key, value, out, errOut)
	}
	if !reflect.DeepEqual(withoutLocks(before), withoutLocks(snapshotHeld(t, s.root))) {
		t.Errorf("%s = %s: a refused purge must leave the binary and every participant byte unchanged", key, value)
	}
	if code != exitUsage || !strings.Contains(errOut, "state and program ownership cannot be separated") ||
		!strings.Contains(errOut, noParticipantChange) {
		t.Errorf("%s = %s: want the ownership refusal, got exit %d\n%s\n%s", key, value, code, out, errOut)
	}
	if strings.Contains(out, "Type ") {
		t.Errorf("%s = %s: the refusal comes before the confirmation", key, value)
	}
}

func TestPurgeRefusesConfiguredStateAtInstallationRoot(t *testing.T) {
	s := installedWithOwnBinary(t)
	purgeRefusedForConfig(t, s, "state_dir", s.home)
}

func TestPurgeRefusesConfiguredStateInsideBinaryTree(t *testing.T) {
	s := installedWithOwnBinary(t)
	purgeRefusedForConfig(t, s, "state_dir", filepath.Join(s.home, "bin"))
	s2 := installedWithOwnBinary(t)
	purgeRefusedForConfig(t, s2, "sessions_dir", filepath.Dir(s2.home))
}

func TestPurgeCannotReintroduceAProtectedEnvironmentJournal(t *testing.T) {
	s := installed(t)
	journal := writeEnvJournalT(t, s.home, envJournal{Version: 1,
		Path:         envJournalPath{Entry: filepath.Join(s.home, "bin"), AddedBySetup: true},
		JevlinConfig: envJournalConfig{ValueSet: s.cfgPath()}})
	setConfigKey(t, s, "sessions_dir", journal)
	env := failingUserEnv{newFakeUserEnv()}
	env.values["Path"] = filepath.Join(s.home, "bin")
	d, out, errOut := s.uninstallDeps(tty(s.home), true)
	d.windows = true
	d.userEnv = env
	code := uninstallMain(d, []string{"-purge-state"})
	if !lexists(journal) {
		t.Fatalf("a journal kept because its revert failed was deleted through a configured path (exit %d)\n%s\n%s", code, out, errOut)
	}
	if lexists(filepath.Join(s.home, "state")) {
		t.Error("the rest of the purge still happens")
	}
}

// The confirmation is the installation path, typed exactly. Every other
// input refuses: another text, another spelling of the same directory, an
// empty read, -yes at a terminal or without one, and no terminal at all.
func TestPurgeRefusesEveryConfirmationButTheExactOne(t *testing.T) {
	cases := []struct {
		name        string
		stdin       func(home string) io.Reader
		interactive bool
		args        []string
		wantCode    int
	}{
		{"wrong text", func(string) io.Reader { return tty("not the installation") }, true, nil, exitUsage},
		{"another spelling of the path", func(h string) io.Reader { return tty(h + string(filepath.Separator)) }, true, nil, exitUsage},
		{"EOF", func(string) io.Reader { return strings.NewReader("") }, true, nil, exitUsage},
		{"-yes at a terminal with nothing typed", func(string) io.Reader { return strings.NewReader("") }, true, []string{"-yes"}, exitUsage},
		{"-yes without a terminal", func(h string) io.Reader { return tty(h) }, false, []string{"-yes"}, exitUsage},
		{"no terminal", func(h string) io.Reader { return tty(h) }, false, nil, exitUsage},
		{"trailing space", func(h string) io.Reader { return tty(h + " ") }, true, nil, exitUsage},
	}
	for _, tc := range cases {
		s := installed(t)
		before := snapshotTree(t, s.root)
		args := append([]string{"-purge-state"}, tc.args...)
		code, out, errOut := s.uninstall(t, tc.stdin(s.home), tc.interactive, args...)
		if code != tc.wantCode {
			t.Errorf("%s: exit %d, want %d\n%s\n%s", tc.name, code, tc.wantCode, out, errOut)
		}
		if !reflect.DeepEqual(withoutLocks(before), withoutLocks(snapshotTree(t, s.root))) {
			t.Errorf("%s: an unconfirmed purge must leave every byte unchanged, integrations included", tc.name)
		}
	}
}

func TestPurgeConfirmationIsTheInstallationPath(t *testing.T) {
	s := newSetupSandbox(t)
	writeInstallation(t, s.home, "identity")
	writeFileT(t, s.cfgPath(), fmt.Sprintf("[search]\nstate_dir = %q\n", filepath.Join(s.home, "state")))
	if token, what := purgeConfirmation(s.home); token != s.home || what != "the installation path" {
		t.Errorf("the token is the canonical installation path: %q (%s)", token, what)
	}
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK || lexists(filepath.Join(s.home, "state")) {
		t.Errorf("typing the installation path confirms: exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestPurgeDryRunNeitherAsksNorChanges(t *testing.T) {
	s := installed(t)
	before := snapshotTree(t, s.root)
	code, out, errOut := s.uninstall(t, strings.NewReader(""), false, "-purge-state", "-dry-run")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, s.root)) {
		t.Error("a purge dry run changes nothing")
	}
	for _, p := range participantState(s) {
		if !strings.Contains(out, "remove "+p) {
			t.Errorf("the dry run must print the exact destructive set; missing %s:\n%s", p, out)
		}
	}
	if strings.Contains(out, "Type ") {
		t.Error("a dry run must not ask for the confirmation")
	}
}

// installedOwningItsBinary is installed(t) plus a binary of its own at
// home/bin, the way TestUninstallBinaryRemovesOnlyTheInstallationsOwnCopy
// sets one up: the running executable IS that copy, so checkBinaryOwnership
// admits -binary. Everything else matches installed(t) exactly, so the same
// fixture can drive all three uninstall modes in one table test.
func installedOwningItsBinary(t *testing.T) *setupSandbox {
	t.Helper()
	s := newSetupSandbox(t)
	name := "jevlin"
	if runtime.GOOS == "windows" {
		name = "jevlin.exe"
	}
	s.exe = filepath.Join(s.home, "bin", name)
	writeFileT(t, s.exe, "the binary")
	s.platform.claim("credits")
	s.onPath["claude"] = true
	s.onPath["codex"] = true
	if code, out, errOut := s.run(nil, false, "-yes", "-with", "claude", "-with", "codex", "-with", "pi"); code != exitOK {
		t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for rel, body := range map[string]string{
		filepath.Join("sessions", "s-1.json"): `{}`,
		preferFile:                            "builtin\n",
		filepath.Join(stateAsidePrefix+"-20260101", "x.key"): "half",
	} {
		writeFileT(t, filepath.Join(s.home, rel), body)
	}
	s.onPath = map[string]bool{} // uninstall must not depend on detection
	return s
}

// TestUninstallDryRunRemoveListingMatchesTheRealRunForEveryMode is D.2's
// literal test for uninstall: "dry-run removal list equals the real run's
// removals for plain, -binary and -purge-state." Both a dry run and the
// real run plan from the same purgeSet/uninstallTargets/binarySet code, so
// this is what catches a file the real run's own locking creates and
// removes while the dry run, which takes no locks, never lists it, without
// knowing in advance which file it is. dry and real are separate sandboxes
// (each with its own temp-directory root), so every "remove " line is
// normalized to a path relative to that sandbox's own root before the two
// listings are compared — the absolute paths themselves never match
// between two different temp directories, but the installation's own
// internal layout does.
func TestUninstallDryRunRemoveListingMatchesTheRealRunForEveryMode(t *testing.T) {
	removeLines := func(root, out string) map[string]bool {
		set := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			trimmed := strings.TrimLeft(line, " ")
			indent := len(line) - len(trimmed)
			if indent == 0 || !strings.HasPrefix(trimmed, "remove ") {
				continue
			}
			// plan()'s one narrative exception: it names the update lock's
			// path only to say when it goes, not as a removal target of its
			// own (that is "removed <path>", printed only once the binary
			// has actually left).
			if strings.Contains(trimmed, "'s update lock once the binary has left") {
				continue
			}
			p := strings.TrimPrefix(trimmed, "remove ")
			if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
				p = rel
			}
			set[p] = true
		}
		return set
	}

	cases := []struct {
		name string
		args []string
	}{
		{"plain", nil},
		{"binary", []string{"-binary"}},
		{"purge-state", []string{"-purge-state"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dry := installedOwningItsBinary(t)
			dryArgs := append(append([]string{}, tc.args...), "-dry-run")
			code, dryOut, errOut := dry.uninstall(t, strings.NewReader(""), false, dryArgs...)
			if code != exitOK {
				t.Fatalf("dry run: exit %d\n%s\n%s", code, dryOut, errOut)
			}
			dryRemoves := removeLines(dry.root, dryOut)
			if len(dryRemoves) == 0 {
				t.Fatalf("dry run %v listed nothing to remove:\n%s", tc.args, dryOut)
			}

			real := installedOwningItsBinary(t)
			var stdin io.Reader = strings.NewReader("")
			interactive := false
			realArgs := tc.args
			if tc.name == "purge-state" {
				stdin, interactive = tty(real.home), true
			} else {
				// Neither -dry-run nor -purge-state: the plain/-binary ask
				// needs an answer, which -yes gives without a terminal
				// (-purge-state's own confirmation refuses -yes outright,
				// so it is never added there).
				realArgs = append(append([]string{}, tc.args...), "-yes")
			}
			code, realOut, errOut := real.uninstall(t, stdin, interactive, realArgs...)
			if code != exitOK {
				t.Fatalf("real run: exit %d\n%s\n%s", code, realOut, errOut)
			}
			realRemoves := removeLines(real.root, realOut)

			for p := range dryRemoves {
				if !realRemoves[p] {
					t.Errorf("dry run listed %s, the real run's plan never did", p)
				}
			}
			for p := range realRemoves {
				if !dryRemoves[p] {
					t.Errorf("the real run removed %s, the dry run never listed it", p)
				}
			}
		})
	}
}

// A state directory the config puts outside the installation is the
// participant's to keep: a purge removes what is under the installation,
// names the outside directory as left, and leaves what is in it.
func TestPurgeLeavesAStateDirectoryOutsideTheInstallation(t *testing.T) {
	s := installed(t)
	outsideState := filepath.Join(s.root, "state-elsewhere")
	store, err := auth.OpenStore(outsideState)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureTraceKey(); err != nil {
		t.Fatal(err)
	}
	traceKey := auth.TraceKeyPath(outsideState)
	setConfigKey(t, s, "state_dir", outsideState)
	if c, _, err := loadConfig(s.cfgPath(), s.getenv); err != nil || c.Search.StateDir != outsideState {
		t.Fatalf("the fixture config must name the outside state_dir: %v", err)
	}
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(traceKey) {
		t.Error("a state directory outside the installation is not removed")
	}
	if !strings.Contains(out, "left:  "+outsideState+" (configured outside "+s.home) {
		t.Errorf("the plan must name the outside state directory as left:\n%s", out)
	}
}

func TestPurgeRefusesWhileAnOperationRuns(t *testing.T) {
	for _, op := range []string{"setup", "connect"} {
		s := installed(t)
		lock := map[string]string{
			"setup":   filepath.Join(s.home, setupLockFile),
			"connect": filepath.Join(s.home, "state", "connect.lock"),
		}[op]
		release := holdLockFile(t, lock)
		setForegroundWait(t, 50*time.Millisecond)
		before := snapshotHeld(t, s.root)
		code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
		release()
		if code != exitTransport || !strings.Contains(errOut, op+" is running") {
			t.Errorf("%s running: purge must refuse naming it, got exit %d\n%s\n%s", op, code, out, errOut)
		}
		if !reflect.DeepEqual(withoutLocks(before), withoutLocks(snapshotHeld(t, s.root))) {
			t.Errorf("%s running: a refused purge changes nothing", op)
		}
	}
}

func TestPurgeTargetGuard(t *testing.T) {
	s := newSetupSandbox(t)
	writeInstallation(t, s.home, "identity", "config")
	link := filepath.Join(s.root, "linked-home")
	symlinkOK := os.Symlink(s.home, link) == nil
	empty := filepath.Join(s.root, "not-an-installation")
	if err := os.MkdirAll(filepath.Join(empty, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"your home directory":           s.userHome,
		"holds no installation":         empty,
		"there is no installation":      filepath.Join(s.root, "missing"),
		"filesystem root":               string(filepath.Separator),
		"is a symlink; name the direct": link,
	}
	for want, home := range cases {
		if strings.HasPrefix(want, "is a symlink") && !symlinkOK {
			continue
		}
		before := snapshotTree(t, s.root)
		code, out, errOut := s.uninstall(t, tty(home), true, "-purge-state", "-home", home)
		if code != exitUsage || !strings.Contains(errOut, strings.TrimSuffix(want, "; name the direct")) {
			t.Errorf("-home %s: want a refusal containing %q, got exit %d\n%s\n%s", home, want, code, out, errOut)
		}
		if !reflect.DeepEqual(before, snapshotTree(t, s.root)) {
			t.Errorf("-home %s: the guard must refuse before anything changes", home)
		}
	}
	if err := checkPurgeTarget(s.home, s.userHome); err != nil {
		t.Errorf("a real installation passes the guard: %v", err)
	}
}

func TestPurgeKeepsTheEnvironmentJournalWhenItsRevertFails(t *testing.T) {
	s := installed(t)
	journal := writeEnvJournalT(t, s.home, envJournal{Version: 1,
		Path:         envJournalPath{Entry: filepath.Join(s.home, "bin"), AddedBySetup: true},
		JevlinConfig: envJournalConfig{ValueSet: s.cfgPath()}})
	env := failingUserEnv{newFakeUserEnv()}
	env.values["Path"] = filepath.Join(s.home, "bin")
	d, out, errOut := s.uninstallDeps(tty(s.home), true)
	d.windows = true
	d.userEnv = env
	code := uninstallMain(d, []string{"-purge-state"})
	if code != exitTransport {
		t.Errorf("a failed environment revert makes the purge report a failure: exit %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(journal) || !lexists(s.home) {
		t.Error("the journal must survive the purge when its revert failed, leaving the installation directory non-empty")
	}
	if lexists(filepath.Join(s.home, "state")) {
		t.Error("the rest of the purge still happens")
	}
}

func TestUninstallRefusesWhileTheGateIsHeld(t *testing.T) {
	s := installed(t)
	mustAcquireGate(t, lifecycleGatePath(s.home))
	setForegroundWait(t, 50*time.Millisecond)
	before := snapshotHeld(t, s.root)
	code, _, errOut := s.uninstall(t, nil, false, "-yes")
	if code != exitTransport || !strings.Contains(errOut, errLifecycleBusy.Error()) {
		t.Errorf("uninstall under a held gate refuses: exit %d, %q", code, errOut)
	}
	if !reflect.DeepEqual(before, snapshotHeld(t, s.root)) {
		t.Error("uninstall under a held gate changes nothing")
	}
}

func TestUninstallCustomHome(t *testing.T) {
	s := newSetupSandbox(t)
	custom := filepath.Join(s.root, "opt", "me", "jevlin")
	s.home = custom
	s.env["JEVLIN_HOME"] = ""
	if code, out, errOut := s.run(nil, false, "-yes", "-home", custom); code != exitOK {
		t.Fatalf("setup -home exited %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut := s.uninstall(t, tty(custom), true, "-purge-state", "-home", custom)
	if code != exitOK || lexists(filepath.Join(custom, "state")) {
		t.Fatalf("purge of a custom home: exit %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(custom + lifecycleGateSuffix) {
		t.Error("a custom home is coordinated through its own sibling gate, which survives")
	}
}

func TestLaunchClassifier(t *testing.T) {
	root := t.TempDir()
	layout := filepath.Join(root, "pkg")
	writeFileT(t, filepath.Join(layout, "package.json"), `{"name":"@jevlinsearch/jevlin"}`)
	writeFileT(t, filepath.Join(layout, "install.js"), "")
	writeFileT(t, filepath.Join(layout, "bin", "jevlin.js"), "")
	partial := filepath.Join(root, "partial")
	writeFileT(t, filepath.Join(partial, "install.js"), "")
	for _, tc := range []struct {
		exe, marker string
		want        launchKind
	}{
		{filepath.Join(root, "home", "bin", "jevlin"), "", launchNative},
		{filepath.Join(root, "home", "bin", "jevlin"), "npm:global", launchNPMGlobal},
		{filepath.Join(root, "home", "bin", "jevlin"), "npm:local", launchNPMLocal},
		{filepath.Join(root, "home", "bin", "jevlin"), "npm:unknown", launchNPMUnknown},
		{filepath.Join(root, "home", "bin", "jevlin"), "npm:something-new", launchNPMUnknown},
		{filepath.Join(root, "_npx", "x", "jevlin"), "npm:global", launchNPMCache},
		{filepath.Join(root, "node_modules", "jevlin", "bin", "jevlin"), "", launchNPMDirect},
		{filepath.Join(layout, "bin", "jevlin"), "", launchNPMLayout},
		{filepath.Join(partial, "bin", "jevlin"), "", launchAmbiguous},
	} {
		if got := classifyLaunch(tc.exe, tc.marker); got != tc.want {
			t.Errorf("classifyLaunch(%s, %q) = %d, want %d", tc.exe, tc.marker, got, tc.want)
		}
	}
}

// uninstallRefusingTarget refuses the same thing on every plan, as the
// Codex target does over a block it must leave.
type uninstallRefusingTarget struct{ uninstallIntegrationTarget }

func (uninstallRefusingTarget) PlanUninstall(_ agentOps, _ agentPaths, _ binEntry, _ func(string) string, p *agentPlan) {
	p.refused = append(p.refused, "Fake integration: left the one thing it cannot remove")
}

// A refusal is about a file, not a binary: the top-level uninstall plans
// every target once per binary candidate, and printed and counted each
// refusal once per candidate ("finished with 2 problem(s)" for one).
func TestATargetRefusalIsReportedOnce(t *testing.T) {
	s := installed(t)
	d, out, errOut := s.uninstallDeps(nil, false)
	d.targets = append(append([]installTarget{}, installTargets...), uninstallRefusingTarget{})
	code := uninstallMain(d, []string{"-yes"})
	if code == exitOK || !strings.Contains(out.String(), "finished with 1 problem(s)") {
		t.Fatalf("uninstall exited %d, or did not count one problem\n%s\n%s", code, out, errOut)
	}
	if n := strings.Count(out.String()+errOut.String(), "left the one thing it cannot remove"); n != 2 {
		t.Errorf("the refusal is reported %d times, want twice (the plan and the failure)\n%s\n%s", n, out, errOut)
	}
}
