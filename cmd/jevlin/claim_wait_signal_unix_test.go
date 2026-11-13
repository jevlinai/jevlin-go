//go:build !windows

package main

// What Ctrl-C at the claim wait really does, which is what the wait line
// (claimWaitMessage) says it does. The interrupt is a real SIGINT to this
// process, sent only once the wait line is on stdout: that line is printed
// inside the poll loop, after operatorContext has taken SIGINT, so the
// signal cannot reach Go's default handler and end the test binary. A run
// that never prints the line is never signaled; it fails after its budget.

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

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

// interruptAtClaimWait runs run with a budget long enough to be interrupted
// in, sends SIGINT once the wait line is printed, and returns what run
// returned and printed.
func interruptAtClaimWait(t *testing.T, run func(stdout, stderr io.Writer) int) (code int, stdout, stderr string) {
	t.Helper()
	saved := connectPollBudget
	connectPollBudget = 5 * time.Second
	defer func() { connectPollBudget = saved }()
	var out, errOut lockedBuffer
	done := make(chan int, 1)
	go func() { done <- run(&out, &errOut) }()
	seen := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(out.String(), claimWaitPrefix) {
			seen = true
			break
		}
	}
	if !seen {
		code = <-done
		t.Fatalf("the run never announced its wait, so it was not interrupted (exit %d):\n%s\n%s", code, out.String(), errOut.String())
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	code = <-done
	return code, out.String(), errOut.String()
}

// Setup's line says Ctrl-C ends setup before its environment and coding
// agents steps, and that running setup again finishes them.
func TestCtrlCAtSetupsClaimWaitEndsSetupBeforeItsLastStepsAndARerunFinishes(t *testing.T) {
	s := newSetupSandbox(t)
	s.onPath["claude"] = true
	skill := s.paths().claudeSkill

	code, out, errOut := interruptAtClaimWait(t, func(stdout, stderr io.Writer) int {
		return setupMain(s.deps(&ttyReader{}, stdout, stderr, false), []string{"-yes"})
	})
	if code == exitOK {
		t.Fatalf("an interrupted claim wait let setup succeed:\n%s\n%s", out, errOut)
	}
	if !strings.Contains(out, claimWaitMessage(5*time.Second, true)) || !strings.Contains(errOut, "connect: interrupted") {
		t.Fatalf("not the interrupt this test is about:\n%s\n%s", out, errOut)
	}
	if strings.Contains(out, "Shell environment") || strings.Contains(out, "Coding agents") {
		t.Errorf("setup went on past the interrupted wait:\n%s", out)
	}
	if lexists(skill) || fileContains(t, s.profilePath(), profileMarkerStart) {
		t.Errorf("the environment or agents step ran after the interrupt")
	}

	s.platform.claim("credits")
	code, out, errOut = s.run(nil, false, "-yes")
	if code != exitOK {
		t.Fatalf("setup run again exited %d\n%s\n%s", code, out, errOut)
	}
	if !lexists(skill) || !fileContains(t, s.profilePath(), profileMarkerStart) {
		t.Errorf("running setup again did not finish the environment and agents steps:\n%s", out)
	}
	if registers, _, _ := s.platform.counts(); registers != 1 {
		t.Errorf("setup run again registered %d times in all, want the one registration kept", registers)
	}
}

// A bare connect's line says Ctrl-C stops the wait and the next search or
// connect completes the claim: the registration and the key stay, and a
// later connect, or the resume a search starts, finds the claim.
func TestCtrlCAtConnectsClaimWaitLeavesTheClaimToTheNextRun(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")

	code, out, errOut := interruptAtClaimWait(t, func(stdout, stderr io.Writer) int {
		args := []string{"-config", cfgPath}
		return cmdConnect(args, &bytes.Buffer{}, stdout, stderr, installedAs(noEnv, args))
	})
	if code == exitOK || !strings.Contains(out, claimWaitMessage(5*time.Second, false)) || !strings.Contains(errOut, "connect: interrupted") {
		t.Fatalf("not the interrupt this test is about (exit %d):\n%s\n%s", code, out, errOut)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.Status != "unclaimed" {
		t.Fatalf("the interrupt lost the registration: %+v %v", reg, ok)
	}

	platform.claim("credits")
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("the resume a search starts exited %d: %s", code, errOut)
	}
	if reg, _ := loadAgent(t, stateDir); reg.Status != "claimed" {
		t.Errorf("the resume did not complete the claim: %+v", reg)
	}
	code, out, _ = runConnect(t, cfgPath, nil)
	if code != exitOK || !strings.Contains(out, "claimed (scopes: credits)") {
		t.Errorf("the next connect did not report the claim (exit %d):\n%s", code, out)
	}
	if registers, _, _ := platform.counts(); registers != 1 {
		t.Errorf("registered %d times, want the one registration kept", registers)
	}
}
