package main

// The daily release check (docs/guide.md "Upgrading").
//
// Every hook at a session's start and end, on every host, reads one stamp,
// update_check.json beside credentials.json, and when no check has run in
// the last day starts `jevlin upgrade -auto` as a detached child. The hook
// reads one bounded
// file and starts at most one process; it contacts nothing. The child is the
// one process a day that reaches the compiled-in GitHub release origin, with
// no credential: the self-updater's own path, invariant 5's one exception,
// and no new destination. Under [update] mode = "apply", the default, a
// native copy replaces itself through the same verified transaction
// `jevlin upgrade` runs (invariant 17). Under "notify", or for a copy npm
// installed, which is never replaced, it only records that a newer release
// exists, which `jevlin status` and `jevlin doctor` then say.
//
// Hooks run outside an agent's sandbox, which is what lets the child reach a
// host Codex's permission profile does not list (invariant 1): the search
// path, and the processes a search spawns, are not touched. Not every hook,
// though: a host started as a shell command inside Codex's sandbox (claude
// -p, opencode run, pi) runs its hooks there, with Codex's loopback proxy as
// its only route out, and a check started from it would ask that proxy for
// GitHub, a host the profile does not list, failing whatever Codex command
// is running. A hook whose environment says so starts no check
// (hookInsideASandbox).
//
// The stamp and its lock live in the installation's own directory, beside
// credentials.json, and never in the state directory. A sandboxed command
// can write the state directory (invariant 19), and the stamp is a record
// this client acts on and shows: whether a check runs, whether a release is
// held after a rollback, which copies survive a failed replacement. In the
// state directory a planted directory at its name made every hook a
// request, a hard link at the lock's name stopped every check, and a forged
// failure made doctor advise a rollback. It is still read bounded, and every
// word in it is held on load to a token this client writes.
//
// A stamp is about one binary, the resolved path it names, and two records
// on it outlast a day because a person has to act on them. A rollback holds
// the release it rolled back from: `jevlin upgrade -rollback` writes it, and
// the check never installs that release, or an older one, again; a newer one
// it does, and `jevlin upgrade` by hand lifts the hold. A replacement that
// could not put the binary back (manual_intervention) is never overwritten by
// the check: status and doctor show it, with the copies that survive beside
// the binary, until a person's upgrade or rollback replaces the record.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
	"github.com/jevlinai/jevlin-go/internal/termtext"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const (
	updateStampFile = "update_check.json"
	// updateLockFile serializes checks that share a state directory, held
	// in one attempt: two hooks that both found the stamp stale make one
	// request. A native apply holds the upgrade's own locks as well.
	updateLockFile = "update_check.lock"
	// updateCheckInterval is how often a check runs: a hook starts one when
	// the stamp is at least this old.
	updateCheckInterval = 24 * time.Hour
	// updateStaleAfter is when doctor stops believing the hooks are running
	// at all: three missed days.
	updateStaleAfter = 3 * updateCheckInterval
	// updateStartedGrace is how long a "started" stamp is read as a check
	// still running. The operation itself ends by selfupdate.OperationTimeout.
	updateStartedGrace = 10 * time.Minute
)

// updateOutcome is what the last check concluded, as a closed token.
type updateOutcome string

const (
	updateStarted    updateOutcome = "started"     // written before the network is touched
	updateCurrent    updateOutcome = "current"     // this copy is the latest release
	updateAvailable  updateOutcome = "available"   // a newer release exists and was not applied
	updateUpgraded   updateOutcome = "upgraded"    // a native copy was replaced
	updateFailed     updateOutcome = "failed"      // Failure says what class
	updateNotRelease updateOutcome = "not_release" // a development build; nothing to compare
	updateRolledBack updateOutcome = "rolled_back" // a person rolled back from Held; never installed again automatically
)

