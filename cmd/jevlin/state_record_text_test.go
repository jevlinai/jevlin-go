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

// writeClaimRecordRaw writes claim.json in home without the record's own
// Save, which refuses what its Load refuses.
func writeClaimRecordRaw(t *testing.T, home string, rec map[string]any) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, claimRecordFile), raw, 0o600); err != nil {
		t.Fatal(err)
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

// The triage's other residual: a scope went to status's text output raw.
// status now says the record cannot be read, in every mode, and repeats
// none of it.
func TestStatusNeverPrintsAPlantedAgentRecord(t *testing.T) {
	for _, field := range []string{"scopes", "status", "agent_id"} {
		t.Run(field, func(t *testing.T) {
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			rec := map[string]any{
				"agent_id": "agent-1", "status": "claimed", "scopes": []string{"search"},
			}
			if field == "scopes" {
				rec[field] = []string{"search", plantedEscapes}
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
