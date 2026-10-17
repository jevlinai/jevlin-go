package main

// The records of the daily release check that wait for a person, and the
// limits on what the check may do without one. Each drives the real
// commands: upgrade -auto as the hook starts it, upgrade and -rollback as a
// person runs them, and status's and doctor's own readers.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

// factsFor is what status and doctor read about the fixture's binary.
func factsFor(t *testing.T, f *upgradeFixture, cfgPath string) updateFacts {
	t.Helper()
	cfg, _, err := loadConfig(cfgPath, envOf(f.env))
	if err != nil {
		t.Fatal(err)
	}
	return updateFactsFor(cfg, f.exe, nil, envOf(f.env))
}

func upToDate(t *testing.T, u updateFacts, now time.Time) doctorCheck {
	t.Helper()
	d := healthyFacts()
	d.Now, d.Update = now, u
	for _, c := range assembleDoctor(d) {
		if c.Name == "up to date" {
			return c
		}
	}
	t.Fatal("no `up to date` check")
	return doctorCheck{}
}

// A rollback holds the release it left. The next day's check finds it still
// the latest and leaves the rolled-back binary alone, status says why, and a
// person's own `jevlin upgrade` lifts the hold. Before, the stamp of the
// automatic upgrade read as the rolled-back copy's own ("upgraded to 0.3.1"),
// and the next check installed 0.3.1 again.
func TestARollbackHoldsTheReleaseItLeft(t *testing.T) {
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.1" {
		t.Fatalf("the automatic upgrade: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	f.build = "0.3.1"
	if code, out, errOut := f.run("-rollback"); code != exitOK || f.content(f.exe) != "0.3.0" {
		t.Fatalf("the rollback: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	st := readStampT(t, stamp)
	if st.Outcome != updateRolledBack || st.Running != "0.3.0" || st.Held != "0.3.1" {
		t.Fatalf("the rollback's record %+v", st)
	}
	withBuild(t, "0.3.0")
	if line := updateLineText(factsFor(t, f, cfgPath), time.Now()); !strings.HasPrefix(line, "rolled back from 0.3.1 to 0.3.0") {
		t.Errorf("status on the rolled-back copy: %q", line)
	}

	// A day on, the check runs, finds 0.3.1 still the latest, and keeps it.
	st.CheckedAt = st.CheckedAt.Add(-25 * time.Hour)
	if err := writeUpdateStamp(stamp, st); err != nil {
		t.Fatal(err)
	}
	f.build = "0.3.0"
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK {
		t.Fatalf("the next day's check: exit %d\n%s\n%s", code, out, errOut)
	}
	if f.content(f.exe) != "0.3.0" {
		t.Fatalf("the check undid the rollback: binary %s", f.content(f.exe))
	}
	st = readStampT(t, stamp)
	if st.Outcome != updateAvailable || st.Latest != "0.3.1" || st.Held != "0.3.1" {
		t.Errorf("the next day's record %+v", st)
	}
	if c := upToDate(t, factsFor(t, f, cfgPath), time.Now()); c.Verdict != verdictOK || !strings.Contains(c.Detail, "rolled back from 0.3.1") {
		t.Errorf("doctor: %s %q", c.Verdict, c.Detail)
	}

	// The person's own upgrade lifts the hold.
	if code, out, errOut := f.run(); code != exitOK || f.content(f.exe) != "0.3.1" {
		t.Fatalf("the person's upgrade: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	if st := readStampT(t, stamp); st.Outcome != updateUpgraded || st.Held != "" || st.Latest != "0.3.1" {
		t.Errorf("after the person's upgrade %+v", st)
	}
}

// A release newer than the one held is installed as usual, and the hold
// goes with it.
func TestAHeldReleaseDoesNotHoldANewerOne(t *testing.T) {
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.2", map[string]string{"0.3.2": "0.3.2\n"}), "")
	bin := stampBinary(f.exe)
	if err := writeUpdateStamp(stamp, updateStamp{CheckedAt: time.Now().Add(-25 * time.Hour), Binary: bin, Running: "0.3.0", Held: "0.3.1", Outcome: updateRolledBack}); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.2" {
		t.Fatalf("exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	if st := readStampT(t, stamp); st.Outcome != updateUpgraded || st.Held != "" {
		t.Errorf("stamp %+v", st)
	}
}

// readOnlyOnCanonicalRun fails the post-install validation of the canonical
// path and, at that moment, makes the binary's directory unwritable, so the
// snapshot cannot be renamed back: manual_intervention, with the candidate
// left at the path and the displaced binary in a snapshot beside it.
type readOnlyOnCanonicalRun struct {
	f   *upgradeFixture
	dir string
}

func (r *readOnlyOnCanonicalRun) Run(ctx context.Context, path string, args, env []string) ([]byte, []byte, error) {
	if path == r.f.exe {
		if err := os.Chmod(r.dir, 0o500); err != nil { // #nosec G302 -- the test's own directory
			r.f.t.Fatal(err)
		}
		return nil, nil, errors.New("injected: the installed binary timed out")
	}
	return r.f.runner.Run(ctx, path, args, env)
}

// A replacement that could not put the binary back leaves the new version
// at the path. The record is the path's, so status and doctor show it from
// that binary, with the copies that survive; doctor no longer offers
// -rollback, which would install the older .previous; and no check
// overwrites it until a person's upgrade succeeds. Before, the binary at the
// path read the record as another copy's, doctor said OK, and the next check
// wrote "current" over it.
func TestAFailedRestoreWaitsForAPerson(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	f.write(selfupdate.PreviousPath(f.exe), "0.2.9")
	binDir := filepath.Dir(f.exe)
	f.runnerOverride = &readOnlyOnCanonicalRun{f: f, dir: binDir}
	t.Cleanup(func() { _ = os.Chmod(binDir, 0o700) }) // #nosec G302 -- the test's own directory
	_, _, _ = f.runAuto(cfgPath)
	_ = os.Chmod(binDir, 0o700) // #nosec G302 -- the test's own directory
	f.runnerOverride = nil
	st := readStampT(t, stamp)
	if !stampNeedsAPerson(st) || f.content(f.exe) != "0.3.1" {
		t.Fatalf("setup: stamp %+v, binary %s", st, f.content(f.exe))
	}
	var snapshot string
	left, _ := selfupdate.StagingLeftovers(binDir)
	for _, p := range left {
		if strings.Contains(p, ".jevlin.snapshot-") {
			snapshot = p
		}
	}

	withBuild(t, "0.3.1")
	facts := factsFor(t, f, cfgPath)
	if line := updateLineText(facts, time.Now()); !strings.Contains(line, "could not put the binary back") || snapshot == "" || !strings.Contains(line, snapshot) {
		t.Errorf("status from the binary at the path: %q (snapshot %s)", line, snapshot)
	}
	c := upToDate(t, facts, time.Now())
	if c.Verdict != verdictNo || c.Fix != manualInterventionFix || strings.Contains(c.Fix, "-rollback") {
		t.Errorf("doctor: %s %q fix %q", c.Verdict, c.Detail, c.Fix)
	}

	// No check overwrites it, the next day or after.
	cfg := mustLoadConfig(t, cfgPath)
	if updateCheckDue(readUpdateStampFile, updateRoot(cfg.Miner), config.UpdateApply, "0.3.1", stampBinary(f.exe), time.Now().Add(72*time.Hour), func() bool { return true }) {
		t.Error("a check is due over a record that waits for a person")
	}
	f.build = "0.3.1"
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || !stampNeedsAPerson(readStampT(t, stamp)) {
		t.Fatalf("a check ran over it: exit %d\n%s\n%s", code, out, errOut)
	}

	// A person's upgrade that answers from the binary at the path ends it.
	if code, out, errOut := f.run(); code != exitOK {
		t.Fatalf("the person's upgrade: exit %d\n%s\n%s", code, out, errOut)
	}
	if st := readStampT(t, stamp); st.Outcome != updateCurrent || st.Running != "0.3.1" {
		t.Errorf("after the person's upgrade %+v", st)
	}
}

// A binary another installation shares is that installation's to update:
// a check started under this installation's config never replaces it,
// whatever this config's mode says. Before, B's check replaced A's binary
// although A's config said mode = "off".
func TestACheckNeverReplacesAnotherInstallationsBinary(t *testing.T) {
	f, _, _ := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "[update]\nmode = \"off\"\n")
	other := filepath.Join(filepath.Dir(f.home), "other-installation")
	if err := os.MkdirAll(filepath.Join(other, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	otherCfg := filepath.Join(other, setupConfigFile)
	writeFileT(t, otherCfg, "[mining]\nstate_dir = \""+filepath.ToSlash(filepath.Join(other, "state"))+"\"\n[update]\nmode = \"apply\"\n")
	code, out, errOut := f.runAuto(otherCfg)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if f.content(f.exe) != "0.3.0" {
		t.Fatalf("another installation's check replaced this one's binary: %s", f.content(f.exe))
	}
	if st := readStampT(t, updateStampPath(other)); st.Outcome != updateAvailable || !strings.Contains(errOut, "another installation") {
		t.Errorf("stamp %+v\n%s", st, errOut)
	}
}

// An automatic upgrade whose agent refresh failed says so in the stamp,
// which status and doctor read: the child's stderr goes nowhere.
func TestAnAutomaticUpgradeRecordsAFailedRefresh(t *testing.T) {
	m := newRerenderMachine(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"})
	if err := os.MkdirAll(filepath.Join(m.home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, m.cfg, "[mining]\nstate_dir = \""+filepath.ToSlash(filepath.Join(m.home, "state"))+"\"\n[update]\nmode = \"apply\"\n")
	m.runner.fail = true
	if code, out, errOut := m.run("-auto", "-config", m.cfg); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	st := readStampT(t, updateStampPath(m.home))
	if st.Outcome != updateUpgraded || st.Refresh != refreshFailed {
		t.Fatalf("stamp %+v", st)
	}
	u := updateFacts{Mode: config.UpdateApply, Build: "0.3.1", Binary: st.Binary, Present: true, Stamp: st, Fix: "jevlin upgrade"}
	if line := updateLineText(u, time.Now()); !strings.Contains(line, "could not be refreshed") {
		t.Errorf("status: %q", line)
	}
	if c := upToDate(t, u, time.Now()); c.Verdict != verdictNo || c.Fix != "jevlin agents install, then restart open agents" {
		t.Errorf("doctor: %s %q fix %q", c.Verdict, c.Detail, c.Fix)
	}
}

type blockingSource struct {
	called  chan struct{}
	release chan struct{}
}

func (b *blockingSource) Release(ctx context.Context, _ *selfupdate.Version) (selfupdate.ReleaseInfo, error) {
	close(b.called)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return selfupdate.ReleaseInfo{}, errors.New("injected: the lookup ended")
}

func (b *blockingSource) DownloadAssets(context.Context, selfupdate.ReleaseInfo, []selfupdate.AssetRequirement) (map[string][]byte, error) {
	return nil, errors.New("unused")
}

// A lookup holds the check's own lock and nothing of the installation's, so
// the flush the same hook started, and a person's connect or setup, are
// admitted while it waits on the network. Before, every check of a native
// copy held the installation's gate for the whole request, up to two
// minutes, even under mode = "notify", which never replaces.
func TestALookupHoldsNothingOfTheInstallation(t *testing.T) {
	f, cfgPath, _ := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "[update]\nmode = \"notify\"\n")
	src := &blockingSource{called: make(chan struct{}), release: make(chan struct{})}
	var out, errOut strings.Builder
	d := upgradeDeps{
		stdout: &out, stderr: &errOut, getenv: envOf(f.env), userHome: filepath.Dir(f.home),
		executable: func() (string, error) { return f.exe, nil },
		build:      func() string { return f.build },
		source:     func() selfupdate.ReleaseSource { return src },
		runner:     f.runner, goos: "linux", goarch: "amd64", operationTimeout: f.timeout,
		agents: f.agents, environ: func() []string { return nil },
	}
	done := make(chan int)
	go func() { done <- upgradeMain(d, []string{"-auto", "-config", cfgPath}) }()
	<-src.called
	gatePath, err := configGatePath(cfgPath, envOf(f.env))
	if err != nil {
		t.Fatal(err)
	}
	admitted, detachedErr := admitOrdinary(gatePath, admitDetached)
	if admitted != nil {
		admitted.release()
	}
	close(src.release)
	<-done
	if detachedErr != nil {
		t.Errorf("a flush was refused while a lookup ran: %v", detachedErr)
	}
}

// A development build has nothing to compare, and learns so before taking
// any lock beside the binary.
func TestADevelopmentBuildTakesNoLockBesideTheBinary(t *testing.T) {
	f, cfgPath, _ := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	f.build = "dev (abc123def456+dirty)"
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if lexists(f.exe + updateLockSuffix) {
		t.Errorf("a development build created %s", f.exe+updateLockSuffix)
	}
}

// A check that cannot claim its stamp makes no request: without a stamp to
// postpone it, every session would check again. Before, a stamp that could
// not be written cost nothing but a note, and the lookup went ahead.
func TestACheckThatCannotWriteItsStampMakesNoRequest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	if err := os.Chmod(f.home, 0o500); err != nil { // #nosec G302 -- the test's own directory
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.home, 0o700) }) // #nosec G302 -- the test's own directory
	code, out, errOut := f.runAuto(cfgPath)
	_ = os.Chmod(f.home, 0o700) // #nosec G302 -- the test's own directory
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if len(f.lr.requests) != 0 || f.content(f.exe) != "0.3.0" {
		t.Errorf("requests %v, binary %s: a check with no stamp reached the network", f.lr.requests, f.content(f.exe))
	}
	if lexists(stamp) {
		t.Errorf("a stamp appeared in a directory that could not be written")
	}
}

// With no [update] key, the check only tells: a native copy is not replaced
// until the participant writes mode = "apply". Releases are not signed yet,
// and apply would replace the binary unattended on the strength of a
// checksums.txt from the same release.
func TestTheDefaultModeOnlyTells(t *testing.T) {
	f := newUpgradeFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}))
	f.env["JEVLIN_HOME"] = f.home
	stateDir := filepath.Join(f.home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(f.home, setupConfigFile)
	writeFileT(t, cfgPath, "[mining]\nstate_dir = \""+filepath.ToSlash(stateDir)+"\"\n")
	if mode := mustLoadConfig(t, cfgPath).Update.Mode; mode != config.UpdateNotify {
		t.Fatalf("the default mode is %q, want notify", mode)
	}
	code, out, errOut := f.runAuto(cfgPath)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if f.content(f.exe) != "0.3.0" {
		t.Fatalf("the default mode replaced the binary: %s", f.content(f.exe))
	}
	if st := readStampT(t, updateStampPath(f.home)); st.Outcome != updateAvailable || st.Latest != "0.3.1" || st.Mode != config.UpdateNotify {
		t.Errorf("stamp %+v", st)
	}
}
