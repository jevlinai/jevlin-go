//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// status advises `jevlin connect` to rebuild only a record connect
// rebuilds: a corrupt one. An agent.json others can read is refused by
// connect too, so "run `jevlin connect` to rebuild it" sent the
// participant to a command that fails the same way.
func TestStatusAdvisesARebuildOnlyForARecordConnectRebuilds(t *testing.T) {
	_, cfgPath, stateDir, _, _, _ := connectedUnclaimed(t)
	path := filepath.Join(stateDir, "agent.json")
	if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- the loose mode is the case under test
		t.Fatal(err)
	}
	out := statusText(t, cfgPath)
	if !strings.Contains(out, "could not be read") {
		t.Fatalf("status did not report the unreadable record:\n%s", out)
	}
	if strings.Contains(out, "to rebuild it") {
		t.Fatalf("status advised a rebuild connect will not do:\n%s", out)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeAgentRecordRaw(t, stateDir, map[string]any{"agent_id": "me", "status": "unclaimed"})
	if out := statusText(t, cfgPath); !strings.Contains(out, "to rebuild it") {
		t.Fatalf("status gave no next step for a corrupt record:\n%s", out)
	}
}
