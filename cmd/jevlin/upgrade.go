package main

// upgrade: replace this native binary with a verified canonical release, or
// with the one-level previous binary.
//
// The order is fixed. The launch classifier decides first whether this copy
// is native at all; an npm copy is told npm's own command and nothing else
// happens. One operation context with selfupdate.OperationTimeout is created
// next and carried through everything that follows — the lifecycle
// exclusion, release discovery, preparation and replacement, or rollback —
// and the candidate's own five-second bound is taken inside it. Success is
// printed only after the transaction returned no error, which is after the
// canonical path validated and .previous was committed. Every Install is
// followed by Prepared.DiscardAfterInstall(err), so a copy an error reports
// as surviving is never removed.
//
// Only then, and never as a condition of success, are the host integrations
// this installation owns rendered again by the binary now installed
// (upgrade_rerender.go, dropin-miner#111).
//
// Failures print one class token — retry, release_invalid, ownership,
// filesystem, lifecycle_busy, refused or manual_intervention — chosen from
// typed errors, never from message text, each with its own exit code where
// the shared exit codes allow and always with its own token.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

const upgradeUsage = `usage: jevlin upgrade [-version X.Y.Z | -rollback] [-home dir]
       jevlin upgrade -auto [-config file] [-home dir]

Replaces this native binary with the latest canonical Jevlin release, or
with -version exactly that release, after verifying its checksum, inspecting
its archive and running the new binary's version command. The binary it
replaces is kept as <binary>.previous. A copy npm installed is updated with npm.

  -version X.Y.Z  upgrade to exactly this release; never an older one
  -rollback       put back <binary>.previous, the one binary the last upgrade or
                  rollback replaced; no network
  -home dir       the installation whose lifecycle lock is held (default as
                  uninstall resolves it)
  -auto           the daily check the agent hooks start, not a command a
                  person runs: with [update] mode = "apply" (the default) a
                  native copy is replaced by a newer release; a copy npm
                  installed, or mode "notify", only records that one exists.
                  Exits quietly; jevlin status and doctor report the result
  -config file    with -auto: the config the hook runs with
`

// upgradeClass is what a participant can do about a failed upgrade.
type upgradeClass string

const (
	upgradeRetry          upgradeClass = "retry"
	upgradeReleaseInvalid upgradeClass = "release_invalid"
	upgradeOwnership      upgradeClass = "ownership"
	upgradeFilesystem     upgradeClass = "filesystem"
	upgradeLifecycleBusy  upgradeClass = "lifecycle_busy"
	upgradeRefused        upgradeClass = "refused"
	upgradeManual         upgradeClass = "manual_intervention"
)

// exit is the class's process exit code.
func (c upgradeClass) exit() int {
	switch c {
	case upgradeRetry, upgradeLifecycleBusy:
		return exitTransport
	case upgradeOwnership, upgradeRefused:
		return exitUsage
	case upgradeReleaseInvalid, upgradeFilesystem:
		return exitClientErr
	default:
		return exitOutcomeUnknown
	}
}

// errUpgradeOwnership marks the launch classifier's refusal.
var errUpgradeOwnership = errors.New("not a native copy")

// classifyUpgrade maps a failure to its class from types alone.
func classifyUpgrade(err error) upgradeClass {
	var active *lifecycleActiveError
	switch {
	case errors.Is(err, errUpgradeOwnership):
		return upgradeOwnership
	case errors.Is(err, errLifecycleBusy), errors.As(err, &active):
		return upgradeLifecycleBusy
	}
	switch selfupdate.KindOf(err) {
	case selfupdate.KindUnavailable, selfupdate.KindReplacementFailed, selfupdate.KindPreviousInUse:
		return upgradeRetry
	case selfupdate.KindReleaseInvalid, selfupdate.KindCandidateInvalid, selfupdate.KindUnsupported:
		return upgradeReleaseInvalid
	case selfupdate.KindNotRelease:
		return upgradeOwnership
	case selfupdate.KindDowngrade:
		return upgradeRefused
	case selfupdate.KindStaging, selfupdate.KindNoPrevious, selfupdate.KindPreviousInvalid:
		return upgradeFilesystem
	case selfupdate.KindIncomplete, selfupdate.KindManualIntervention:
		return upgradeManual
	}
	return upgradeFilesystem
}

// upgradeDeps is everything upgrade reaches outside itself.
type upgradeDeps struct {
	stdout, stderr   io.Writer
	getenv           func(string) string
	userHome         string
	executable       func() (string, error)
	build            func() string
	source           func() selfupdate.ReleaseSource
	runner           selfupdate.CommandRunner
	goos, goarch     string
	operationTimeout time.Duration
	// agents and environ serve the re-render that follows a committed
	// replacement: where the host files are, and what the child that renders
	// them runs with.
	agents  agentOps
	environ func() []string
	// now is the clock -auto stamps with; nil means time.Now.
	now func() time.Time
}

