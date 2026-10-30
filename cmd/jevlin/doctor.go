package main

// `doctor` — the participant's health check.
//
// `status` prints what this installation has, flatly. This command is a
// VERDICT, not a dump: one line per question, each of which is either true
// or not, and each of which names the next command when it is not.
//
// It opens only existing state and creates nothing to diagnose it: no
// state directory, no key, no registration. Every check is local except
// `up to date`, which reads the stamp the daily release check left rather
// than asking anyone.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/redact"
)

// doctorVerdict has exactly three values, and the third is not a polite
// spelling of the second. NO is a fact — the thing is not true, and there is
// usually something to do about it. UNKNOWN is the absence of a fact, which
// calls for fixing the dependency rather than the setup.
type doctorVerdict string

const (
	verdictOK      doctorVerdict = "OK"
	verdictNo      doctorVerdict = "NO"
	verdictUnknown doctorVerdict = "UNKNOWN"
)

type doctorCheck struct {
	Name    string
	Verdict doctorVerdict
	Detail  string
	// Fix is the next command or action, and is empty when there is none
	// to offer.
	Fix string
}

// doctorFacts is everything the checks are computed from: the raw answers
// and the raw failures, with no interpretation applied yet. Gathering and
// judging are separate so the judging is testable without a filesystem.
type doctorFacts struct {
	// ConfigSource is the file describeConfigSource resolved (ruling
	// D-R1), empty when resolution found none and built-in defaults are in
	// use. StateDir is the state directory that config led to.
	ConfigSource string
	StateDir     string

	// Credential is whether credentials.json holds a key this installation
	// may send to its router, and CredentialErr why it could not be read
	// when it could not. A missing file is neither: HasCredential false
	// with no error.
	HasCredential bool
	CredentialErr error

	// Registration is the stored platform identity, when there is one.
	HasRegistration bool
	Registration    auth.AgentRegistration
	RegistrationErr error

	// TraceKey is whether the state directory holds the trace key a search
	// with no hook keys its session id with; TraceKeyErr why it could not
	// be read.
	HasTraceKey bool
	TraceKeyErr error

	// State is who can open the state directory and its credentials, where
	// the platform has access lists to ask (state_acl.go); elsewhere it is not
	// checked and there is no state access check.
	State stateAccessFacts

	// Update is the daily release check's stamp and mode (update_check.go),
	// for `up to date`.
	Update updateFacts

	// Binary is which copy a bare `jevlin` runs, next to the one this
	// installation keeps at <home>/bin (gatherBinaryFacts). Checked only
	// when the installation keeps one.
	Binary binaryFacts

	// Now is sampled once, by the gatherer. Nothing downstream calls
	// time.Now(), so a judgment over these facts is reproducible.
	Now time.Time
}

// assembleDoctor turns the gathered facts into the verdicts.
//
// It is pure — including of the clock, which arrives as f.Now — and it is
// where every wording rule lives.
func assembleDoctor(f doctorFacts) []doctorCheck {
	checks := []doctorCheck{
		doctorCredentialCheck(f),
		doctorRegistrationCheck(f),
		doctorTraceKeyCheck(f),
		doctorUpdateCheck(f),
	}
	if f.Binary.Checked {
		checks = append(checks, doctorBinaryCheck(f.Binary))
	}
	if f.State.Checked {
		checks = append(checks, doctorStateCheck(f.State))
	}
	return checks
}

// doctorCredentialCheck: does this installation hold a key a search can
// send? The key's value is never named, only whether one is on file.
func doctorCredentialCheck(f doctorFacts) doctorCheck {
	c := doctorCheck{Name: "search key"}
	switch {
	case f.CredentialErr != nil:
		c.Verdict, c.Detail = verdictNo, "credentials.json cannot be used: "+redact.Error(f.CredentialErr).Error()
		c.Fix = "jevlin login, or jevlin connect"
	case f.HasCredential:
		c.Verdict, c.Detail = verdictOK, "a key is stored for this installation's router"
	default:
		c.Verdict, c.Detail = verdictNo, "no key is stored; a search would need JEVLIN_API_KEY"
		c.Fix = "jevlin connect (or jevlin login with a key you already have)"
	}
	return c
}

