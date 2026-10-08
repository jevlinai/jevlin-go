package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The platform resolves a key to its own agent and answers not found for
// any other id. These hold a foreground connect to rebuilding a record that
// names an agent the stored key does not belong to from /v1/agents/me
// (hard invariant 13), and the resume and -force to never doing so.

// unknownAgentFixture registers two agents on a stub that, like the
// router, answers a status call only for the key's own agent; stores the
// second agent's key; and writes record as agent.json.
func unknownAgentFixture(t *testing.T, record map[string]any) (platform *stubPlatform, cfgPath, stateDir, ownID, otherID string) {
	t.Helper()
	withShortConnectTimings(t)
	platform = newStubPlatform(t)
	platform.setStatusRequiresOwner(true)
	cfgPath, stateDir = connectConfig(t, platform.srv.URL, "")
	otherID, _ = registerAgent(t, platform)
	ownID, ownKey := registerAgent(t, platform)
	setupLostRegistration(t, mustLoadConfig(t, cfgPath), ownKey, false)
	if record["agent_id"] == nil {
		record["agent_id"] = otherID
	}
	writeAgentRecordRaw(t, stateDir, record)
	return platform, cfgPath, stateDir, ownID, otherID
}

// #60's stuck participant: an older build replacing an expired
// registration wrote the new agent's key, crashed before the new record,
// and connect then refused with "expired registration is no longer known
// to the platform" until agent.json was moved aside by hand.
func TestConnectRebuildsAReplacementTheOldRecordHides(t *testing.T) {
	platform, cfgPath, stateDir, ownID, otherID := unknownAgentFixture(t, map[string]any{"status": "expired"})
	platform.mu.Lock()
	platform.statusByAgent[otherID] = "expired"
	platform.mu.Unlock()
	registerBefore, _, _ := platform.counts()

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	if strings.Contains(errOut, "no longer known to the platform; refusing") {
		t.Fatalf("connect still refused:\n%s", errOut)
	}
	if !strings.Contains(errOut, "rebuilt the registration from the platform (agent "+ownID+")") {
		t.Fatalf("connect did not say it rebuilt the record:\n%s", errOut)
	}
	if registerAfter, _, _ := platform.counts(); registerAfter != registerBefore {
		t.Fatalf("a rebuild registered: %d Register calls, want %d", registerAfter, registerBefore)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != ownID || reg.Status != "unclaimed" {
		t.Fatalf("agent.json = %+v ok=%v, want the stored key's own agent", reg, ok)
	}
	if !strings.Contains(out, "claim this agent:") {
		t.Fatalf("the rebuilt agent's claim link was not printed:\n%s", out)
	}
}

// A sandboxed command rewrites agent.json to name an agent of its own,
// unclaimed, with a claim link on the platform's own origin: the origin
// check passes it. A foreground connect must ask about the agent before it
// prints that link, and print the stored key's own agent's link instead.
func TestConnectNeverPrintsTheClaimLinkOfAnAgentTheKeyIsNot(t *testing.T) {
	platform, cfgPath, stateDir, ownID, otherID := unknownAgentFixture(t, map[string]any{"status": "unclaimed"})
	platform.mu.Lock()
	otherCode := platform.claimCodeByAgent[otherID]
	ownCode := platform.claimCodeByAgent[ownID]
	platform.mu.Unlock()
	rec := map[string]any{
		"agent_id": otherID, "status": "unclaimed",
		"claim_url": platform.srv.URL + "/claim/" + otherCode, "claim_code": otherCode,
	}
	writeAgentRecordRaw(t, stateDir, rec)

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	if strings.Contains(out, otherCode) {
		t.Fatalf("connect printed the planted agent's claim link:\n%s", out)
	}
	if !strings.Contains(out, "code: "+ownCode) {
		t.Fatalf("connect did not print the stored key's own claim code %s:\n%s", ownCode, out)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != ownID {
		t.Fatalf("agent.json = %+v ok=%v, want %s", reg, ok, ownID)
	}
}

// A claimed record from disk is asked about too: without that, the poll
// says the agent is no longer known and the run ends.
func TestConnectRebuildsAClaimedRecordTheKeyDoesNotKnow(t *testing.T) {
	platform, cfgPath, stateDir, ownID, _ := unknownAgentFixture(t, map[string]any{"status": "claimed", "scopes": []string{"search"}})
	platform.claim("search")

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != ownID || reg.Status != "claimed" {
		t.Fatalf("agent.json = %+v ok=%v, want %s claimed", reg, ok, ownID)
	}
}

// The resume never rebuilds (invariant 13) and never asks /v1/agents/me.
// -force does not rebuild either: it may ask, so as to say what it found,
// but agent.json is untouched, and it never tells the participant to run
// the command they just ran.
func TestNeitherTheResumeNorForceRebuildsAnUnknownAgent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		args   []string
	}{
		{"resume unclaimed", "unclaimed", []string{"-resume"}},
		{"resume expired", "expired", []string{"-resume"}},
		{"resume claimed", "claimed", []string{"-resume"}},
		{"force expired", "expired", []string{"-force"}},
		{"force unclaimed", "unclaimed", []string{"-force"}},
		{"force claimed", "claimed", []string{"-force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform, cfgPath, stateDir, _, otherID := unknownAgentFixture(t, map[string]any{"status": tc.status})
			before, err := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- the test's own state dir
			if err != nil {
				t.Fatal(err)
			}
			_, out, errOut := runConnect(t, cfgPath, nil, tc.args...)
			if n := platform.meCallCount(); n != 0 && tc.args[0] == "-resume" {
				t.Fatalf("%s asked /v1/agents/me %d times\nstdout=%s\nstderr=%s", tc.name, n, out, errOut)
			}
			if tc.args[0] == "-force" && strings.Contains(out+errOut, "connect -force") {
				t.Fatalf("%s told the participant to run connect -force:\n%s%s", tc.name, out, errOut)
			}
			after, err := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- the test's own state dir
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("%s rewrote agent.json (it named %s):\nbefore=%s\nafter=%s", tc.name, otherID, before, after)
			}
		})
	}
}

// /v1/agents/me that knows no agent for the key leaves the run exactly as
// it was before: the expired path still refuses.
func TestAnUnknownAgentWithADeadKeyStillRefuses(t *testing.T) {
	platform, cfgPath, stateDir, _, otherID := unknownAgentFixture(t, map[string]any{"status": "expired"})
	cfg := mustLoadConfig(t, cfgPath)
	if err := writeCredentials(credentialsPath(cfg.Miner), credentials{APIKey: "sr-a-key-nobody-minted"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}
	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK || !strings.Contains(errOut, "no longer known to the platform; refusing automatic replacement") {
		t.Fatalf("connect did not refuse: code=%d\n%s", code, errOut)
	}
	if platform.meCallCount() != 1 {
		t.Fatalf("/v1/agents/me calls = %d, want 1", platform.meCallCount())
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != otherID {
		t.Fatalf("agent.json changed: %+v ok=%v", reg, ok)
	}
}