func knownUpdateOutcome(o updateOutcome) bool {
	switch o {
	case updateStarted, updateCurrent, updateAvailable, updateUpgraded, updateFailed, updateNotRelease, updateRolledBack:
		return true
	}
	return false
}

// updateStamp is update_check.json. Every string but Binary is a token: a
// release version in its canonical spelling, an outcome, a failure class.
// Binary is a path, compared and never printed.
type updateStamp struct {
	V         int       `json:"v"`
	CheckedAt time.Time `json:"checked_at"`
	// Binary is the resolved path of the binary the check was about: a
	// stamp is evidence only about that file.
	Binary  string        `json:"binary,omitempty"`
	Running string        `json:"running,omitempty"` // the version that ran the check
	Latest  string        `json:"latest,omitempty"`  // the release the check found
	Outcome updateOutcome `json:"outcome"`
	Failure string        `json:"failure,omitempty"` // an upgradeClass, with Outcome failed
	// Held is a release a person rolled back from, carried by every check
	// after it: neither it nor an older release is installed automatically.
	Held string `json:"held,omitempty"`
	// Refresh is "failed" when an automatic upgrade replaced the binary but
	// could not refresh the agent integrations it owns.
	Refresh string `json:"refresh,omitempty"`
	// Mode is the [update] mode the check ran under, so a release it found
	// under notify is applied as soon as the mode is apply, and one it kept
	// for any other reason is not looked up again within the day.
	Mode string `json:"mode,omitempty"`
}

// refreshFailed is updateStamp.Refresh's one value.
const refreshFailed = "failed"

func updateStampPath(root string) string { return filepath.Join(root, updateStampFile) }
func updateLockPath(root string) string  { return filepath.Join(root, updateLockFile) }

// updateRoot is the installation's own directory, where the stamp and its
// lock live: beside credentials.json (installRoot), which no sandbox may
// write. A config naming no state directory has none, and then no check
// runs: installRoot would be ".", wherever the command happened to run.
func updateRoot(m config.Search) string {
	if m.StateDir == "" {
		return ""
	}
	return installRoot(m)
}

// installationUpdateRoot is updateRoot for a command that knows the
// installation's directory rather than a config: its own jevlin.toml says
// where the stamp is, and an installation without one keeps it in the
// directory itself, the default layout.
func installationUpdateRoot(home string, getenv func(string) string) string {
	cfgPath := filepath.Join(home, setupConfigFile)
	if !lexists(cfgPath) {
		return home
	}
	cfg, _, err := loadConfig(cfgPath, getenv)
	if err != nil {
		root, _ := refusedConfigUpdate(cfgPath)
		return root
	}
	return updateRoot(cfg.Search)
}

