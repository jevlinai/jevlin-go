package main

// The daily release check (docs/guide.md "Upgrading").
//
// Every hook that starts a flush — a session's start and end, on every host
// — also reads one stamp in the state directory, update_check.json, and when
// no check has run in the last day starts `jevlin upgrade -auto` as a
// detached child, the exact shape of the flush. The hook reads one bounded
// file and starts at most one process; it contacts nothing. The child is the
// one process a day that reaches the compiled-in GitHub release origin, with
// no credential: the self-updater's own path, invariant 5's one exception,
// and no new destination. With [update] mode = "apply", the default, a native
// copy replaces itself through the same verified transaction `jevlin upgrade`
// runs (invariant 17); with "notify", or for a copy npm installed, which is
// never replaced, it only records that a newer release exists, which
// `jevlin status` and `jevlin doctor` then say.
//
// Hooks run outside an agent's sandbox, which is what lets the child reach a
// host Codex's permission profile does not list (invariant 1): the search
// path, and the processes a search spawns, are not touched.
//
// The stamp lives in the state directory, which a sandboxed command can
// write (invariant 19): it is read without waiting and bounded, every word
// in it is held on load to a token this client writes — a version, an
// outcome, a failure class — and a record that is not is refused unread, so
// nothing a writer put there is acted on or shown. The worst a forged stamp
// can do is postpone a check by a day.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const (
	updateStampFile = "update_check.json"
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
)

func knownUpdateOutcome(o updateOutcome) bool {
	switch o {
	case updateStarted, updateCurrent, updateAvailable, updateUpgraded, updateFailed, updateNotRelease:
		return true
	}
	return false
}

// updateStamp is update_check.json. Every string is a token: a release
// version in its canonical spelling, an outcome, a failure class.
type updateStamp struct {
	V         int           `json:"v"`
	CheckedAt time.Time     `json:"checked_at"`
	Running   string        `json:"running,omitempty"` // the version that ran the check
	Latest    string        `json:"latest,omitempty"`  // the release the check found
	Outcome   updateOutcome `json:"outcome"`
	Failure   string        `json:"failure,omitempty"` // an upgradeClass, with Outcome failed
}

func updateStampPath(stateDir string) string { return filepath.Join(stateDir, updateStampFile) }

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
		!versionToken(st.Running) || !versionToken(st.Latest) || !failureToken(st.Failure) {
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

// readUpdateStampFile is the production read: the state dir is a writable
// root of Codex's sandbox, so a FIFO or a huge file at the name must cost
// the check, never hang the command (fsx.ReadRegular).
func readUpdateStampFile(path string) ([]byte, error) {
	return fsx.ReadRegular(path, stampMaxBytes)
}

// writeUpdateStamp replaces the stamp. fsx stages under an exclusive random
// name it creates, so no link at a predictable name is written through. The
// state directory is not created here: setup makes it, and a check on a
// machine without one simply runs again at the next session.
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

// updateCheckDue is the hook's whole decision: the mode is not off, the
// state directory is known, and the stamp is absent, unreadable, a day old
// or in the future. An unreadable stamp runs the check, which rewrites it.
func updateCheckDue(read func(string) ([]byte, error), stateDir, mode string, now time.Time) bool {
	if mode == config.UpdateOff || stateDir == "" {
		return false
	}
	st, ok, err := readUpdateStamp(read, updateStampPath(stateDir))
	if err != nil || !ok {
		return true
	}
	return !updateStampFresh(st, now)
}

// startUpdateCheck launches `upgrade -auto` as a detached child, the exact
// shape of startFlush: best effort, output goes nowhere, the next hook tries
// again if this one could not even start.
func startUpdateCheck(cfgPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"upgrade", "-auto"}
	if cfgPath != "" {
		args = append(args, "-config", cfgPath)
	}
	return spawnDetached(exe, args)
}

// ── what status and doctor read ─────────────────────────────────────────

// updateFacts is everything the two reports say about the check: the mode,
// the stamp, and the one command that applies a release to this copy.
type updateFacts struct {
	Mode        string
	UnknownMode string
	Stamp       updateStamp
	Present     bool
	Err         error
	// Fix is `jevlin upgrade` for a native copy and npm's own command for a
	// copy it owns, decided here from the launch, never from the stamp.
	Fix string
}

func gatherUpdateFacts(cfg *config.Config, getenv func(string) string) updateFacts {
	f := updateFacts{Mode: cfg.Update.Mode, UnknownMode: cfg.Update.UnknownMode, Fix: updateFixCommand(getenv)}
	if cfg.Mining.StateDir != "" {
		f.Stamp, f.Present, f.Err = readUpdateStamp(readUpdateStampFile, updateStampPath(cfg.Mining.StateDir))
	}
	return f
}

func updateFixCommand(getenv func(string) string) string {
	exe, err := os.Executable()
	if err != nil {
		return "jevlin upgrade"
	}
	if kind := classifyLaunch(exe, getenv("JEVLIN_LAUNCH")); kind != launchNative {
		return npmRemovalGuidance(kind, "upgrade")
	}
	return "jevlin upgrade"
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
	switch st.Outcome {
	case updateCurrent:
		return fmt.Sprintf("%s is the latest release (checked %s)", st.Running, age)
	case updateUpgraded:
		return fmt.Sprintf("upgraded to %s %s; restart open agents", st.Latest, age)
	case updateAvailable:
		return fmt.Sprintf("%s is available and this copy is %s (checked %s) — %s", st.Latest, st.Running, age, f.Fix)
	case updateFailed:
		return fmt.Sprintf("the last check failed (%s) %s; the next runs in a day", st.Failure, age)
	case updateNotRelease:
		return fmt.Sprintf("not a release build; nothing to compare (checked %s)", age)
	default: // started
		return fmt.Sprintf("a check started %s", age)
	}
}
