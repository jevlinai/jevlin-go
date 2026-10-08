//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// Once credentials.json holds the journal's key, the journal decides which
// agent this installation is, and nothing a sandboxed command leaves at
// state/agent.json may keep it from publishing. A record naming another agent
// was the shape the first review planted; these are the others it can make,
// each of which refuses to load. Every one used to leave the journal
// unfinished and every later run failing the same way. Now the name is set
// aside, never opened or followed, and the journal's record is published.
func TestAJournalPublishesOverWhateverIsAtTheAgentRecordsName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path, outside string)
	}{
		{"a record others can read", func(t *testing.T, path, _ string) {
			if err := os.WriteFile(path, []byte(`{"agent_id":"agent-other","status":"claimed"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- the loose mode is the planted shape
				t.Fatal(err)
			}
		}},
		{"a link to a file outside", func(t *testing.T, path, outside string) {
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a directory", func(t *testing.T, path, _ string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"a FIFO", func(t *testing.T, path, _ string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			cfg := mustLoadConfig(t, cfgPath)
			if _, err := auth.OpenStore(stateDir); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "participant-file")
			if err := os.WriteFile(outside, []byte("the participant's own file"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, filepath.Join(stateDir, "agent.json"), outside)
			if err := testJournal(t, cfgPath).Save(auth.PendingRegistration{
				AgentID:        "agent-journal",
				Key:            "sr-journal-key",
				ClaimURL:       platform.srv.URL + "/claim/JOURNAL-1",
				ClaimCode:      "JOURNAL-1",
				ClaimExpiresAt: "2026-09-16T00:00:00Z",
				Status:         "unclaimed",
			}); err != nil {
				t.Fatal(err)
			}

			code, _, errOut := runConnect(t, cfgPath, nil, "-resume")
			if code != exitOK {
				t.Fatalf("journal recovery exited %d: %s", code, errOut)
			}
			if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
				t.Fatalf("journal recovery issued a new Register: %d", registerCalls)
			}
			if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != "agent-journal" {
				t.Fatalf("the journal's registration was not published: %+v ok=%v", reg, ok)
			}
			if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
				t.Fatalf("the journal was not finished: ok=%v err=%v", ok, err)
			}
			if got, err := readCredentials(credentialsPath(cfg.Miner)); err != nil || got.APIKey != "sr-journal-key" {
				t.Fatalf("credentials.json = %+v %v", got, err)
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "the participant's own file" { // #nosec G304 -- the test's own file
				t.Fatalf("the file a planted link named changed: %q %v", got, err)
			}
			matches, _ := filepath.Glob(filepath.Join(stateDir, "agent.json.unreadable*"))
			if len(matches) != 1 {
				t.Fatalf("what was at agent.json was not kept aside once: %v", matches)
			}
		})
	}
}
