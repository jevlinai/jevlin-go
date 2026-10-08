package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The claim link is the one thing this client tells a person to open, and
// agent.json is in the state directory a sandboxed command can rewrite.
// These hold the link to the claim record beside credentials.json, to the
// agent the stored key belongs to, and to a platform that has just said
// that agent is unclaimed.

// connectedUnclaimed registers through the real connect and returns the
// agent id and the claim record publication wrote for it.
func connectedUnclaimed(t *testing.T) (platform *stubPlatform, cfgPath, stateDir, agentID, claimURL, claimCode string) {
	t.Helper()
	withShortConnectTimings(t)
	platform = newStubPlatform(t)
	cfgPath, stateDir = connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("setup connect exited %d: %s", code, errOut)
	}
	reg, ok := loadAgent(t, stateDir)
	claim := storedClaim(t, cfgPath, reg.AgentID)
	if !ok || claim.ClaimURL == "" || claim.ClaimCode == "" {
		t.Fatalf("setup left no claim record: reg=%+v claim=%+v", reg, claim)
	}
	return platform, cfgPath, stateDir, reg.AgentID, claim.ClaimURL, claim.ClaimCode
}

// The review's must-fix: a planted agent.json that keeps this installation's
// agent id but carries another agent's link on the platform's own origin.
// The platform knows the agent, the link passes the origin check, and
// connect, status, connect -json and mining enable all printed it; claiming
// it would have put the attacker's agent in the participant's account.
func TestALinkPlantedInTheAgentRecordIsNeverPrinted(t *testing.T) {
	platform, cfgPath, stateDir, agentID, claimURL, claimCode := connectedUnclaimed(t)
	const planted = "ZZ99-EVIL"
	writeAgentRecordRaw(t, stateDir, map[string]any{
		"agent_id": agentID, "status": "unclaimed",
		"claim_url": platform.srv.URL + "/claim/" + planted, "claim_code": planted,
	})

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	var text, textErr, js, jsErr, envelope bytes.Buffer
	_ = statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv)
	_ = statusMain([]string{"-config", cfgPath, "-json"}, &js, &jsErr, noEnv)
	emitMachine(&envelope, connectEnvelope(cfgPath, noEnv, exitOK, ""))
	_, mOut, mErr := runMiningEnable(t, cfgPath)
	for name, got := range map[string]string{
		"connect":       out + errOut,
		"status":        text.String() + textErr.String(),
		"status -json":  js.String() + jsErr.String(),
		"connect -json": envelope.String(),
		"mining enable": mOut + mErr,
	} {
		if strings.Contains(got, planted) {
			t.Errorf("%s printed the planted link:\n%s", name, got)
		}
	}
	if !strings.Contains(out, claimURL) || !strings.Contains(out, "code: "+claimCode) {
		t.Errorf("connect did not print the agent's own link from the claim record:\n%s", out)
	}
	if !strings.Contains(text.String(), claimURL) {
		t.Errorf("status did not print the agent's own link:\n%s", text.String())
	}
}

// The same planted record when the platform says the agent is claimed:
// connect asks /v1/agents/me before it prints, and a claimed agent has no
// link to show.
func TestAClaimedAgentShowsNoLinkWhateverItsRecordSays(t *testing.T) {
	platform, cfgPath, stateDir, agentID, _, _ := connectedUnclaimed(t)
	platform.claim("search")
	writeAgentRecordRaw(t, stateDir, map[string]any{
		"agent_id": agentID, "status": "unclaimed",
		"claim_url": platform.srv.URL + "/claim/ZZ99-EVIL", "claim_code": "ZZ99-EVIL",
	})
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	if strings.Contains(out, "claim this agent") || strings.Contains(out, "/claim/") {
		t.Fatalf("connect printed a claim link for a claimed agent:\n%s", out)
	}
	if reg, _ := loadAgent(t, stateDir); reg.Status != "claimed" {
		t.Fatalf("the platform's answer was not applied: %+v", reg)
	}
}