// refusedConfigUpdate is the update root and mode for a config that exists
// and does not load: above all an earlier version's, which every command
// refuses until setup has run. That is the state an unattended update
// leaves a machine in, and a root of "" there meant no hook started the
// daily check again, and a rollback to the earlier version wrote no hold,
// so the next day's check installed this one again. The root is the
// installation directory as the file's own version placed it: for an
// earlier version's file the parent of its [miner] intake_dir, where 0.6.x
// kept the stamp beside credentials.json, else of its [mining] state_dir
// (the intake directory's default parent); for a format-1 file the parent
// of [search] state_dir; else the directory the file is in, which is the
// installation directory in every layout setup writes. The mode is the
// file's [update] mode, which both formats spell alike, else the default.
func refusedConfigUpdate(cfgPath string) (root, mode string) {
	if cfgPath == "" {
		return "", ""
	}
	root, mode = filepath.Dir(cfgPath), config.UpdateApply
	data, err := readExistingConfig(cfgPath)
	if err != nil || data == nil {
		return root, mode
	}
	var f struct {
		Search struct {
			StateDir string `toml:"state_dir"`
		} `toml:"search"`
		Mining struct {
			StateDir string `toml:"state_dir"`
		} `toml:"mining"`
		Miner struct {
			IntakeDir string `toml:"intake_dir"`
		} `toml:"miner"`
		Update struct {
			Mode string `toml:"mode"`
		} `toml:"update"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return root, mode
	}
	for _, dir := range []string{f.Miner.IntakeDir, f.Mining.StateDir, f.Search.StateDir} {
		if dir != "" && filepath.IsAbs(dir) {
			root = filepath.Dir(filepath.Clean(dir))
			break
		}
	}
	// As both formats read it: no mode is apply, and a word neither knows is
	// notify, never apply.
	switch m := strings.ToLower(strings.TrimSpace(f.Update.Mode)); m {
	case "", config.UpdateApply:
	case config.UpdateOff:
		mode = m
	default:
		mode = config.UpdateNotify
	}
	return root, mode
}

// stampBinary is the path a stamp names for the running binary: resolved,
// so a stamp written through a link and one written through the file agree.
func stampBinary(exe string) string {
	if resolved, err := selfupdate.ResolveExecutable(exe); err == nil {
		return resolved
	}
	return exe
}

// isReleaseVersion reports whether build is a release version in its
// canonical spelling, as the stamp records it.
func isReleaseVersion(build string) bool {
	v, err := selfupdate.ParseVersion(build)
	return err == nil && build == v.String()
}

// stampIsThisCopy reports whether st is about the binary at binary, which
// reports build. A stamp naming another binary is no evidence about this
// one: two copies may share an installation. Then the version must be the
// one the outcome leaves at the path: the release an upgrade installed, the
// one a rollback restored, the one a check ran as. A binary since replaced
// by hand reads its old stamp as none. A manual_intervention is the
// exception: the replacement could not put the binary back, so either
// version may be at the path, and the record belongs to the path.
func stampIsThisCopy(st updateStamp, build, binary string) bool {
	if st.Binary != "" && binary != "" && !samePath(st.Binary, binary) {
		return false
	}
	switch {
	case st.Outcome == updateNotRelease:
		return !isReleaseVersion(build)
	case stampNeedsAPerson(st):
		return st.Binary != "" && binary != ""
	case st.Outcome == updateUpgraded:
		return st.Latest == build
	default:
		return st.Running == build
	}
}

// stampNeedsAPerson is the record a check never overwrites: a replacement
// that could not put the binary back.
func stampNeedsAPerson(st updateStamp) bool {
	return st.Outcome == updateFailed && st.Failure == string(upgradeManual)
}

// errUpdateStampForeign is every way the stamp can fail to be one this
// version wrote, in the client's words: the record itself is never quoted.
var errUpdateStampForeign = errors.New(updateStampFile + " is not a record this version wrote")

// decodeUpdateStamp holds the record to what this client writes.
func decodeUpdateStamp(data []byte) (updateStamp, error) {
	var st updateStamp
	if err := json.Unmarshal(data, &st); err != nil {
		return updateStamp{}, errUpdateStampForeign
	}
	if st.V != 1 || st.CheckedAt.IsZero() || !knownUpdateOutcome(st.Outcome) ||
		!versionToken(st.Running) || !versionToken(st.Latest) || !versionToken(st.Held) ||
		!failureToken(st.Failure) || (st.Refresh != "" && st.Refresh != refreshFailed) || !binaryToken(st.Binary) ||
		(st.Mode != "" && st.Mode != config.UpdateApply && st.Mode != config.UpdateNotify) {
		return updateStamp{}, errUpdateStampForeign
	}
	return st, nil
}

// versionToken accepts the empty string and a release version in its
// canonical spelling, as selfupdate prints it.
func versionToken(s string) bool {
	if s == "" {
		return true
	}
	v, err := selfupdate.ParseVersion(s)
	return err == nil && v.String() == s
}

// binaryToken accepts the empty string and an absolute path with no
// character a terminal acts on. The path is only ever compared.
func binaryToken(s string) bool {
	return s == "" || (filepath.IsAbs(s) && !termtext.HasControlChar(s))
}

func failureToken(s string) bool {
	switch upgradeClass(s) {
	case "", upgradeRetry, upgradeReleaseInvalid, upgradeOwnership, upgradeFilesystem,
		upgradeLifecycleBusy, upgradeRefused, upgradeManual:
		return true
	}
	return false
}

// readUpdateStamp reads the stamp through read — the hook's own seam, or
// readUpdateStampFile. Absent is (zero, false, nil); unreadable, or not a
// record this version wrote, is an error that quotes nothing.
func readUpdateStamp(read func(string) ([]byte, error), path string) (updateStamp, bool, error) {
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return updateStamp{}, false, nil
	}
	if err != nil {
		return updateStamp{}, false, err
	}
	st, err := decodeUpdateStamp(data)
	if err != nil {
		return updateStamp{}, false, err
	}
	return st, true, nil
}

// readUpdateStampFile is the production read: bounded, and never waiting on
// a FIFO (fsx.ReadRegular), though no sandbox can write where the stamp is.
func readUpdateStampFile(path string) ([]byte, error) {
	return fsx.ReadRegular(path, stampMaxBytes)
}

// writeUpdateStamp replaces the stamp. fsx stages under an exclusive random
// name it creates, so no link at a predictable name is written through. The
// directory is not created here: it is the installation's, which setup
// makes.
func writeUpdateStamp(path string, st updateStamp) error {
	st.V = 1
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data, 0o600)
}

// updateStampFresh reports whether st postpones the next check. A stamp in
// the future — a clock that was set back — is not fresh, so no machine waits
// for a date that will not come.
func updateStampFresh(st updateStamp, now time.Time) bool {
	age := now.Sub(st.CheckedAt)
	return age >= 0 && age < updateCheckInterval
}

// updateCheckDue is the hook's whole decision, and the child's again under
// its lock: the mode is not off, the installation's directory is known, and
// the stamp is absent, unreadable, another copy's, a day old or in the future
// — unless it is this binary's manual_intervention, which waits for a person.
// One more case is due within the day: a stamp that found a release
// available under notify, when the mode is now apply and this is a native
// copy — the participant who switched should not wait a day for a release
// the check already knows. A release kept for another reason (a rollback's
// hold, another installation's binary) is not looked up again within the
// day. native is asked only then: it may read beside the binary. An
// unreadable stamp runs the check, which rewrites it.
func updateCheckDue(read func(string) ([]byte, error), root, mode, build, binary string, now time.Time, native func() bool) bool {
	if mode == config.UpdateOff || root == "" {
		return false
	}
	st, ok, err := readUpdateStamp(read, updateStampPath(root))
	if err == nil && ok && stampNeedsAPerson(st) && stampIsThisCopy(st, build, binary) {
		return false
	}
	if err != nil || !ok || !stampIsThisCopy(st, build, binary) || !updateStampFresh(st, now) {
		return true
	}
	return st.Outcome == updateAvailable && st.Mode == config.UpdateNotify && st.Held == "" &&
		mode == config.UpdateApply && native != nil && native()
}

// hookInsideASandbox reports whether a hook's own environment says it runs
// inside an agent's sandbox: Codex's sandbox markers, or a proxy on this
// machine's loopback, the one route out of Codex's sandbox
// (pkg/auth/proxy.go). A check is not started from there. The cost is a
// person whose own proxy listens on loopback: their hooks start no check,
// doctor says so after three days, and `jevlin upgrade` by hand still works.
func hookInsideASandbox(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	for _, k := range []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED"} {
		if getenv(k) != "" {
			return true
		}
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if proxyOnLoopback(getenv(k)) {
			return true
		}
	}
	return false
}

func proxyOnLoopback(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	return err == nil && isLoopbackName(u.Hostname())
}

// updateCheckArgs is the command line the detached check runs with, held in
// one place so a test reads the same words a hook spawns.
func updateCheckArgs(cfgPath string) []string {
	args := []string{"upgrade", "-auto"}
	if cfgPath != "" {
		args = append(args, "-config", cfgPath)
	}
	return args
}

// startUpdateCheck launches `upgrade -auto` as a detached child: best
// effort, output goes nowhere, the next hook tries again if this one could
// not even start.
func startUpdateCheck(cfgPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDetached(exe, updateCheckArgs(cfgPath))
}

// ── what status and doctor read ─────────────────────────────────────────

// updateFacts is everything the two reports say about the check: the mode,
// the stamp, which copy is asking, and the one command that applies a
// release to it.
type updateFacts struct {
	Mode        string
	UnknownMode string
	// Build and Binary are this copy's version and resolved path, which a
	// stamp must be about (stampIsThisCopy) to say anything about it.
	Build   string
	Binary  string
	Stamp   updateStamp
	Present bool
	Err     error
	// Fix is `jevlin upgrade` for a native copy and npm's own command for a
	// copy it owns, decided here from the launch, never from the stamp.
	Fix string
	// Recovery is every copy of the binary a failed replacement left beside
	// it, read from the binary's own directory, which no sandbox may write
	// — never from the stamp. Filled only when the stamp says the last
	// automatic upgrade needed a person (manual_intervention).
	Recovery []string
}

// thisCopy reports whether the stamp on file is this copy's own.
func (f updateFacts) thisCopy() bool { return f.Present && stampIsThisCopy(f.Stamp, f.Build, f.Binary) }

func gatherUpdateFacts(cfg *config.Config, getenv func(string) string) updateFacts {
	exe, err := os.Executable()
	return updateFactsFor(cfg, exe, err, getenv)
}

// updateFactsFor is gatherUpdateFacts for the binary at exe (or none, when
// exeErr says it could not be found).
func updateFactsFor(cfg *config.Config, exe string, exeErr error, getenv func(string) string) updateFacts {
	f := updateFacts{Mode: cfg.Update.Mode, UnknownMode: cfg.Update.UnknownMode, Build: buildVersion(), Fix: "jevlin upgrade"}
	err := exeErr
	if err == nil {
		f.Binary = stampBinary(exe)
		if kind := classifyLaunch(exe, getenv("JEVLIN_LAUNCH")); kind != launchNative {
			f.Fix = npmRemovalGuidance(kind, "upgrade")
		}
	}
	if root := updateRoot(cfg.Search); root != "" {
		f.Stamp, f.Present, f.Err = readUpdateStamp(readUpdateStampFile, updateStampPath(root))
	}
	if err == nil && f.thisCopy() && stampNeedsAPerson(f.Stamp) {
		f.Recovery = updateRecoveryPaths(exe)
	}
	return f
}

// updateRecoveryPaths is what a replacement that could not restore the
// binary leaves for a person: the one-level previous binary, and every
// staging file the self-updater names beside the executable, listed from
// that directory now rather than remembered. A detached upgrade printed
// them to nobody; status and doctor print them from here.
func updateRecoveryPaths(exe string) []string {
	resolved, err := selfupdate.ResolveExecutable(exe)
	if err != nil {
		resolved = exe
	}
	var out []string
	if previous := selfupdate.PreviousPath(resolved); lexists(previous) {
		out = append(out, previous)
	}
	left, _ := selfupdate.StagingLeftovers(filepath.Dir(resolved))
	return append(out, left...)
}

// ageText is a duration a person reads: "just now", "5m ago", "3h ago",
// "2d ago". A negative duration is a stamp from the future, said as such.
func ageText(d time.Duration) string {
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// updateLineText is status's one line about the check. Every word is the
// client's own; a version is printed from the parsed token.
func updateLineText(f updateFacts, now time.Time) string {
	if f.Mode == config.UpdateOff {
		return "automatic release checks are off ([update] mode)"
	}
	if f.Err != nil {
		return "the last check could not be read: " + f.Err.Error()
	}
	if !f.Present {
		return "no release check has run yet; the agent hooks start one daily"
	}
	st := f.Stamp
	age := ageText(now.Sub(st.CheckedAt))
	if !f.thisCopy() {
		return fmt.Sprintf("no release check has run yet for this copy; the last, %s, was by another copy of jevlin", age)
	}
	switch st.Outcome {
	case updateCurrent:
		return fmt.Sprintf("%s is the latest release (checked %s)", st.Running, age)
	case updateUpgraded:
		if st.Refresh == refreshFailed {
			return fmt.Sprintf("upgraded to %s %s, but the agent integrations could not be refreshed: run `jevlin agents install`, then restart open agents", st.Latest, age)
		}
		return fmt.Sprintf("upgraded to %s %s; restart open agents", st.Latest, age)
	case updateRolledBack:
		return fmt.Sprintf("rolled back from %s to %s %s; %s is not installed again automatically, a newer release of this minor is", st.Held, st.Running, age, st.Held)
	case updateAvailable:
		if st.Held != "" {
			return fmt.Sprintf("%s is the latest release, and this copy was rolled back from %s, so it is not installed automatically (checked %s) — %s installs it", st.Latest, st.Held, age, f.Fix)
		}
		if newMinor(st.Running, st.Latest) {
			return fmt.Sprintf("%s is available, a new minor release, which is not installed automatically and may need `jevlin setup` after it is; this copy is %s (checked %s) — %s", st.Latest, st.Running, age, f.Fix)
		}
		return fmt.Sprintf("%s is available and this copy is %s (checked %s) — %s", st.Latest, st.Running, age, f.Fix)
	case updateFailed:
		if stampNeedsAPerson(st) {
			return manualInterventionDetail(age, f.Recovery) + "; " + manualInterventionFix
		}
		return fmt.Sprintf("the last check failed (%s) %s; the next runs in a day", st.Failure, age)
	case updateNotRelease:
		return fmt.Sprintf("not a release build; nothing to compare (checked %s)", age)
	default: // started
		return fmt.Sprintf("a check started %s", age)
	}
}

// newMinor reports whether latest is a release of another major or minor
// version than running, both in their canonical spelling: the releases the
// daily check leaves to a person (autoAppliesAcross).
func newMinor(running, latest string) bool {
	r, err1 := selfupdate.ParseVersion(running)
	l, err2 := selfupdate.ParseVersion(latest)
	return err1 == nil && err2 == nil && !autoAppliesAcross(r, l)
}

// manualInterventionDetail and manualInterventionFix are what status and
// doctor say about a replacement that could not put the binary back. The
// surviving copies are listed from the binary's own directory, never from
// the stamp. No rollback is offered: .previous is the binary before the last
// completed upgrade, not the one this replacement displaced, so
// `upgrade -rollback` would move further back.
func manualInterventionDetail(age string, recovery []string) string {
	line := "the last automatic upgrade " + age + " could not put the binary back"
	if len(recovery) > 0 {
		line += "; these copies survive beside it: " + strings.Join(recovery, ", ")
	}
	return line
}

const manualInterventionFix = "reinstall from the releases page, or put one of those copies back by hand; none is deleted, and no check runs until `jevlin upgrade` succeeds"

// unknownModeNote names an [update] mode word this version does not know,
// quoted, and says what the check does instead. A word holding a character
// a terminal acts on is not repeated.
func unknownModeNote(word string) string {
	if word == "" {
		return ""
	}
	if termtext.HasControlChar(word) || len(word) > 32 {
		return "; [update] mode names a word this version does not know, so it checks and notifies only"
	}
	return fmt.Sprintf("; [update] mode %q is a word this version does not know, so it checks and notifies only", word)
}
