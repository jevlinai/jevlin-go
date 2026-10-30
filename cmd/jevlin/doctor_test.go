package main

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// healthyNow is the moment healthyFacts reports at; a stamp's age is
// measured from it.
var healthyNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// healthyFacts is an installation with nothing wrong: a key on file, a
// claimed registration, a trace key, and a release check that found this
// copy current an hour ago. The stamp names this binary and this version,
// so it is this copy's (stampIsThisCopy).
func healthyFacts() doctorFacts {
	now := healthyNow
	return doctorFacts{
		ConfigSource:    "/home/u/.jevlin/jevlin.toml",
		StateDir:        "/home/u/.jevlin/state",
		HasCredential:   true,
		HasRegistration: true,
		Registration:    auth.AgentRegistration{AgentID: "agent-1", Status: "claimed", Scopes: []string{"search"}},
		HasTraceKey:     true,
		Update: updateFacts{Mode: "apply", Present: true, Build: "v0.7.0", Binary: "/home/u/.jevlin/bin/jevlin", Stamp: updateStamp{
			V: 1, CheckedAt: now.Add(-time.Hour), Binary: "/home/u/.jevlin/bin/jevlin", Running: "v0.7.0", Latest: "v0.7.0", Outcome: updateCurrent,
		}},
		Now: now,
	}
}

func verdictOf(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, checks)
	return doctorCheck{}
}

func TestAHealthyInstallationHasAllChecksOK(t *testing.T) {
	f := healthyFacts()
	for _, c := range assembleDoctor(f) {
		if c.Verdict != verdictOK {
			t.Errorf("%s: %s %s", c.Name, c.Verdict, c.Detail)
		}
	}
}

// Every input that can be absent or unreadable produces its own verdict,
// and an unreadable one is never read as absent: NO with the reason, never
// a polite OK.
func TestDoctorVerdictsOverTheInputDomain(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*doctorFacts)
		check   string
		verdict doctorVerdict
		detail  string
		fix     string
	}{
		{"no key", func(f *doctorFacts) { f.HasCredential = false }, "search key", verdictNo, "no key is stored", "jevlin connect"},
		{"key unusable", func(f *doctorFacts) { f.CredentialErr = errors.New("credentials: readable by others") }, "search key", verdictNo, "readable by others", "jevlin login"},
		{"unregistered", func(f *doctorFacts) { f.HasRegistration = false }, "registration", verdictOK, "not registered", ""},
		{"unclaimed", func(f *doctorFacts) { f.Registration.Status = "unclaimed" }, "registration", verdictNo, "not yet claimed", "claim link"},
		{"expired", func(f *doctorFacts) { f.Registration.Status = "expired" }, "registration", verdictNo, "expired", "jevlin connect"},
		{"corrupt record", func(f *doctorFacts) { f.RegistrationErr = auth.ErrAgentRegistrationCorrupt }, "registration", verdictNo, "could not be read", "rebuilds it"},
		{"no trace key", func(f *doctorFacts) { f.HasTraceKey = false }, "trace key", verdictOK, "not made yet", ""},
		{"trace key unreadable", func(f *doctorFacts) { f.TraceKeyErr = errors.New("trace.key is a symlink") }, "trace key", verdictNo, "symlink", "jevlin setup"},
		{"release available", func(f *doctorFacts) {
			f.Update.Stamp.Latest, f.Update.Stamp.Outcome, f.Update.Fix = "v0.8.0", updateAvailable, "jevlin upgrade"
		}, "up to date", verdictNo, "v0.8.0 is available", "jevlin upgrade"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := healthyFacts()
			tc.mutate(&f)
			c := verdictOf(t, assembleDoctor(f), tc.check)
			if c.Verdict != tc.verdict || !strings.Contains(c.Detail, tc.detail) || !strings.Contains(c.Fix, tc.fix) {
				t.Fatalf("%s = %+v, want %s containing %q with fix containing %q", tc.check, c, tc.verdict, tc.detail, tc.fix)
			}
		})
	}
}

// NO is a successful diagnosis; only a report with nothing but UNKNOWN in
// it fails the command.
func TestDoctorExitReportsWhetherTheDiagnosisRan(t *testing.T) {
	if doctorExit([]doctorCheck{{Verdict: verdictNo}, {Verdict: verdictUnknown}}) != exitOK {
		t.Error("a NO beside an UNKNOWN exited non-zero")
	}
	if doctorExit([]doctorCheck{{Verdict: verdictUnknown}}) != exitTransport {
		t.Error("a report of nothing but UNKNOWN exited zero")
	}
}

// The text report names the checks that could not run, and the JSON report
// lists them under their own key, from the same facts.
func TestTheReportNamesTheChecksThatCouldNotRun(t *testing.T) {
	f := healthyFacts()
	f.Update.Err = errors.New("stamp: not JSON")
	checks := assembleDoctor(f)
	var out bytes.Buffer
	printDoctor(&out, checks, f)
	if !strings.Contains(out.String(), "1 check(s) could not run: up to date") {
		t.Fatalf("text report:\n%s", out.String())
	}
	env := doctorEnvelope(f, checks, doctorExit(checks))
	report, ok := env.Data.(doctorReport)
	if !ok || len(report.ChecksCannotRun) != 1 || report.ChecksCannotRun[0] != "up to date" {
		t.Fatalf("json report: %+v", env.Data)
	}
}

// doctorConfig writes a config naming stateDir as the state directory, for
// a doctor run against a real installation layout.
func doctorConfig(t *testing.T, stateDir string) string {
	t.Helper()
	return writeTOML(t, "[miner]\nstate_dir = "+strconv.Quote(stateDir)+"\n")
}