func (d upgradeDeps) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func cmdUpgrade(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	home, _ := os.UserHomeDir()
	return upgradeMain(upgradeDeps{
		stdout:           stdout,
		stderr:           stderr,
		getenv:           getenv,
		userHome:         home,
		executable:       os.Executable,
		build:            buildVersion,
		source:           func() selfupdate.ReleaseSource { return selfupdate.NewHTTPSource(nil) },
		runner:           selfupdate.ExecRunner{},
		goos:             runtime.GOOS,
		goarch:           runtime.GOARCH,
		operationTimeout: selfupdate.OperationTimeout,
		agents:           realAgentOps(),
		environ:          upgradeEnviron,
		now:              time.Now,
	}, args)
}

func upgradeMain(d upgradeDeps, args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(d.stderr)
	fs.Usage = func() {}
	versionFlag := fs.String("version", "", "upgrade to exactly this release")
	rollback := fs.Bool("rollback", false, "put back the previous binary")
	homeFlag := fs.String("home", "", "the installation directory")
	auto := fs.Bool("auto", false, "the daily check the hooks start")
	cfgFlag := fs.String("config", "", "with -auto: the config to read")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(d.stdout, upgradeUsage)
			return exitOK
		}
		fmt.Fprint(d.stderr, upgradeUsage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(d.stderr, "jevlin upgrade: unexpected argument %q\n%s", fs.Arg(0), upgradeUsage)
		return exitUsage
	}
	if *rollback && *versionFlag != "" {
		fmt.Fprintf(d.stderr, "jevlin upgrade: -version and -rollback cannot be combined: -rollback restores the one previous binary, whatever its version\n%s", upgradeUsage)
		return exitUsage
	}
	if *auto {
		if *rollback || *versionFlag != "" {
			fmt.Fprintf(d.stderr, "jevlin upgrade: -auto takes neither -version nor -rollback: it checks for the latest release and never moves backwards\n%s", upgradeUsage)
			return exitUsage
		}
		return upgradeAuto(d, *cfgFlag, *homeFlag)
	}
	if *cfgFlag != "" {
		fmt.Fprintf(d.stderr, "jevlin upgrade: -config is read only with -auto; an upgrade takes -home\n%s", upgradeUsage)
		return exitUsage
	}
	var requested *selfupdate.Version
	if *versionFlag != "" {
		v, err := selfupdate.ParseVersion(*versionFlag)
		if err != nil {
			fmt.Fprintf(d.stderr, "jevlin upgrade: -version: %v\n%s", err, upgradeUsage)
			return exitUsage
		}
		requested = &v
	}
	return upgradeRun(d, *homeFlag, requested, *rollback)
}

func upgradeRun(d upgradeDeps, homeFlag string, requested *selfupdate.Version, rollback bool) int {
	fail := func(err error, paths ...string) int {
		class := classifyUpgrade(err)
		fmt.Fprintf(d.stderr, "jevlin upgrade: %s: %v\n", class, err)
		var typed *selfupdate.Error
		if errors.As(err, &typed) {
			paths = append(paths, typed.Paths...)
		}
		for _, p := range paths {
			fmt.Fprintf(d.stderr, "  %s\n", p)
		}
		return class.exit()
	}

	exe, err := d.executable()
	if err != nil {
		return fail(fmt.Errorf("cannot determine my own path: %w", err))
	}
	resolved, err := selfupdate.ResolveExecutable(exe)
	if err != nil {
		return fail(&selfupdate.Error{Kind: selfupdate.KindStaging, Err: err})
	}
	if err := checkUpgradeLaunch(resolved, d.getenv("JEVLIN_LAUNCH")); err != nil {
		return fail(fmt.Errorf("%w: %v", errUpgradeOwnership, err))
	}
	current := d.build()
	if v, err := selfupdate.ParseVersion(current); err != nil || current != v.String() {
		// Before any lock file is created or anything is fetched.
		return fail(&selfupdate.Error{Kind: selfupdate.KindNotRelease,
			Err: fmt.Errorf("this binary reports %q, not a release version; install a release before upgrading", current)})
	}
	home, err := resolveInstallationHome(homeFlag, d.getenv, d.userHome, exe)
	if err != nil {
		return fail(err)
	}

	// The one deadline for the whole operation, set before anything waits,
	// fetches or replaces.
	ctx, cancel := context.WithTimeout(context.Background(), d.operationTimeout)
	defer cancel()

	ex, err := excludeForUpgrade(home, resolved, lifecycleForegroundWait)
	if err != nil {
		return fail(err)
	}
	defer ex.release()
	ensureUpgradeFlushLock(home, d.getenv, d.stderr)

	updater := selfupdate.Updater{Runner: d.runner, GOOS: d.goos, GOARCH: d.goarch}
	if rollback {
		rolled, err := updater.Rollback(ctx, resolved, current)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(d.stdout, "rolled back %s from %s to %s\nthe binary it replaced is now %s; run `jevlin upgrade -rollback` again to swap back\n",
			rolled.Executable, rolled.From, rolled.To, selfupdate.PreviousPath(rolled.Executable))
		d.rerenderOwned(ctx, exe, resolved, home)
		return exitOK
	}

	updater.Source = d.source()
	prepared, err := updater.Prepare(ctx, resolved, current, requested)
	if err != nil {
		return fail(err)
	}
	if prepared.NoChange {
		fmt.Fprintf(d.stdout, "jevlin %s is already the %s release; nothing to do\n", prepared.From, selectedRelease(requested))
		return exitOK
	}
	if err := installPrepared(ctx, updater, prepared); err != nil {
		return fail(err)
	}
	fmt.Fprintf(d.stdout, "upgraded %s from %s to %s\nthe binary it replaced is kept as %s; `jevlin upgrade -rollback` restores it\n",
		prepared.Executable, prepared.From, prepared.To, selfupdate.PreviousPath(prepared.Executable))
	d.rerenderOwned(ctx, exe, resolved, home)
	return exitOK
}

