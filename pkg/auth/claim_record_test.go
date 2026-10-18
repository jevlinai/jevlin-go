package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheClaimRecordHoldsOneAgentsLink(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenClaimRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.Load(); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	want := ClaimBootstrap{AgentID: "agent-1", ClaimURL: "https://platform.example/claim/AB12", ClaimCode: "AB12-CD34", ClaimExpiresAt: "2026-09-16T00:00:00Z"}
	if err := r.Save(want); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := r.For("agent-1"); !ok || err != nil || got != want {
		t.Fatalf("For(agent-1) = %+v %v %v", got, ok, err)
	}
	if got, ok, err := r.For("agent-2"); ok || err != nil || got != (ClaimBootstrap{}) {
		t.Fatalf("For(agent-2) handed back another agent's link: %+v %v %v", got, ok, err)
	}
	if info, err := os.Stat(filepath.Join(dir, claimRecordFile)); err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Fatalf("claim.json: %v %v", info, err)
	}
}

// What Load refuses is what Save refuses: a link whose text would act on a
// terminal, an agent id that names a route, and fields this version does not
// write.
func TestTheClaimRecordRefusesOnLoadWhatItRefusesToSave(t *testing.T) {
	for name, rec := range map[string]map[string]any{
		"escape in the code":   {"agent_id": "agent-1", "claim_url": "https://p.example/c", "claim_code": "AB\x1b]52;c;x\a"},
		"tag text in the link": {"agent_id": "agent-1", "claim_url": "https://p.example/c\U000e0049"},
		"agent id me":          {"agent_id": "me", "claim_url": "https://p.example/c"},
		"no link":              {"agent_id": "agent-1"},
		"an unknown field":     {"agent_id": "agent-1", "claim_url": "https://p.example/c", "next": "x"},
	} {
		dir := t.TempDir()
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, claimRecordFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		r, _ := OpenClaimRecord(dir)
		if _, ok, err := r.Load(); ok || err == nil {
			t.Errorf("%s: loaded", name)
		} else if strings.ContainsAny(err.Error(), "\x1b\a") {
			t.Errorf("%s: the refusal repeats the planted bytes: %q", name, err)
		}
		if _, isAgent := rec["agent_id"].(string); isAgent && name != "an unknown field" {
			b := ClaimBootstrap{}
			b.AgentID, _ = rec["agent_id"].(string)
			b.ClaimURL, _ = rec["claim_url"].(string)
			b.ClaimCode, _ = rec["claim_code"].(string)
			if err := r.Save(b); err == nil {
				t.Errorf("%s: saved", name)
			}
		}
	}
}

// Older versions kept claim_url and claim_code in agent.json. They are not
// fields any more: a record carrying them, planted text included, loads
// without them, and nothing reads them.
func TestAnAgentRecordsOldClaimFieldsAreIgnored(t *testing.T) {
	s, dir := newStore(t)
	raw := `{"agent_id":"agent-1","status":"unclaimed","claim_url":"https://evil.example/claim","claim_code":"x\u001b]52;c;x\u0007"}`
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := s.LoadAgentRegistration()
	if !ok || err != nil || rec.AgentID != "agent-1" || rec.Status != "unclaimed" {
		t.Fatalf("got %+v ok=%v err=%v", rec, ok, err)
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "claim_url") || strings.Contains(string(out), "claim_code") {
		t.Fatalf("the record still carries a claim field: %s", out)
	}
}
