package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	return writeTOML(t, "[search]\nstate_dir = "+strconv.Quote(stateDir)+"\n")
}

// doctor diagnoses and changes nothing: the state directory and the
// installation directory around it are byte for byte what it found, in
// both of its modes. The fixture is a registered installation whose trace
// key is removed, the state of one made before connect wrote the key:
// doctor used to make it there, through the store's creating read
// (Store.TraceKey), so a command that only reports wrote a secret into the
// directory it was asked about, and the "trace key" check could never say
// "not made yet" because asking had made it. Store.LoadTraceKey is the
// reading that does not create, and this holds doctor to it.
func TestDoctorLeavesTheInstallationAsItFoundIt(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("registering connect failed: %d\n%s\n%s", code, out, errOut)
	}
	home := filepath.Dir(stateDir)
	if err := os.Remove(auth.TraceKeyPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	// cmdDoctor reads the environment itself; the installation it
	// diagnoses is this one, and nothing of the developer's.
	t.Setenv("JEVLIN_HOME", home)
	t.Setenv("JEVLIN_CONFIG", "")

	before := snapshotTree(t, home)
	for _, args := range [][]string{{"-config", cfgPath}, {"-config", cfgPath, "-json"}} {
		var out, errOut bytes.Buffer
		if code := cmdDoctor(args, &out, &errOut); code != exitOK {
			t.Fatalf("doctor %v exited %d\n%s\n%s", args, code, out.String(), errOut.String())
		}
		if !strings.Contains(out.String(), "not made yet") {
			t.Errorf("doctor %v does not report the trace key as not made yet:\n%s", args, out.String())
		}
		if lexists(auth.TraceKeyPath(stateDir)) {
			t.Errorf("doctor %v made the trace key", args)
		}
		after := snapshotTree(t, home)
		if !reflect.DeepEqual(before, after) {
			var changed []string
			for path := range before {
				if sig, ok := after[path]; !ok || sig != before[path] {
					changed = append(changed, path+" (changed or removed)")
				}
			}
			for path := range after {
				if _, ok := before[path]; !ok {
					changed = append(changed, path+" (made)")
				}
			}
			sort.Strings(changed)
			t.Fatalf("doctor %v changed the installation:\n  %s", args, strings.Join(changed, "\n  "))
		}
	}
}
