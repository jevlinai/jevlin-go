package main

// After a claim, searches stay on the key's default tier, fast, and nothing
// in setup, connect or the skill said that claiming opened the others. A
// foreground connect that sees the agent become claimed now says which tiers
// opened and how to choose one, in the text form only.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// The line names exactly the tiers a claim opens, as the agents help marks
// them, and both ways to choose one.
func TestTheTiersLineNamesOnlyTheTiersAClaimOpens(t *testing.T) {
	opening := claimedTiersMessage[:strings.Index(claimedTiersMessage, ";")]
	for _, tier := range config.SearchTiers {
		claimedOnly := strings.Contains(tierLabel(tier), "claimed agents only")
		if named := strings.Contains(opening, tier); named != claimedOnly {
			t.Errorf("tier %s: claimed agents only = %v, named as opened by the claim = %v: %q", tier, claimedOnly, named, opening)
		}
	}
	for _, how := range []string{"`jevlin agents tier <tier>`", "`/jevlin tier <tier>`"} {
		if !strings.Contains(claimedTiersMessage, how) {
			t.Errorf("the line does not name %s: %q", how, claimedTiersMessage)
		}
	}
	if strings.Contains(claimedTiersMessage, "\n") {
		t.Errorf("the tiers pointer is one line: %q", claimedTiersMessage)
	}
}

// A connect run while the agent is recorded unclaimed, that finds it claimed
// (from /me, before it polls), says so and points to the tiers, once, right
// after the claim.
func TestAConnectThatSeesTheClaimPointsToTheTiers(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, claimedTiersMessage) {
		t.Errorf("an agent still unclaimed was pointed to the tiers a claim opens:\n%s", out)
	}

	platform.claim("credits")
	code, out, errOut = runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if n := strings.Count(out, claimedTiersMessage); n != 1 || !strings.Contains(out, "claimed (scopes: credits)\n"+claimedTiersMessage+"\n") {
		t.Errorf("the claim is followed by the tiers pointer once (%d):\n%s", n, out)
	}
}

// The claim happens while connect waits: the poll that finds it is followed
// by the pointer.
func TestAClaimDuringTheWaitPointsToTheTiers(t *testing.T) {
	withShortConnectTimings(t)
	connectPollBudget = 5 * time.Second
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	var out, errOut lockedBuffer
	done := make(chan int, 1)
	go func() {
		args := []string{"-config", cfgPath}
		done <- cmdConnect(args, &bytes.Buffer{}, &out, &errOut, installedAs(noEnv, args))
	}()
	if !awaitClaimWait(&out, 4*time.Second) {
		t.Fatalf("connect never waited (exit %d):\n%s\n%s", <-done, out.String(), errOut.String())
	}
	platform.claim("credits")
	if code := <-done; code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out.String(), errOut.String())
	}
	got := out.String()
	wait := strings.Index(got, claimWaitPrefix)
	if n := strings.Count(got, claimedTiersMessage); n != 1 || !strings.Contains(got[wait:], "claimed (scopes: credits)\n"+claimedTiersMessage+"\n") {
		t.Errorf("the claim found by the poll is followed by the tiers pointer once (%d):\n%s", n, got)
	}
}

