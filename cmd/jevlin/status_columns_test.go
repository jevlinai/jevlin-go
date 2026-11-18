package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// Every value status prints starts in one column, whatever its label: the
// labels were once padded by hand, and "turn end:", one character longer
// than the width they were padded to, pushed its value one column right
// while the agent line sat one column left. The test reads the real output
// of every agent state, so a label added later is held to the same column.
func TestEveryStatusValueStartsInOneColumn(t *testing.T) {
	column := statusLabelWidth + 1
	for name, setup := range map[string]func(t *testing.T, root string, store *auth.Store){
		"not registered": func(*testing.T, string, *auth.Store) {},
		"unclaimed, with its claim link": func(t *testing.T, root string, _ *auth.Store) {
			writeAgentRecordRaw(t, filepath.Join(root, "state"), map[string]any{"agent_id": "agent-fictional-1", "status": "unclaimed"})
			writeClaimRecordRaw(t, root, map[string]any{"agent_id": "agent-fictional-1", "claim_url": "https://platform.jevlin.ai/claim/abc123", "claim_code": "ABC-123"})
		},
		"claimed": func(t *testing.T, _ string, s *auth.Store) {
			if err := s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-2", Status: "claimed", Scopes: []string{"search"}}); err != nil {
				t.Fatal(err)
			}
		},
		"a corrupt record": func(t *testing.T, root string, _ *auth.Store) {
			writeAgentRecordRaw(t, filepath.Join(root, "state"), map[string]any{"agent_id": 7})
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath, root, store := statusFixture(t)
			setup(t, root, store)
			var out, errOut bytes.Buffer
			statusMain([]string{"-config", cfgPath}, &out, &errOut, noEnv)
			lines := strings.Split(strings.TrimRight(out.String()+errOut.String(), "\n"), "\n")
			if len(lines) < 5 {
				t.Fatalf("status printed too little to check:\n%s", out.String()+errOut.String())
			}
			for _, line := range lines {
				if len(line) <= column || line[column-1] != ' ' || line[column] == ' ' {
					t.Errorf("value not in column %d: %q", column, line)
					continue
				}
				if label := strings.TrimRight(line[:column], " "); label != "" && !strings.HasSuffix(label, ":") {
					t.Errorf("label %q does not end its padding: %q", label, line)
				}
			}
		})
	}
}
