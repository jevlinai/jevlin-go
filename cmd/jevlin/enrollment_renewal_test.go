package main

// An enrollment on record whose authorization this configuration's AS did not
// issue — the token an earlier version stored with no issuer, or one another
// AS issued before as_url changed — is renewed by the one command every
// refusal names, `jevlin connect`, and nothing promises a revoke it cannot
// perform.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// plantedForeignAuthorizations are the two records this AS did not issue.
var plantedForeignAuthorizations = map[string]string{ // #nosec G101 -- planted test records, not credentials
	"a token an earlier version stored": "rt-from-an-earlier-version",
	"a token another AS issued":         `{"v":1,"as":"https://as-old.example","token":"rt-old"}`,
}

func TestConnectRenewsAnAuthorizationThisASDidNotIssue(t *testing.T) {
	for name, planted := range plantedForeignAuthorizations {
		t.Run(name, func(t *testing.T) {
			cfgPath, stateDir, _, as := fullyEnrolledInstall(t)
			if err := os.WriteFile(filepath.Join(stateDir, "refresh.token"), []byte(planted), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := mustLoadConfig(t, cfgPath)

			// The flush names the command that fixes it.
			var fo, fe bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, code := runFlush(ctx, cfg, cfgPath, true, &fo, &fe); code == exitOK || !strings.Contains(fe.String(), "run: jevlin connect") {
				t.Fatalf("flush before renewal: exit %d\n%s", code, fe.String())
			}
			// So does doctor.
			var dout, derr bytes.Buffer
			_ = cmdDoctor([]string{"-config", cfgPath}, &dout, &derr)
			if !strings.Contains(dout.String(), "was not issued by this configuration's AS") || !strings.Contains(dout.String(), "run `jevlin connect`") {
				t.Errorf("doctor does not name the renewal:\n%s", dout.String())
			}
			if !shouldResume(cfg, noEnv) {
				t.Error("no resume would be started for an authorization this AS did not issue")
			}

			before := as.assertionRedemptionCount()
			if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK || !strings.Contains(out, "enrolled for mining") {
				t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
			}
			if as.assertionRedemptionCount() != before+1 {
				t.Fatalf("connect redeemed %d assertion(s), want 1", as.assertionRedemptionCount()-before)
			}
			store, err := auth.OpenStore(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if mine, err := store.AuthorizationFor(cfg.Mining.ASBaseURL); err != nil || !mine {
				t.Fatalf("after connect the authorization is not this AS's: %v %v", mine, err)
			}
			// And once renewed, connect leaves it alone.
			if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK || as.assertionRedemptionCount() != before+1 {
				t.Errorf("a second connect enrolled again: %d redemptions", as.assertionRedemptionCount()-before)
			}
		})
	}
}

// mining disable stops mining here and promises no revoke it cannot make:
// an authorization this AS did not issue cannot be revoked from here by this
// run or any later one, so no marker is left to keep starting resumes.
func TestDisablePromisesNoRevokeForAnAuthorizationThisASDidNotIssue(t *testing.T) {
	for name, planted := range plantedForeignAuthorizations {
		t.Run(name, func(t *testing.T) {
			cfgPath, stateDir, _, as := fullyEnrolledInstall(t)
			if err := os.WriteFile(filepath.Join(stateDir, "refresh.token"), []byte(planted), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := runMiningDisable(t, cfgPath)
			if code != exitOK || strings.Contains(out, "the AS will be told on the next run") || !strings.Contains(out, "cannot be revoked from here") {
				t.Fatalf("disable: exit %d\n%s\n%s", code, out, errOut)
			}
			store, err := auth.OpenStore(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if pending, _ := store.LoadRevokePending(); pending {
				t.Error("a revoke is still promised")
			}
			_ = os.Remove(resumeStampPath(stateDir))
			if shouldResume(mustLoadConfig(t, cfgPath), noEnv) {
				t.Error("a resume would still be started")
			}
			if n := as.revokeCallCount(); n != 0 {
				t.Errorf("%d revoke request(s) reached an AS", n)
			}

			// A marker an earlier run left is settled by the next flush the same way,
			// rather than retried for ever.
			if err := store.SaveRevokePending(); err != nil {
				t.Fatal(err)
			}
			var fo, fe bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, code := runFlush(ctx, mustLoadConfig(t, cfgPath), cfgPath, true, &fo, &fe); code != exitOK {
				t.Fatalf("flush: exit %d\n%s", code, fe.String())
			}
			if pending, _ := store.LoadRevokePending(); pending {
				t.Error("the flush left the marker for a revoke no run can make")
			}
			if n := as.revokeCallCount(); n != 0 {
				t.Errorf("%d revoke request(s) reached an AS", n)
			}
		})
	}
}
