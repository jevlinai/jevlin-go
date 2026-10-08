package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// A sandboxed command can rewrite any record in the state directory. These
// plant what one would to act on the participant's terminal (an OSC 52
// clipboard write, a one-byte C1 CSI and a right-to-left override) and hold
// jevlin's own output to never carrying a byte of it.
const plantedEscapes = "x\x1b]52;c;ZXZpbA==\a\u009b2J\u202ey"

func requireNoPlantedEscape(t *testing.T, what, out string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\a", "\u009b", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%s carried a planted control character to the terminal:\n%q", what, out)
		}
	}
}

func writeAgentRecordRaw(t *testing.T, stateDir string, rec map[string]any) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The triage's residual for the claim-link check: an on-origin claim_url
// passes it, and the claim_code beside it went to connect's stdout raw. A
// foreground connect now treats the record as corrupt and rebuilds it from
// the platform, which knows the real code.
func TestAPlantedClaimCodeIsRebuiltFromThePlatformNotPrinted(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	agentID, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, false)
	writeAgentRecordRaw(t, stateDir, map[string]any{
		"agent_id": agentID, "status": "unclaimed",
		"claim_url":  platform.srv.URL + "/claim/AB12-CD34",
		"claim_code": plantedEscapes,
	})

	code, out, errOut := runConnect(t, cfgPath, nil)
	requireNoPlantedEscape(t, "connect", out+errOut)
	if code != exitOK {
		t.Fatalf("connect exited %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	if platform.meCallCount() != 1 {
		t.Fatalf("the record was not rebuilt from /v1/agents/me: %d calls", platform.meCallCount())
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != agentID || reg.ClaimCode != "AB12-CD34" {
		t.Fatalf("rebuilt record = %+v ok=%v", reg, ok)
	}
	if !strings.Contains(out, "code: AB12-CD34") {
		t.Fatalf("connect did not print the platform's own code:\n%s", out)
	}
	if !lexists(filepath.Join(stateDir, "agent.json.corrupt")) {
		t.Fatal("the planted record was not set aside as evidence")
	}
}

// The triage's other residual: a scope (and the slot and refusal beside it)
// went to status's text output raw. status now says the record cannot be
// read, in every mode, and repeats none of it.
func TestStatusNeverPrintsAPlantedAgentRecord(t *testing.T) {
	for _, field := range []string{"scopes", "last_enrollment_slot", "slot_refusal", "status", "agent_id"} {
		t.Run(field, func(t *testing.T) {
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			rec := map[string]any{
				"agent_id": "agent-1", "status": "claimed", "scopes": []string{"mining"},
				"last_enrollment_slot": "twilight-slot-3",
			}
			if field == "scopes" {
				rec[field] = []string{"mining", plantedEscapes}
			} else {
				rec[field] = plantedEscapes
			}
			writeAgentRecordRaw(t, stateDir, rec)

			var text, textErr, js, jsErr bytes.Buffer
			_ = statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv)
			_ = statusMain([]string{"-config", cfgPath, "-json"}, &js, &jsErr, noEnv)
			requireNoPlantedEscape(t, "status", text.String()+textErr.String())
			requireNoPlantedEscape(t, "status -json", js.String()+jsErr.String())
			if !strings.Contains(textErr.String(), "registration on file could not be read") {
				t.Fatalf("status did not say the record cannot be read:\nstdout=%s\nstderr=%s", text.String(), textErr.String())
			}

			code, out, errOut := runMiningEnable(t, cfgPath)
			requireNoPlantedEscape(t, "mining enable", out+errOut)
			if code == exitOK {
				t.Fatalf("mining enable acted on a record it cannot trust:\n%s%s", out, errOut)
			}

			var envelope bytes.Buffer
			emitMachine(&envelope, connectEnvelope(cfgPath, noEnv, exitOK, ""))
			requireNoPlantedEscape(t, "connect -json", envelope.String())

			if _, ok, err := mustStore(t, stateDir).LoadAgentRegistration(); ok || err == nil {
				t.Fatal("the planted record loads")
			}
		})
	}
}

func mustStore(t *testing.T, stateDir string) *auth.Store {
	t.Helper()
	store, err := auth.OpenStoreExisting(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
