package main

// The line a foreground connect prints when it starts waiting for the claim.
// A new user saw the claim link and then nothing for three minutes, could not
// tell whether anything was happening, and pressed Ctrl-C, which in setup
// stopped setup before its agents step. The line says that it is waiting,
// for how long (the loop's own budget, not a number typed into a sentence),
// and what Ctrl-C does there: in setup it ends setup before the environment
// and coding agents steps, on its own it stops the wait and leaves the claim
// to the next search or connect. claim_wait_signal_unix_test.go holds both
// sentences to what an interrupt really does.

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

const claimWaitPrefix = "Waiting up to "

// lockedBuffer is a writer a run can print to while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// awaitClaimWait reports whether out shows the wait line within limit.
func awaitClaimWait(out *lockedBuffer, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(out.String(), claimWaitPrefix) {
			return true
		}
	}
	return false
}

func TestTheBudgetIsSpelledTheWayAPersonReadsIt(t *testing.T) {
	for d, want := range map[time.Duration]string{
		3 * time.Minute:         "3 minutes",
		time.Minute:             "1 minute",
		90 * time.Second:        "90 seconds",
		time.Second:             "1 second",
		300 * time.Millisecond:  "300ms",
		1500 * time.Millisecond: "1.5s",
	} {
		if got := waitBudgetText(d); got != want {
			t.Errorf("waitBudgetText(%v) = %q, want %q", d, got, want)
		}
	}
	if got := claimWaitMessage(3*time.Minute, false); !strings.HasPrefix(got, "Waiting up to 3 minutes for the claim. ") {
		t.Errorf("the production budget reads as three minutes: %q", got)
	}
}

// A bare connect that waits says so once, after the claim steps and before
// the timeout, with the budget the loop keeps and the bare connect's Ctrl-C.
func TestConnectSaysItIsWaitingForTheClaimAndWhatCtrlCDoes(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	line := claimWaitMessage(connectPollBudget, false)
	if n := strings.Count(out, claimWaitPrefix); n != 1 {
		t.Fatalf("the wait is announced %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "\n"+line+"\n") {
		t.Fatalf("the wait line is not the bare connect's, on a line of its own:\nwant %q\n%s", line, out)
	}
	if !strings.Contains(line, "up to "+waitBudgetText(connectPollBudget)+" ") || waitBudgetText(connectPollBudget) != "300ms" {
		t.Errorf("the line names the loop's own budget (%v): %q", connectPollBudget, line)
	}
	steps, wait, timeout := strings.Index(out, "To claim it:"), strings.Index(out, line), strings.Index(out, claimTimeoutMessage)
	if steps < 0 || timeout < 0 || steps >= wait || wait >= timeout {
		t.Errorf("the wait line comes after the claim steps and before the timeout (%d, %d, %d):\n%s", steps, wait, timeout, out)
	}
	if strings.Contains(out, "setup") {
		t.Errorf("a bare connect's wait line speaks of setup:\n%s", out)
	}
}

// Setup's connect says what Ctrl-C does to setup, and that a wait that runs
// out lets setup go on.
func TestSetupSaysItIsWaitingForTheClaimAndThatCtrlCEndsSetup(t *testing.T) {
	s := newSetupSandbox(t)
	code, out, errOut := s.run(nil, false, "-yes")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	line := claimWaitMessage(connectPollBudget, true)
	if n := strings.Count(out, claimWaitPrefix); n != 1 {
		t.Fatalf("the wait is announced %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "\n"+line+"\n") {
		t.Fatalf("the wait line is not setup's:\nwant %q\n%s", line, out)
	}
	if strings.Contains(out, claimWaitMessage(connectPollBudget, false)) {
		t.Errorf("setup printed the bare connect's Ctrl-C sentence:\n%s", out)
	}
	if wait, next := strings.Index(out, line), strings.Index(out, "Shell environment"); next >= 0 && wait > next {
		t.Errorf("the wait line comes before the steps setup goes on to:\n%s", out)
	}
}

// Text only: -json emits one envelope, -resume is a detached child whose
// output goes nowhere and never waits, and a run whose first poll settles the
// claim does not wait at all.
func TestTheClaimWaitIsAnnouncedOnlyWhenATextRunWaits(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")

	code, out, errOut := runConnect(t, cfgPath, nil, "-json")
	if code != exitOK || strings.Contains(out+errOut, claimWaitPrefix) {
		t.Errorf("-json (exit %d) announces the wait:\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runConnect(t, cfgPath, nil, "-resume")
	if code != exitOK || strings.Contains(out+errOut, claimWaitPrefix) {
		t.Errorf("-resume (exit %d) announces the wait:\n%s\n%s", code, out, errOut)
	}
	platform.claim("credits")
	code, out, errOut = runConnect(t, cfgPath, nil)
	if code != exitOK || !strings.Contains(out, "claimed (scopes: credits)") {
		t.Fatalf("connect for a claimed agent exited %d:\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, claimWaitPrefix) {
		t.Errorf("a connect whose first poll found the claim announces a wait:\n%s", out)
	}
}