// A claim the poll finds but cannot record is not announced: the record
// still says unclaimed, connect exits non-zero naming the failed save, and
// the tiers pointer, which says the claim opened them, is not printed. The
// save fails because agent.json's name is taken by a non-empty directory,
// put there by the stub between answering "claimed" and the client saving
// it, on every OS (no permission bits, which a root runner ignores).
func TestAClaimThatCouldNotBeSavedDoesNotPointToTheTiers(t *testing.T) {
	withShortConnectTimings(t)
	connectPollBudget = 5 * time.Second
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	var swapErr error
	swapped := false
	platform.mu.Lock()
	platform.statusHook = func(status string) {
		// Called with the stub's lock released; taken here so the test reads
		// what it wrote.
		platform.mu.Lock()
		defer platform.mu.Unlock()
		if status != "claimed" || swapped {
			return
		}
		swapped = true
		record := filepath.Join(stateDir, "agent.json")
		if swapErr = os.Remove(record); swapErr == nil {
			if swapErr = os.Mkdir(record, 0o700); swapErr == nil {
				swapErr = os.WriteFile(filepath.Join(record, "keep"), []byte("x"), 0o600)
			}
		}
	}
	platform.mu.Unlock()
	var out, errOut lockedBuffer
	done := make(chan int, 1)
	go func() {
		args := []string{"-config", cfgPath}
		done <- cmdConnect(args, &bytes.Buffer{}, &out, &errOut, installedAs(noEnv, args))
	}()
	if !awaitClaimWait(&out, 4*time.Second) {
		t.Fatalf("connect never waited (exit %d):\n%s\n%s", <-done, out.String(), errOut.String())
	}
	platform.claim("credits")
	code := <-done
	platform.mu.Lock()
	gotSwap, err := swapped, swapErr
	platform.mu.Unlock()
	if !gotSwap || err != nil {
		t.Fatalf("the save was never made to fail (swapped %v: %v), so nothing was tested", gotSwap, err)
	}
	if code != exitTransport {
		t.Errorf("connect exited %d after failing to record the claim, want %d\n%s\n%s", code, exitTransport, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), claimedTiersMessage) {
		t.Errorf("a claim that was not recorded pointed to the tiers it opened:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "jevlin:") {
		t.Errorf("the failed save was not named on stderr:\n%s", errOut.String())
	}
}

// Setup's connect is the same run: a setup that sees the claim points to
// the tiers too.
func TestSetupThatSeesTheClaimPointsToTheTiers(t *testing.T) {
	s := newSetupSandbox(t)
	if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK || strings.Contains(out, claimedTiersMessage) {
		t.Fatalf("first setup (exit %d) on an unclaimed agent:\n%s\n%s", code, out, errOut)
	}
	s.platform.claim("credits")
	code, out, errOut := s.run(nil, false, "-yes")
	if code != exitOK {
		t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
	}
	if strings.Count(out, claimedTiersMessage) != 1 {
		t.Errorf("setup's connect saw the claim and did not point to the tiers once:\n%s", out)
	}
}

// Text only, and only for a claim this run saw happen: -json carries one
// envelope, -resume prints to nobody, and a record already claimed when the
// run began was not claimed by anything this run watched.
func TestTheTiersPointerIsForATextRunThatSawTheClaim(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect: %s", errOut)
	}
	platform.claim("credits")

	code, out, errOut := runConnect(t, cfgPath, nil, "-json")
	if code != exitOK || strings.Contains(out+errOut, "balanced and max") {
		t.Errorf("-json (exit %d) carries the tiers pointer:\n%s\n%s", code, out, errOut)
	}
	if reg, _ := loadAgent(t, stateDir); reg.Status != "claimed" {
		t.Fatalf("the -json run did not record the claim: %+v", reg)
	}
	code, out, errOut = runConnect(t, cfgPath, nil)
	if code != exitOK || !strings.Contains(out, "claimed (scopes: credits)") || strings.Contains(out, claimedTiersMessage) {
		t.Errorf("a connect for an agent already recorded as claimed (exit %d) repeats the pointer:\n%s\n%s", code, out, errOut)
	}

	platform2 := newStubPlatform(t)
	cfgPath2, _ := connectConfig(t, platform2.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath2, nil); code != exitOK {
		t.Fatalf("connect: %s", errOut)
	}
	platform2.claim("credits")
	code, out, errOut = runConnect(t, cfgPath2, nil, "-resume")
	if code != exitOK || strings.Contains(out+errOut, "balanced and max") {
		t.Errorf("-resume (exit %d) carries the tiers pointer:\n%s\n%s", code, out, errOut)
	}
}
