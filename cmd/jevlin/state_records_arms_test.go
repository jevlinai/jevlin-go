package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// The arms of this branch's code that a reviewer deleted without a test
// going red, each held here to what its comment promises.

// An empty stored link is no link: nothing to check, nothing said on
// stderr about a link "not on the configured platform.base_url".
func TestAnEmptyClaimLinkIsNotReportedAsOffOrigin(t *testing.T) {
	var errOut bytes.Buffer
	if got := printableClaimURL("", "https://platform.example", &errOut); got != "" || errOut.Len() != 0 {
		t.Fatalf("printableClaimURL(\"\") = %q, stderr %q", got, errOut.String())
	}
}

// The expired-replacement path asks /v1/agents/me when the platform does
// not know the expired record's agent. /me naming that same agent, or
// failing outright, gives nothing to rebuild from: the run refuses exactly
// as it did before, with agent.json untouched.
func TestAnExpiredRecordTheKeyCannotSettleStillRefuses(t *testing.T) {
	for name, meFails := range map[string]bool{"/me fails": true, "/me names no other agent": false} {
		t.Run(name, func(t *testing.T) {
			platform, cfgPath, stateDir, ownID, _ := unknownAgentFixture(t, map[string]any{"status": "expired"})
			if meFails {
				platform.setMeError(true)
			} else {
				// The record names the key's own agent, which the platform
				// then forgets: status answers not found, and /me is asked.
				writeAgentRecordRaw(t, stateDir, map[string]any{"agent_id": ownID, "status": "expired"})
				platform.mu.Lock()
				platform.statusNotFound = true
				platform.mu.Unlock()
			}
			before, err := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- the test's own state dir
			if err != nil {
				t.Fatal(err)
			}
			code, _, errOut := runConnect(t, cfgPath, nil)
			if code == exitOK || !strings.Contains(errOut, "refusing automatic replacement") {
				t.Fatalf("connect did not refuse: %d\n%s", code, errOut)
			}
			if registers(platform) != 2 { // the fixture's two
				t.Fatalf("connect registered: %d", registers(platform))
			}
			after, _ := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- the test's own state dir
			if !bytes.Equal(before, after) {
				t.Fatalf("agent.json changed:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

// The adoption transaction validates every object it moves before moving
// any: a claim.json in the source that is a symlink stops the identity
// bundle, and nothing moves. The claim record is moved last, so a failure
// at it is a failure after state/, the credential and the journal moved,
// and must put all three back.
func TestTheIdentityBundleHoldsTheClaimRecordToItsRules(t *testing.T) {
	t.Run("a link in the source", func(t *testing.T) {
		root := t.TempDir()
		src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
		writeInstallation(t, src, "identity")
		target := filepath.Join(root, "elsewhere.json")
		writeFileT(t, target, `{"agent_id":"old-agent","claim_url":"https://platform.example/claim/X"}`)
		if err := os.Symlink(target, filepath.Join(src, claimRecordFile)); err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
			}
			t.Skipf("cannot make a symlink here: %v", err)
		}
		// The destination exists, as an installation's own directory does:
		// without it every adoption failed on its first move, whatever the
		// source held, and this proved nothing about the link.
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		a := &adoption{src: src, dst: dst, now: time.Now(), out: &bytes.Buffer{}, restrict: restrictToOwner}
		err := a.run()
		if err == nil {
			t.Fatal("a symlinked claim.json was adopted")
		}
		if !strings.Contains(err.Error(), filepath.Join(src, claimRecordFile)+" is a symlink") {
			t.Fatalf("the refusal is not the link's: %v", err)
		}
		if len(a.moved) != 0 || lexists(filepath.Join(dst, credentialsFile)) {
			t.Fatalf("moved %v despite the refusal", a.moved)
		}
	})
	t.Run("a failure after the records moved", func(t *testing.T) {
		root := t.TempDir()
		src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
		writeInstallation(t, src, "identity")
		writeFileT(t, filepath.Join(src, registrationJournalFile), `{"agent_id":"old-agent"}`)
		writeFileT(t, filepath.Join(src, claimRecordFile), `{"agent_id":"old-agent","claim_url":"https://platform.example/claim/X"}`)
		// The claim record moves after state/, the credential and the
		// journal; failing its move must unwind the three that moved.
		claimErr := errors.New("the claim record would not move")
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		a := &adoption{src: src, dst: dst, now: time.Now(), out: &bytes.Buffer{}, restrict: restrictToOwner,
			moveFn: func(from, to string) error {
				if filepath.Base(from) == claimRecordFile {
					return claimErr
				}
				return os.Rename(from, to)
			}}
		if err := a.run(); err == nil || !strings.Contains(err.Error(), claimErr.Error()) {
			t.Fatalf("the adoption did not fail at the claim record, so nothing was moved to be put back: %v", err)
		}
		if len(a.moved) < 3 {
			t.Fatalf("only %v moved before the claim record failed; the rollback had nothing to undo", a.moved)
		}
		for _, rel := range []string{filepath.Join("state", "agent.json"), credentialsFile, registrationJournalFile, claimRecordFile} {
			if !lexists(filepath.Join(src, rel)) || lexists(filepath.Join(dst, rel)) {
				t.Errorf("%s was not put back after the failure", rel)
			}
		}
	})
}

// pkg/auth's own entry points refuse an empty directory, whatever the
// caller: an empty one would put the record in the working directory.
func TestTheRecordsBesideTheCredentialNeedADirectory(t *testing.T) {
	if _, err := auth.OpenClaimRecord(""); err == nil {

		t.Error("OpenClaimRecord(\"\") succeeded")
	}
	if _, err := auth.OpenRegistrationJournal(""); err == nil {
		t.Error("OpenRegistrationJournal(\"\") succeeded")
	}
}
