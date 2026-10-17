package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// On Linux, Codex runs a command in its own PID namespace, so the flush a
// search starts dies with the search (issue #80, seen live with jevlin's own
// binary on Codex 0.158.0), and only a hook running outside the sandbox
// delivers. Two of Codex's hooks do: SessionStart and Stop. The check holds
// when one of ours is installed and neither is approved, and only on Linux.
// It claims nothing where the record cannot be told.
func TestCodexDeliveryIsUnapprovedOnlyOnLinuxWithNeitherDeliveringHookApproved(t *testing.T) {
	for _, c := range []struct {
		name   string
		goos   string
		config func(paths agentPaths) string
		hooks  bool
		want   bool
	}{
		{"linux, nothing approved", "linux", nil, true, true},
		{"linux, only a hook that does not deliver approved", "linux", func(p agentPaths) string { return approvalTables(p.codexHooks, "0", "pre_tool_use", "pre_compact") }, true, true},
		{"linux, SessionStart approved", "linux", func(p agentPaths) string { return approvalTables(p.codexHooks, "0", "session_start") }, true, false},
		{"linux, Stop approved", "linux", func(p agentPaths) string { return approvalTables(p.codexHooks, "0", "stop") }, true, false},
		{"linux, a config.toml that is not TOML", "linux", func(agentPaths) string { return "= = =\n" }, true, false},
		{"linux, no hooks of ours", "linux", nil, false, false},
		{"macOS, nothing approved", "darwin", nil, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ops, paths, entry := codexStatusMachine(t)
			if !c.hooks {
				delete(m.files, slash(paths.codexHooks))
			}
			if c.config != nil {
				m.files[slash(paths.codexConfig)] = []byte(c.config(paths))
			}
			if got := codexDeliveryUnapproved(ops, paths, entry, c.goos); got != c.want {
				t.Fatalf("codexDeliveryUnapproved = %v, want %v", got, c.want)
			}
		})
	}
}

// codexMiningIs says mining is on, or off, for every installation, for the
// test's length.
func codexMiningIs(t *testing.T, on bool) {
	t.Helper()
	was := codexMiningOn
	codexMiningOn = func(string, func(string) string) bool { return on }
	t.Cleanup(func() { codexMiningOn = was })
}

// agents status says it under Codex's row, and only with mining on: a
// participant who has not turned mining on has nothing waiting to earn.
// StatusNotes is skipped on an OS where Codex gets no hooks at all, since
// it answers nothing about hooks there.
func TestAgentsStatusWarnsWhenCodexSearchesCannotBeDelivered(t *testing.T) {
	if _, err := codexHooksFor(codexTarget{}, goldenEntry(), runtime.GOOS); err != nil {
		t.Skipf("no Codex hooks are written here: %v", err)
	}
	onCodexOS(t, "linux")
	for _, on := range []bool{true, false} {
		codexMiningIs(t, on)
		_, ops, paths, entry := codexStatusMachine(t)
		notes := strings.Join(codexTarget{}.StatusNotes(ops, paths, entry, os.Getenv), "\n")
		if got := strings.Contains(notes, "delivery: "+codexUndeliveredLine); got != on {
			t.Errorf("mining on=%v: the delivery warning shown=%v, in:\n%s", on, got, notes)
		}
	}
}

