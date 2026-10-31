package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Exit codes, shared by every command: the shell convention an agent or a
// script can branch on without parsing stderr.
const (
	exitOK        = 0
	exitTransport = 1
	exitUsage     = 2
	exitClientErr = 3 // any non-2xx below 500
	exitServerErr = 4 // HTTP 5xx
)

// exitOutcomeUnknown: an upgrade's outcome could not be classified from a
// typed kind, which is a defect in the classifier rather than a state the
// participant can act on.
const exitOutcomeUnknown = 5

// exitLifecycleBusy is connectRun's internal signal, in machine mode only,
// that it never passed the lifecycle gate. cmdConnect's JSON wrapper
// translates it to exitTransport with code lifecycle_busy; like
// exitHumanDecisionRequired it is out of range so a leak would be obvious.
const exitLifecycleBusy = -2

// exitConfigNotFound is connectRun's internal signal, in machine mode only,
// that resolution (describeConfigSource, ruling D-R1) found no config file
// at all. Like the other two sentinels, connectCommand's JSON wrapper
// translates it before anything reaches main(); the text path never
// produces it (machine is false there), so it is out of the exit-code
// range for the same reason as the other two.
const exitConfigNotFound = -3

// exitForceDoesNotRebuild is connectRun's internal signal, in machine mode
// only, that -force met a record naming another agent than the stored key's:
// -force replaces rather than recovers, so the run stops, and only dropping
// -force can rebuild it. cmdConnect's JSON wrapper translates it to
// fix_input with code force_does_not_rebuild: retrying the same command
// can never succeed, so "retry" was the wrong instruction.
const exitForceDoesNotRebuild = -4

// exitKeyOffOrigin is connectRun's internal signal, in machine mode only,
// that the stored key is bound to another agents API than the config names
// (connectKeyRefusal). connectCommand's own pre-check normally answers
// first; this is the net behind it, translated to fix_input with code
// key_origin_mismatch like that pre-check.
const exitKeyOffOrigin = -5

// configProblem is a config that did not load, said with its source once:
// pkg/config names the file in a parse or schema error already, and a
// value's rule names only its key.
func configProblem(cfgSource string, err error) string {
	if cfgSource != "" && strings.Contains(err.Error(), cfgSource) {
		return fmt.Sprintf("config: %v", err)
	}
	return fmt.Sprintf("config (%s): %v", orDefaults(cfgSource), err)
}

func orDefaults(cfgSource string) string {
	if cfgSource == "" {
		return "defaults/env, no config file found"
	}
	return cfgSource
}

// printConfigSource names, in every text report that resolves one, exactly
// which config file it read (or that none was found and built-in defaults
// are in use) and which state directory that resolution led to — ruling
// D-R1's naming requirement. status and doctor both call this before
// anything else, so a participant whose terminal resolved a different
// installation than they expected sees why at the top of the report rather
// than having to infer it from what follows.
func printConfigSource(w io.Writer, cfgSource, stateDir string) {
	fmt.Fprintf(w, "%-8s %s\n", "config:", orDefaults(cfgSource))
	fmt.Fprintf(w, "%-8s %s\n", "state:", stateDir)
}

// newFlagSet builds a FlagSet whose usage output goes to the command's own
// stderr writer rather than the process's.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// onceString is a string flag that refuses to be set twice. The flag
// package lets a later occurrence win, and a Claude Code allow rule is a
// prefix match ending after this installation's `-config <path>`, so
// anything appended to it — a second -config — would run unprompted with
// another config's router and the same key.
type onceString struct {
	value string
	set   bool
}

func (o *onceString) String() string { return o.value }

func (o *onceString) Set(v string) error {
	if o.set {
		return errors.New("may be given only once")
	}
	o.value, o.set = v, true
	return nil
}

// onceStringFlag defines a flag whose value can be given at most once.
func onceStringFlag(fs *flag.FlagSet, name, usage string) *string {
	o := &onceString{}
	fs.Var(o, name, usage)
	return &o.value
}

// operatorContext cancels on SIGINT/SIGTERM so a person waiting at a
// terminal can abandon a command without leaving anything open.
func operatorContext(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	return sigCtx, func() { stop(); cancel() }
}
