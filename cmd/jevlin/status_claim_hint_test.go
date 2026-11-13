package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// status reads only the record on this machine, so an agent claimed on the
// website still reads as unclaimed until the next connect or search records
// it. Every unclaimed line, with or without a claim link to show, is followed
// by the one line that says so; no other state gets it.
func TestStatusSaysConnectRecordsAClaimMadeOnTheWebsite(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, root string, store *auth.Store)
		want  bool
	}{
		"unclaimed, no claim link on file": {func(t *testing.T, _ string, s *auth.Store) {
			if err := s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-1", Status: "unclaimed"}); err != nil {
				t.Fatal(err)
			}
		}, true},
		"unclaimed, with its claim link": {func(t *testing.T, root string, _ *auth.Store) {
			writeAgentRecordRaw(t, filepath.Join(root, "state"), map[string]any{"agent_id": "agent-fictional-2", "status": "unclaimed"})
			writeClaimRecordRaw(t, root, map[string]any{"agent_id": "agent-fictional-2", "claim_url": "https://platform.jevlin.ai/claim/abc123", "claim_code": "ABC-123"})
		}, true},
		"unclaimed, a link that fails validation": {func(t *testing.T, root string, _ *auth.Store) {
			writeAgentRecordRaw(t, filepath.Join(root, "state"), map[string]any{"agent_id": "agent-fictional-3", "status": "unclaimed"})
			writeClaimRecordRaw(t, root, map[string]any{"agent_id": "agent-fictional-3", "claim_url": "https://evil.example/claim/abc123", "claim_code": "ABC-123"})
		}, true},
		"claimed": {func(t *testing.T, _ string, s *auth.Store) {
			if err := s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-4", Status: "claimed", Scopes: []string{"search"}}); err != nil {
				t.Fatal(err)
			}
		}, false},
		"expired": {func(t *testing.T, _ string, s *auth.Store) {
			if err := s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-5", Status: "expired"}); err != nil {
				t.Fatal(err)
			}
		}, false},
		"not registered": {func(*testing.T, string, *auth.Store) {}, false},
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath, root, store := statusFixture(t)
			tc.setup(t, root, store)
			var out bytes.Buffer
			if code := statusMain([]string{"-config", cfgPath}, &out, io.Discard, noEnv); code != exitOK {
				t.Fatalf("status exited %d", code)
			}
			text := out.String()
			if got := strings.Contains(text, "\n"+unclaimedHint+"\n"); got != tc.want {
				t.Errorf("hint shown = %v, want %v:\n%s", got, tc.want, text)
			}
			if tc.want && !strings.Contains(text, "agent:  unclaimed") {
				t.Errorf("the fixture is not unclaimed:\n%s", text)
			}
			if tc.want && strings.Index(text, unclaimedHint) < strings.Index(text, "agent:  unclaimed") {
				t.Errorf("the hint comes before the line it follows:\n%s", text)
			}
		})
	}
	if unclaimedHint != "        Claimed it on the website? `jevlin connect` updates this." {
		t.Errorf("the hint reads %q", unclaimedHint)
	}
}