// doctorRegistrationCheck: is this agent registered with the platform, and
// has a person claimed it? Unregistered is OK with a note — a key from
// `jevlin login` searches without one — and unclaimed is NO, because the
// claim is what ties the agent to an account.
func doctorRegistrationCheck(f doctorFacts) doctorCheck {
	c := doctorCheck{Name: "registration"}
	switch {
	case f.RegistrationErr != nil:
		c.Verdict, c.Detail = verdictNo, "the registration on file could not be read: "+redact.Error(f.RegistrationErr).Error()
		if errors.Is(f.RegistrationErr, auth.ErrAgentRegistrationCorrupt) {
			c.Fix = "jevlin connect, which rebuilds it from the platform"
		}
	case !f.HasRegistration:
		c.Verdict, c.Detail = verdictOK, "not registered with the platform; a stored key still searches"
	case f.Registration.Status == "claimed":
		c.Verdict, c.Detail = verdictOK, "claimed (scopes: "+strings.Join(f.Registration.Scopes, ", ")+")"
	case f.Registration.Status == "expired":
		c.Verdict, c.Detail = verdictNo, "the registration expired before it was claimed"
		c.Fix = "jevlin connect"
	default:
		c.Verdict, c.Detail = verdictNo, "registered and not yet claimed"
		c.Fix = "jevlin connect prints the claim link"
	}
	return c
}

// doctorTraceKeyCheck: can a search from a shell with no hook send a stable
// session id? Without the key each such search sends a one-off id, which
// loses nothing but the grouping; a search inside a sandbox cannot make the
// key itself.
func doctorTraceKeyCheck(f doctorFacts) doctorCheck {
	c := doctorCheck{Name: "trace key"}
	switch {
	case f.TraceKeyErr != nil:
		c.Verdict, c.Detail = verdictNo, "the trace key cannot be read: "+redact.Error(f.TraceKeyErr).Error()
		c.Fix = "jevlin setup"
	case f.HasTraceKey:
		c.Verdict, c.Detail = verdictOK, "present in the state directory"
	default:
		c.Verdict, c.Detail = verdictOK, "not made yet; the first search that can write the state directory makes it"
	}
	return c
}

// cmdDoctor gathers the facts and prints the verdicts.
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", stderr)
	cfgPath := fs.String("config", "", "path to TOML config file")
	asJSON := fs.Bool("json", false, "report as one JSON object instead of text")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, src, err := loadConfig(*cfgPath, os.Getenv)
	if err != nil {
		if *asJSON {
			emitMachine(stdout, commandEnvelope{
				machineHeader: newMachineHeader("doctor", exitTransport, "config_unreadable", false, actionFixInput),
				Error:         clientMessage(err),
			})
			return exitTransport
		}
		fmt.Fprintf(stderr, "jevlin: config (%s): %v\n", orDefaults(src), err)
		return exitTransport
	}

	// Gathered once, rendered either way: the JSON report makes no read
	// the text report does not make, and neither is produced from the
	// other's output.
	f := gatherDoctorFacts(cfg, src, os.Getenv)
	checks := assembleDoctor(f)
	if *asJSON {
		code := doctorExit(checks)
		emitMachine(stdout, doctorEnvelope(f, checks, code))
		return code
	}
	printDoctor(stdout, checks, f)
	return doctorExit(checks)
}

