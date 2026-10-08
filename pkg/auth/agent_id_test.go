package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidAgentIDRefusesARouteAPathOrAControl(t *testing.T) {
	for _, id := range []string{"", "me", ".", "..", "a/b", `a\b`, "agent-1\u200f", "agent-1\x1b[2J"} {
		if ValidAgentID(id) == nil {
			t.Errorf("%q accepted", id)
		}
	}
	for _, id := range []string{"agent-1", "01a11680-4c2e-4b6f-9d3a-2f7c5e1b0a9d", "Me", "mE", "me2"} {
		if err := ValidAgentID(id); err != nil {
			t.Errorf("%q refused: %v", id, err)
		}
	}
}

// "me" is the router's self-lookup: GET /v1/agents/me answers for whatever
// agent the key belongs to, so a record naming it passed every check that
// asks the platform about the record's agent. Such a record is corrupt on
// load, which is what sends a foreground connect to rebuild it from the
// platform, and it is never written.
func TestAnAgentRecordNamingARouteIsCorrupt(t *testing.T) {
	for _, id := range []string{"me", "..", "a/b"} {
		s, dir := newStore(t)
		raw := `{"agent_id":"` + id + `","status":"unclaimed"}`
		if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.LoadAgentRegistration(); !errors.Is(err, ErrAgentRegistrationCorrupt) {
			t.Errorf("agent_id %q: load = %v, want ErrAgentRegistrationCorrupt", id, err)
		}
		if err := s.SaveAgentRegistration(AgentRegistration{AgentID: id, Status: "unclaimed"}); err == nil {
			t.Errorf("agent_id %q: saved", id)
		}
	}
}
