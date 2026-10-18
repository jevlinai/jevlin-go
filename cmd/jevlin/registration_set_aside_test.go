package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent.json.corrupt already on disk used to make PreserveCorrupt
// refuse, so every later foreground connect failed the same way with no
// advice; a sandboxed command could plant one to keep a corrupt record from
// ever being rebuilt. Now an earlier copy is replaced, and a name held by
// anything but a regular file gets a fresh name, so neither blocks.
func TestEarlierEvidenceNeverBlocksARebuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"an earlier copy", func(t *testing.T, path string) { writeFileT(t, path, "an earlier record") }},
		{"a directory at the name", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfgPath, stateDir, agentID, _, _ := connectedUnclaimed(t)
			writeAgentRecordRaw(t, stateDir, map[string]any{"agent_id": agentID, "status": "unclaimed", "scopes": []string{"x\x1b[2J"}})
			evidence := filepath.Join(stateDir, "agent.json.corrupt")
			tc.plant(t, evidence)

			code, _, errOut := runConnect(t, cfgPath, nil)
			if code != exitOK {
				t.Fatalf("connect exited %d with earlier evidence present:\n%s", code, errOut)
			}
			if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != agentID || len(reg.Scopes) != 0 {
				t.Fatalf("the record was not rebuilt: %+v ok=%v", reg, ok)
			}
			kept := false
			entries, err := os.ReadDir(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "agent.json.corrupt") && !e.IsDir() {
					got, err := os.ReadFile(filepath.Join(stateDir, e.Name())) // #nosec G304 -- the test's own state dir
					kept = kept || (err == nil && strings.Contains(string(got), agentID))
				}
			}
			if !kept {
				t.Fatalf("the corrupt record was not kept as evidence: %v", entries)
			}
		})
	}
}