// status shows what is waiting and since when, and, when Codex on Linux is
// why, the same sentence as agents status; status -json carries both.
func TestStatusSaysWhatIsWaitingAndWhenCodexIsHoldingIt(t *testing.T) {
	oldest := time.Date(2026, 10, 16, 13, 45, 25, 0, time.UTC)
	enabled := auth.MiningDecision{State: auth.MiningEnabled}
	// A connected participant's status takes the claimed-registration path,
	// which prints the lines from its own place; both paths are held.
	claimed := func(f agentIdentityFacts) agentIdentityFacts {
		f.HasRegistration = true
		f.Registration = auth.AgentRegistration{AgentID: "agent-1", Status: "claimed", Scopes: []string{"credits", "mining"}}
		return f
	}
	for _, c := range []struct {
		name          string
		f             agentIdentityFacts
		waiting, held bool
	}{
		{"waiting, held by Codex", agentIdentityFacts{Decision: enabled, Waiting: 3, OldestWaiting: oldest, CodexUndelivered: true}, true, true},
		{"waiting, nothing holding it", agentIdentityFacts{Decision: enabled, Waiting: 3, OldestWaiting: oldest}, true, false},
		{"nothing waiting", agentIdentityFacts{Decision: enabled}, false, false},
		{"claimed, waiting, held by Codex", claimed(agentIdentityFacts{Decision: enabled, Waiting: 3, OldestWaiting: oldest, CodexUndelivered: true}), true, true},
		{"claimed, nothing waiting", claimed(agentIdentityFacts{Decision: enabled}), false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			renderAgentIdentity(c.f, &out, &errOut)
			text := out.String()
			if got := strings.Contains(text, "waiting: 3 search(es) recorded and not yet delivered, the oldest made 2026-10-16T13:45:25Z"); got != c.waiting {
				t.Errorf("waiting line shown=%v, want %v:\n%s", got, c.waiting, text)
			}
			if got := strings.Contains(text, "delivery: "+codexUndeliveredLine); got != c.held {
				t.Errorf("Codex line shown=%v, want %v:\n%s", got, c.held, text)
			}
			var env struct {
				Data struct {
					Delivery *statusDeliveryJSON `json:"delivery"`
				} `json:"data"`
			}
			b, err := json.Marshal(statusEnvelope(c.f, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &env); err != nil {
				t.Fatal(err)
			}
			d := env.Data.Delivery
			if d == nil || d.Waiting == nil || *d.Waiting != c.f.Waiting || d.WaitingUnknown || d.CodexHooksUnapproved != c.held {
				t.Fatalf("status -json delivery = %+v, want waiting %d and unapproved %v", d, c.f.Waiting, c.held)
			}
		})
	}

	// An intake that could not be read is an unknown count, said as such,
	// never 0: a model reads 0 as nothing waiting.
	unread := agentIdentityFacts{Decision: enabled, WaitingErr: errors.New("injected")}
	var out, errOut bytes.Buffer
	renderAgentIdentity(unread, &out, &errOut)
	if !strings.Contains(out.String(), "waiting: unknown: the intake directory could not be read") {
		t.Errorf("an unread intake:\n%s", out.String())
	}
	b, err := json.Marshal(statusEnvelope(unread, nil))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data struct {
			Delivery map[string]any `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	if _, has := env.Data.Delivery["waiting"]; has || env.Data.Delivery["waiting_unknown"] != true {
		t.Errorf("status -json for an unread intake: %v", env.Data.Delivery)
	}
}

// status counts the searches waiting in intake, reads the Codex check, and
// does neither with mining off.
func TestStatusCountsTheSearchesWaitingInIntake(t *testing.T) {
	was := statusCodexUndelivered
	statusCodexUndelivered = func(string, func(string) string) bool { return true }
	t.Cleanup(func() { statusCodexUndelivered = was })

	f := newFlushFixture(t, newFakeAS(t))
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "waiting-1")
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "waiting-2")
	facts, ok := gatherAgentIdentity([]string{"-config", f.cfgPath}, os.Getenv)
	if !ok {
		t.Fatal("status gathered nothing")
	}
	if facts.Waiting != 2 || facts.OldestWaiting.IsZero() || !facts.CodexUndelivered {
		t.Fatalf("Waiting = %d, OldestWaiting = %v, CodexUndelivered = %v; want 2, a time, true", facts.Waiting, facts.OldestWaiting, facts.CodexUndelivered)
	}

	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMiningEnabled(false); err != nil {
		t.Fatal(err)
	}
	facts, _ = gatherAgentIdentity([]string{"-config", f.cfgPath}, os.Getenv)
	if facts.Waiting != 0 || facts.CodexUndelivered {
		t.Fatalf("with mining off: Waiting = %d, CodexUndelivered = %v; want neither read", facts.Waiting, facts.CodexUndelivered)
	}
}

// The count does not depend on the registration: an agent.json that cannot
// be read still leaves what waits in intake counted. Before, status returned
// before the count, and status -json said "waiting": 0.
func TestStatusCountsTheSearchesWaitingWhateverTheRegistration(t *testing.T) {
	was := statusCodexUndelivered
	statusCodexUndelivered = func(string, func(string) string) bool { return false }
	t.Cleanup(func() { statusCodexUndelivered = was })

	f := newFlushFixture(t, newFakeAS(t))
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "waiting-1")
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "waiting-2")
	writeFileT(t, filepath.Join(f.cfg.Mining.StateDir, "agent.json"), "{not json")
	facts, ok := gatherAgentIdentity([]string{"-config", f.cfgPath}, os.Getenv)
	if !ok || facts.RegistrationErr == nil {
		t.Fatalf("setup: ok=%v, registration error %v; want a corrupt registration", ok, facts.RegistrationErr)
	}
	if facts.Waiting != 2 || facts.WaitingErr != nil {
		t.Errorf("with agent.json corrupt: Waiting = %d, WaitingErr = %v; want 2 counted", facts.Waiting, facts.WaitingErr)
	}
}

// An intake that cannot be read is an unknown count where the count is
// taken, not only where it is printed: the gatherer keeps the read error,
// and status -json says waiting_unknown, never "waiting": 0. A regular file
// where the directory should be makes the read fail with ENOTDIR on Linux
// and macOS. Not on Windows: there Go's syscall.ENOTDIR is
// ERROR_PATH_NOT_FOUND, which is fs.ErrNotExist, so readIntake reads the
// file as an intake not yet created. The gatherer is the same code there.
func TestStatusKeepsTheErrorOfAnIntakeItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows a file read as a directory is ERROR_PATH_NOT_FOUND, which readIntake takes for no intake yet")
	}
	was := statusCodexUndelivered
	statusCodexUndelivered = func(string, func(string) string) bool { return false }
	t.Cleanup(func() { statusCodexUndelivered = was })

	f := newFlushFixture(t, newFakeAS(t))
	if err := os.RemoveAll(f.cfg.Miner.IntakeDir); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, f.cfg.Miner.IntakeDir, "not a directory")
	if _, err := os.ReadDir(f.cfg.Miner.IntakeDir); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("fixture: reading the intake gives %v; want an error other than not-found", err)
	}
	facts, ok := gatherAgentIdentity([]string{"-config", f.cfgPath}, os.Getenv)
	if !ok {
		t.Fatal("status gathered nothing")
	}
	if facts.WaitingErr == nil {
		t.Fatalf("an intake that is a file: Waiting = %d and no error kept; want the count unknown", facts.Waiting)
	}
	b, err := json.Marshal(statusEnvelope(facts, nil))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data struct {
			Delivery map[string]any `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	if _, has := env.Data.Delivery["waiting"]; has || env.Data.Delivery["waiting_unknown"] != true {
		t.Errorf("status -json for an intake that is a file: %v", env.Data.Delivery)
	}
}

// A record with no time says nothing about when the oldest search was made.
// Read after a timed record, it used to put no time in that record's place.
// writeIntake names a record by when it finished, so the untimed one finishes
// later to be read last, and the test checks that it is.
func TestTheOldestWaitingTimeSkipsARecordWithNoTime(t *testing.T) {
	was := statusCodexUndelivered
	statusCodexUndelivered = func(string, func(string) string) bool { return false }
	t.Cleanup(func() { statusCodexUndelivered = was })

	f := newFlushFixture(t, newFakeAS(t))
	timed := time.Date(2026, 10, 16, 13, 45, 25, 0, time.UTC)
	for _, rec := range []intakeRecord{
		{RequestID: "timed", StatusCode: http.StatusOK, StartedAt: timed, FinishedAt: timed},
		{RequestID: "untimed", StatusCode: http.StatusOK, FinishedAt: timed.Add(time.Minute)},
	} {
		if _, err := writeIntake(f.cfg.Miner.IntakeDir, rec); err != nil {
			t.Fatal(err)
		}
	}
	records, _, err := readIntake(f.cfg.Miner.IntakeDir)
	if err != nil || len(records) != 2 || records[1].rec.RequestID != "untimed" {
		t.Fatalf("fixture: %d record(s), err %v; want two, the untimed one read last", len(records), err)
	}
	facts, ok := gatherAgentIdentity([]string{"-config", f.cfgPath}, os.Getenv)
	if !ok {
		t.Fatal("status gathered nothing")
	}
	if facts.Waiting != 2 || !facts.OldestWaiting.Equal(timed) {
		t.Fatalf("Waiting = %d, OldestWaiting = %v; want 2, and %v from the one timed record", facts.Waiting, facts.OldestWaiting, timed)
	}
}

// agents install says it where the participant acts on it: on Linux, with
// mining on, when it writes Codex's hooks. Not with mining off, and not on
// another OS. Skipped where Codex gets no hooks at all.
func TestCodexInstallSaysSearchesWaitForTheHooksApproval(t *testing.T) {
	if _, err := codexHooksFor(codexTarget{}, goldenEntry(), runtime.GOOS); err != nil {
		t.Skipf("no Codex hooks are written here: %v", err)
	}
	const note = "until you approve these hooks in Codex, a search made only in Codex is not delivered"
	for _, c := range []struct {
		name string
		goos string
		on   bool
		want bool
	}{
		{"linux, mining on", "linux", true, true},
		{"linux, mining off", "linux", false, false},
		{"macOS, mining on", "darwin", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			onCodexOS(t, c.goos)
			codexMiningIs(t, c.on)
			_, ops := newFakeMachine("codex")
			code, out, _ := runAgents(t, ops, nil, "install", "-client", "codex", "-config", testCfg, "-yes")
			if code != exitOK {
				t.Fatalf("install: %d\n%s", code, out)
			}
			if got := strings.Contains(out, note); got != c.want {
				t.Errorf("install note shown=%v, want %v:\n%s", got, c.want, out)
			}
		})
	}
}
