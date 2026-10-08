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

// A payout.json in the state directory that does not load (not bech32) is
// as unused as one that names another address: nothing recorded, nothing
// declared, nothing echoed.
func TestALegacyPayoutRecordThatDoesNotLoadIsNotUsed(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	writeFileT(t, filepath.Join(stateDir, "payout.json"), `{"address":"send-funds-to-evil.example"}`)
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if n := as.declarationAttempts(); n != 0 {
		t.Fatalf("declared %q", as.declaredAddress())
	}
	if _, ok, _ := testPayoutRecord(t, stateDir).Load(); ok {
		t.Fatal("an address that does not load was recorded")
	}
	if strings.Contains(out+errOut, "evil.example") {
		t.Fatalf("the planted text was repeated:\n%s%s", out, errOut)
	}
}

// The adoption transaction validates every object it moves before moving
// any: a payout.json in the source that is a symlink stops the identity
// bundle, and nothing moves. Its failure after a move restores the payout
// and claim records with the rest, and describeInstallation names them.
func TestTheIdentityBundleHoldsThePayoutAndClaimRecordsToItsRules(t *testing.T) {
	t.Run("a link in the source", func(t *testing.T) {
		root := t.TempDir()
		src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
		writeInstallation(t, src, "identity")
		target := filepath.Join(root, "elsewhere.json")
		writeFileT(t, target, `{"address":"`+participantAddress+`"}`)
		if err := os.Symlink(target, filepath.Join(src, payoutRecordFile)); err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("cannot make a symlink on this runner, and CI does not let this test skip: %v", err)
			}
			t.Skipf("cannot make a symlink here: %v", err)
		}
		a := &adoption{src: src, dst: dst, now: time.Now(), out: &bytes.Buffer{}, restrict: restrictToOwner}
		if err := a.run(); err == nil {
			t.Fatal("a symlinked payout.json was adopted")
		}
		if len(a.moved) != 0 || lexists(filepath.Join(dst, credentialsFile)) {
			t.Fatalf("moved %v despite the refusal", a.moved)
		}
	})
	t.Run("a failure after the records moved", func(t *testing.T) {
		root := t.TempDir()
		src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
		writeInstallation(t, src, "identity")
		writeFileT(t, filepath.Join(src, payoutRecordFile), `{"address":"`+participantAddress+`"}`)
		writeFileT(t, filepath.Join(src, claimRecordFile), `{"agent_id":"old-agent","claim_url":"https://platform.example/claim/X"}`)
		if got := describeInstallation(src); !strings.Contains(got, "a payout address") {
			t.Errorf("describeInstallation does not name the payout record: %q", got)
		}
		// The wallet stage runs after the identity; failing it must unwind
		// the identity, the two records beside the credential included.
		walletErr := errors.New("the wallet would not move")
		writeWalletFixture(t, filepath.Join(src, "wallet"))
		a := &adoption{src: src, dst: dst, now: time.Now(), out: &bytes.Buffer{}, restrict: restrictToOwner,
			moveFn: func(from, to string) error {
				if filepath.Base(from) == "wallet" {
					return walletErr
				}
				return os.Rename(from, to)
			}}
		if err := a.run(); err == nil {
			t.Fatal("the adoption succeeded despite the wallet failing")
		}
		for _, rel := range []string{payoutRecordFile, claimRecordFile, credentialsFile} {
			if !lexists(filepath.Join(src, rel)) || lexists(filepath.Join(dst, rel)) {
				t.Errorf("%s was not put back after the failure", rel)
			}
		}
	})
}

// pkg/auth's own entry points refuse an empty directory, whatever the
// caller: an empty one would put the record in the working directory.
func TestTheRecordsBesideTheCredentialNeedADirectory(t *testing.T) {
	if _, err := auth.OpenPayoutRecord(""); err == nil {
		t.Error("OpenPayoutRecord(\"\") succeeded")
	}
	if _, err := auth.OpenClaimRecord(""); err == nil {
		t.Error("OpenClaimRecord(\"\") succeeded")
	}
	if _, err := auth.OpenRegistrationJournal(""); err == nil {
		t.Error("OpenRegistrationJournal(\"\") succeeded")
	}
}