// installPrepared is the one place a replacement is attempted: every
// Install is followed by Prepared.DiscardAfterInstall(err), so a copy an
// error reports as surviving is never removed.
func installPrepared(ctx context.Context, updater selfupdate.Updater, prepared selfupdate.Prepared) error {
	err := updater.Install(ctx, prepared)
	prepared.DiscardAfterInstall(err)
	return err
}

// ensureUpgradeFlushLock creates the installation's flush lock when it is
// absent, so a flush a sandboxed agent starts after the upgrade can take it
// read-only. It runs inside the upgrade's exclusion and never fails the
// upgrade: a lock it cannot create is said, and the first flush outside a
// sandbox creates it.
func ensureUpgradeFlushLock(home string, getenv func(string) string, stderr io.Writer) {
	_, path, err := operationLockPaths(home, getenv)
	if err == nil {
		_, err = ensureFlushLockFile(path)
	}
	if err != nil {
		fmt.Fprintf(stderr, "jevlin upgrade: note: the flush lock could not be created (%v); a flush inside an agent's sandbox will not run until one outside it has\n", err)
	}
}

// upgradeAuto is the daily check (update_check.go): the detached child a
// hook starts. Nobody is at the terminal, so it says what it did on stderr,
// records it in the stamp, which status and doctor read, and exits 0 on
// every outcome but a config it cannot load. It takes the locks an upgrade
// takes, in one attempt, so it never waits on a setup, uninstall or upgrade
// a person is running and never runs beside another check of the same
// binary; the stamp is read under them, so two hooks that both found it
// stale make one request, not two. The stamp is claimed before the network
// is touched: a child that dies mid-way postpones the next check a day
// rather than running one every session.
func upgradeAuto(d upgradeDeps, cfgPath, homeFlag string) int {
	say := func(format string, a ...any) {
		fmt.Fprintf(d.stderr, "jevlin upgrade -auto: "+format+"\n", a...)
	}
	cfg, src, err := loadConfig(cfgPath, d.getenv)
	if err != nil {
		say("config (%s): %v", orDefaults(src), err)
		return exitTransport
	}
	if cfg.Update.Mode == config.UpdateOff {
		say("[update] mode is off; nothing checked")
		return exitOK
	}
	stateDir := cfg.Mining.StateDir
	if stateDir == "" {
		say("no state directory; nothing checked")
		return exitOK
	}
	exe, err := d.executable()
	if err != nil {
		say("cannot determine my own path: %v", err)
		return exitOK
	}
	resolved, err := selfupdate.ResolveExecutable(exe)
	if err != nil {
		say("%v", err)
		return exitOK
	}
	native := classifyLaunch(resolved, d.getenv("JEVLIN_LAUNCH")) == launchNative
	home := ""
	if native {
		// The installation a manual upgrade would hold, by the same rule
		// (resolveInstallationHome), so the two never hold different gates
		// for one binary. The hook's config names the state directory, not
		// the installation.
		home, err = resolveInstallationHome(homeFlag, d.getenv, d.userHome, exe)
		if err != nil {
			say("%v", err)
			return exitOK
		}
		ex, err := excludeForUpgrade(home, resolved, 0)
		if err != nil {
			say("%v; not checking now", err)
			return exitOK
		}
		defer ex.release()
	}
	// One check at a time per state directory, whatever the copy: a lookup
	// for a copy this command never replaces holds this and nothing beside
	// the binary. A lock that cannot be opened is a state directory the
	// stamp cannot be written to either; the check goes on without it.
	if lock, held, lerr := tryLockFile(updateLockPath(stateDir)); lerr == nil {
		if !held {
			say("another check holds %s; not checking now", updateLockPath(stateDir))
			return exitOK
		}
		defer func() { _ = unlockFile(lock) }()
	}

	now := d.clock()
	current := d.build()
	stampPath := updateStampPath(stateDir)
	if !updateCheckDue(readUpdateStampFile, stateDir, cfg.Update.Mode, current, now, func() bool { return native }) {
		say("checked today; not due")
		return exitOK
	}
	record := func(st updateStamp) {
		if werr := writeUpdateStamp(stampPath, st); werr != nil {
			say("the stamp could not be written (%v); the next session checks again", werr)
		}
	}
	stamp := updateStamp{CheckedAt: now, Outcome: updateStarted}
	v, perr := selfupdate.ParseVersion(current)
	if perr != nil || current != v.String() {
		stamp.Outcome = updateNotRelease
		record(stamp)
		say("this binary reports %q, not a release version; nothing to compare", current)
		return exitOK
	}
	stamp.Running = current
	record(stamp)
	failed := func(err error) int {
		stamp.Outcome, stamp.Failure = updateFailed, string(autoFailureClass(err))
		record(stamp)
		say("%s: %v", stamp.Failure, err)
		// The surviving copies a replacement reports go to a stderr nobody
		// reads; status and doctor list them from the binary's directory
		// (updateRecoveryPaths) for as long as the stamp says so.
		var typed *selfupdate.Error
		if errors.As(err, &typed) {
			for _, p := range typed.Paths {
				say("  %s", p)
			}
		}
		return exitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.operationTimeout)
	defer cancel()

	if native && cfg.Update.Mode == config.UpdateApply {
		ensureUpgradeFlushLock(home, d.getenv, d.stderr)
		updater := selfupdate.Updater{Source: d.source(), Runner: d.runner, GOOS: d.goos, GOARCH: d.goarch}
		prepared, err := updater.Prepare(ctx, resolved, current, nil)
		if selfupdate.KindOf(err) == selfupdate.KindDowngrade {
			// GitHub's latest is older than this copy — a release was
			// withdrawn. Nothing to apply, and not a failure of the check.
			prepared, err = selfupdate.Prepared{From: v, To: v, NoChange: true}, nil
		}
		if err != nil {
			return failed(err)
		}
		if prepared.NoChange {
			stamp.Outcome, stamp.Latest = updateCurrent, current
			record(stamp)
			say("%s is the latest release", current)
			return exitOK
		}
		if err := installPrepared(ctx, updater, prepared); err != nil {
			return failed(err)
		}
		stamp.Outcome, stamp.Latest = updateUpgraded, prepared.To.String()
		record(stamp)
		say("upgraded %s from %s to %s; the binary it replaced is kept as %s", prepared.Executable, prepared.From, prepared.To, selfupdate.PreviousPath(prepared.Executable))
		d.rerenderOwned(ctx, exe, resolved, home)
		return exitOK
	}

	// notify, or a copy this command never replaces: one request, no download.
	release, err := d.source().Release(ctx, nil)
	if err != nil {
		return failed(err)
	}
	if release.Version.Compare(v) > 0 {
		stamp.Outcome, stamp.Latest = updateAvailable, release.Version.String()
		record(stamp)
		say("%s is available; this copy is %s and is not replaced here", stamp.Latest, current)
		return exitOK
	}
	stamp.Outcome, stamp.Latest = updateCurrent, current
	record(stamp)
	say("%s is the latest release", current)
	return exitOK
}

// autoFailureClass is classifyUpgrade for the check, where an error with no
// kind is the network — a release lookup that could not be made — and so
// retry, never the filesystem.
func autoFailureClass(err error) upgradeClass {
	var typed *selfupdate.Error
	var active *lifecycleActiveError
	if !errors.As(err, &typed) && !errors.As(err, &active) && !errors.Is(err, errLifecycleBusy) && !errors.Is(err, errUpgradeOwnership) {
		return upgradeRetry
	}
	return classifyUpgrade(err)
}

func selectedRelease(requested *selfupdate.Version) string {
	if requested != nil {
		return "requested"
	}
	return "latest"
}