// gatherDoctorFacts reads each source once, keeping failures rather than
// returning on the first one — one unreadable file must not blank the
// report.
func gatherDoctorFacts(cfg *config.Config, src string, getenv func(string) string) doctorFacts {
	f := doctorFacts{ConfigSource: src, StateDir: cfg.Search.StateDir, Now: time.Now()}
	f.Update = gatherUpdateFacts(cfg, getenv)
	f.Binary = gatherBinaryFacts(doctorInstallationHome(src, getenv))
	if stateACL.managed {
		f.State = inspectStateAccess(cfg.Search.StateDir, auth.CredentialFiles())
	}
	switch creds, err := readCredentials(credentialsPath(cfg.Search)); {
	case err == nil:
		f.HasCredential = creds.APIKey != ""
		if cfg.Search.RouterURL != nil {
			if cerr := routerKeyService.check(creds, cfg.Search.RouterURL.String(), getenv); cerr != nil {
				f.CredentialErr = cerr
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		f.CredentialErr = err
	}
	store, err := auth.OpenStoreExisting(cfg.Search.StateDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			f.RegistrationErr, f.TraceKeyErr = err, err
		}
		return f
	}
	f.Registration, f.HasRegistration, f.RegistrationErr = store.LoadAgentRegistration()
	// LoadTraceKey never makes the key: doctor reports and changes nothing.
	f.HasTraceKey, f.TraceKeyErr = store.LoadTraceKey()
	return f
}

// doctorInstallationHome is the installation doctor is diagnosing: the
// loaded config's directory when the config is an installation's
// jevlin.toml, and otherwise the installation uninstall and upgrade resolve.
// "" when there is none to name.
func doctorInstallationHome(cfgSource string, getenv func(string) string) string {
	if cfgSource != "" && filepath.Base(cfgSource) == setupConfigFile {
		if abs, err := filepath.Abs(cfgSource); err == nil {
			return filepath.Dir(abs)
		}
	}
	userHome, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	home, err := resolveInstallationHome("", getenv, userHome, exe)
	if err != nil {
		return ""
	}
	return home
}

// doctorExit decides the process status.
//
// The exit status reports whether the DIAGNOSIS succeeded, not whether the
// news is good, and it is the same decision whichever renderer ran.
//
// The tempting design is the opposite — non-zero whenever any check is
// not OK — and it is wrong for this command in a way that only shows up
// in front of a new participant. The first thing anyone runs `doctor`
// for is an installation that has just been set up and not yet claimed,
// which is the state it exists to explain. Exiting 1 there tells a
// person, and every script wrapping this, that the command failed, when
// what actually happened is that it worked perfectly and the answer is
// "not claimed yet". A diagnostic that reports a correct diagnosis as its
// own failure teaches people to stop reading it.
//
// So `NO` is a successful run: the check ran and the answer is no. Only
// a report that could not be produced at all is a failure, and the way
// that shows is every check coming back UNKNOWN — nothing was reachable,
// so nothing was learned.
func doctorExit(checks []doctorCheck) int {
	for _, c := range checks {
		if c.Verdict != verdictUnknown {
			return exitOK
		}
	}
	return exitTransport
}

// The two column widths every line in the report shares.
const (
	doctorNameWidth    = 21
	doctorVerdictWidth = 7
)

func printDoctor(w io.Writer, checks []doctorCheck, f doctorFacts) {
	printConfigSource(w, f.ConfigSource, f.StateDir)
	var unknown []string
	for _, c := range checks {
		fmt.Fprintf(w, "  %-*s %-*s  %s\n", doctorNameWidth, c.Name, doctorVerdictWidth, c.Verdict, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "  %-*s %-*s  → %s\n", doctorNameWidth, "", doctorVerdictWidth, "", c.Fix)
		}
		if c.Verdict == verdictUnknown {
			unknown = append(unknown, c.Name)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(w, "\n  %d check(s) could not run: %s.\n", len(unknown), strings.Join(unknown, ", "))
		fmt.Fprintln(w, "  Everything above them was answered; nothing was assumed.")
	}
}

// doctorUpdateCheck reads the daily release check's stamp (update_check.go).
//
// NO is one fact: a newer release exists and this copy is not it, with the
// one command that applies it here. A check that never ran is OK with a
// date — a new installation's first check is a day away, and that is not a
// fault — but one that has not run for three days is UNKNOWN: the hooks
// that start it may not be running, and this check cannot tell. A failed,
// unfinished, unreadable or future-dated stamp is UNKNOWN with the reason,
// and a stamp from a development build has nothing to compare.
func doctorUpdateCheck(f doctorFacts) doctorCheck {
	c := doctorCheck{Name: "up to date"}
	u := f.Update
	modeNote := unknownModeNote(u.UnknownMode)
	undetermined := func(reason string) doctorCheck {
		c.Verdict, c.Detail = verdictUnknown, "could not determine — "+reason+modeNote
		return c
	}
	if u.Mode == config.UpdateOff {
		c.Verdict, c.Detail = verdictOK, "automatic release checks are off ([update] mode)"
		return c
	}
	if u.Err != nil {
		return undetermined("the update stamp could not be read: " + redact.Error(u.Err).Error())
	}
	if !u.Present {
		c.Verdict, c.Detail = verdictOK, "no release check has run yet; the agent hooks start one daily"+modeNote
		return c
	}
	st := u.Stamp
	age := f.Now.Sub(st.CheckedAt)
	if !u.thisCopy() {
		c.Verdict, c.Detail = verdictOK, fmt.Sprintf("no release check has run yet for this copy; the last, %s, was by another copy of jevlin%s", ageText(age), modeNote)
		return c
	}
	// Before the staleness rule: no check runs after this record, by design,
	// so its age says nothing about the hooks.
	if stampNeedsAPerson(st) {
		c.Verdict, c.Detail, c.Fix = verdictNo, manualInterventionDetail(ageText(age), u.Recovery), manualInterventionFix
		return c
	}
	switch {
	case age < 0:
		return undetermined("the update stamp is in the future")
	case age >= updateStaleAfter:
		c.Verdict = verdictUnknown
		c.Detail = fmt.Sprintf("could not determine — the last check ran %s; a check runs daily when an agent session starts%s", ageText(age), modeNote)
		c.Fix = u.Fix
		return c
	}
	switch st.Outcome {
	case updateCurrent:
		c.Verdict, c.Detail = verdictOK, fmt.Sprintf("%s is the latest release (checked %s)%s", st.Running, ageText(age), modeNote)
	case updateUpgraded:
		if st.Refresh == refreshFailed {
			c.Verdict, c.Detail = verdictNo, fmt.Sprintf("upgraded to %s %s, but the agent integrations could not be refreshed", st.Latest, ageText(age))
			c.Fix = "jevlin agents install, then restart open agents"
			return c
		}
		c.Verdict, c.Detail = verdictOK, fmt.Sprintf("upgraded to %s %s; restart open agents%s", st.Latest, ageText(age), modeNote)
	case updateRolledBack:
		c.Verdict, c.Detail = verdictOK, fmt.Sprintf("rolled back from %s to %s %s; %s is not installed again automatically, a newer release is%s", st.Held, st.Running, ageText(age), st.Held, modeNote)
	case updateAvailable:
		if st.Held != "" {
			c.Verdict, c.Detail = verdictOK, fmt.Sprintf("%s is the latest release, and this copy was rolled back from %s, so it is not installed automatically (checked %s); %s installs it%s", st.Latest, st.Held, ageText(age), u.Fix, modeNote)
			return c
		}
		c.Verdict, c.Detail = verdictNo, fmt.Sprintf("%s is available and this copy is %s (checked %s)%s", st.Latest, st.Running, ageText(age), modeNote)
		c.Fix = u.Fix
	case updateFailed:
		return undetermined(fmt.Sprintf("the last check failed (%s) %s; the next runs in a day", st.Failure, ageText(age)))
	case updateNotRelease:
		c.Verdict, c.Detail = verdictOK, fmt.Sprintf("not a release build; nothing to compare (checked %s)", ageText(age))
	default: // started
		if age < updateStartedGrace {
			return undetermined(fmt.Sprintf("a check started %s and is still running", ageText(age)))
		}
		return undetermined(fmt.Sprintf("a check started %s and never reported; the next runs in a day", ageText(age)))
	}
	return c
}
