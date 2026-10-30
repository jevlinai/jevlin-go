package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// What an earlier version left in a directory setup adopts from, or in an
// installation a purge empties, is named by path and never called
// deletable: this client keeps no list of those files, and one of them can
// hold a key (an earlier version kept its payout wallet's there).
func TestNothingAnEarlierVersionLeftIsCalledDeletable(t *testing.T) {
	invitations := []string{"delete it when you like", "Delete them yourself", "you may delete"}
	says := func(t *testing.T, what, out string) {
		t.Helper()
		for _, inv := range invitations {
			if strings.Contains(out, inv) {
				t.Errorf("%s invites deleting what an earlier version left (%q):\n%s", what, inv, out)
			}
		}
		if !strings.Contains(out, "look before deleting") {
			t.Errorf("%s does not say to look first:\n%s", what, out)
		}
	}

	t.Run("adoption", func(t *testing.T) {
		s := newSetupSandbox(t)
		old := s.home + ".061"
		writeFileT(t, filepath.Join(old, "state", "agent.json"), `{"agent_id":"agent-old","status":"claimed","scopes":["search"]}`+"\n")
		if err := writeCredentials(filepath.Join(old, credentialsFile), credentials{APIKey: "sr-old", Router: s.env["JEVLIN_ROUTER_URL"], AgentsAPI: s.platform.srv.URL}); err != nil {
			t.Fatal(err)
		}
		writeFileT(t, filepath.Join(old, "earlier-dir", "earlier.key"), "fictional\n")
		s.platform.mu.Lock()
		s.platform.agentByKey["sr-old"] = "agent-old"
		s.platform.statusByAgent["agent-old"] = "claimed"
		s.platform.scopesByAgent["agent-old"] = []string{"search"}
		s.platform.mu.Unlock()
		_, out, _ := s.run(strings.NewReader("y\n"), true, "-no-profile", "-no-agents")
		if !strings.Contains(out, "left the rest of "+old) {
			t.Fatalf("the run did not adopt from %s and leave the rest:\n%s", old, out)
		}
		says(t, "setup's adoption", out)
	})

	t.Run("purge", func(t *testing.T) {
		s := installed(t)
		leftovers(t, s)
		_, out, _ := s.uninstall(t, tty(s.home), true, "-purge-state")
		says(t, "the purge", leftSection(t, out))
	})
}