// When /v1/agents/me cannot answer, the check that shows a stored link is
// this agent's cannot be made: connect fails closed, shows no link and
// mints none, where it used to print the stored record's link and only then
// fail at the poll.
func TestNoLinkIsShownWhenThePlatformCannotSayWhoseKeyItIs(t *testing.T) {
	platform, cfgPath, _, _, claimURL, _ := connectedUnclaimed(t)
	platform.setMeError(true)
	mints := platform.claimCodeCallCount()
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitTransport {
		t.Fatalf("connect exited %d, want %d:\n%s%s", code, exitTransport, out, errOut)
	}
	if strings.Contains(out+errOut, claimURL) || strings.Contains(out, "claim this agent") {
		t.Fatalf("connect showed a claim link it could not check:\n%s%s", out, errOut)
	}
	if platform.claimCodeCallCount() != mints {
		t.Fatal("connect minted a claim code it could not check was for this agent")
	}
	if !strings.Contains(errOut, "no claim link is shown") {
		t.Fatalf("connect did not say why it stopped:\n%s", errOut)
	}
}

// "me" is the router's self-lookup route: a record naming it answered every
// status call for whatever agent the key belongs to and was never rebuilt.
// It is corrupt on load now, and a foreground connect rebuilds it.
func TestARecordNamingTheSelfLookupIsRebuilt(t *testing.T) {
	platform, cfgPath, stateDir, agentID, claimURL, _ := connectedUnclaimed(t)
	writeAgentRecordRaw(t, stateDir, map[string]any{"agent_id": "me", "status": "unclaimed"})
	before := platform.meCallCount()
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	if platform.meCallCount() == before {
		t.Fatal("the record naming \"me\" was not rebuilt from /v1/agents/me")
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != agentID {
		t.Fatalf("the rebuilt record names %q, want %q", reg.AgentID, agentID)
	}
	if !strings.Contains(out, claimURL) {
		t.Fatalf("connect did not print the agent's own link after the rebuild:\n%s", out)
	}
}

// An older version kept the link in agent.json. It is not read: status says
// no link is on file, and a foreground connect mints a fresh one into the
// claim record.
func TestALinkAnOlderVersionKeptInTheAgentRecordIsReplacedByAFreshOne(t *testing.T) {
	platform, cfgPath, stateDir, agentID, claimURL, claimCode := connectedUnclaimed(t)
	if err := os.Remove(filepath.Join(filepath.Dir(stateDir), claimRecordFile)); err != nil {
		t.Fatal(err)
	}
	writeAgentRecordRaw(t, stateDir, map[string]any{
		"agent_id": agentID, "status": "unclaimed", "claim_url": claimURL, "claim_code": claimCode,
	})
	var text, textErr bytes.Buffer
	_ = statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv)
	if strings.Contains(text.String(), claimURL) || !strings.Contains(text.String(), "no claim link is on file") {
		t.Fatalf("status read the link from agent.json, or did not say none is on file:\n%s", text.String())
	}
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	if platform.claimCodeCallCount() != 1 || !strings.Contains(out, "/claim/MINT-01") {
		t.Fatalf("connect did not mint and print a fresh link (%d mints):\n%s", platform.claimCodeCallCount(), out)
	}
	if storedClaim(t, cfgPath, agentID).ClaimURL != platform.srv.URL+"/claim/MINT-01" {
		t.Fatal("the fresh link was not recorded beside credentials.json")
	}
}

// -force with a record naming another agent than the stored key's does not
// rebuild, so running the same command again can never succeed: machine
// mode says fix_input, with a code of its own, never retry.
func TestForceOnARecordNamingAnotherAgentAsksForDifferentInput(t *testing.T) {
	_, cfgPath, _, _, _ := unknownAgentFixture(t, map[string]any{"status": "claimed"})
	code, out, _ := runConnect(t, cfgPath, nil, "-json", "-force")
	if code == exitOK {
		t.Fatalf("connect -json -force exited 0:\n%s", out)
	}
	var env struct {
		Code      string `json:"code"`
		Action    string `json:"action"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not one envelope: %v\n%s", err, out)
	}
	if env.Code != "force_does_not_rebuild" || env.Action != actionFixInput || env.Retryable {
		t.Fatalf("envelope = %+v, want force_does_not_rebuild / fix_input / not retryable\n%s", env, out)
	}
}
