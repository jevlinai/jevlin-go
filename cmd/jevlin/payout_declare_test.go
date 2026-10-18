package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// setConfigPayoutAddress writes [mining] payout_address into the fixture's
// config, the participant's scripted answer.
func setConfigPayoutAddress(t *testing.T, cfgPath, address string) {
	t.Helper()
	raw, err := os.ReadFile(cfgPath) // #nosec G304 -- the test's own config
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "[mining]\n", "[mining]\npayout_address = \""+address+"\"\n", 1)
	if text == string(raw) {
		t.Fatalf("the fixture config has no [mining] table:\n%s", raw)
	}
	writeFileT(t, cfgPath, text)
}

func statusText(t *testing.T, cfgPath string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	printAgentIdentityStatus([]string{"-config", cfgPath}, &stdout, &stderr, os.Getenv)
	return stdout.String() + stderr.String()
}

// One reading of "the address this installation would declare". Each row is
// a state of the record, the config and the state directory; the poll,
// shouldResume and status must agree on it.
func TestOneAnswerToWhichAddressThisInstallationWouldDeclare(t *testing.T) {
	t.Run("an invalid config address does not hide the wallet's", func(t *testing.T) {
		_, as, cfgPath, stateDir := enrolledButUndeclared(t)
		walletDir, err := defaultWalletDir()
		if err != nil {
			t.Fatal(err)
		}
		writeWalletFixture(t, walletDir)
		own := walletFixtureAddress(t)
		plantStatePayout(t, stateDir, own)
		setConfigPayoutAddress(t, cfgPath, "cosmos1notours")
		if err := os.Remove(resumeStampPath(stateDir)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if !shouldResume(mustLoadConfig(t, cfgPath), os.Getenv) {
			t.Fatal("no resume would be spawned for the installation's own wallet address")
		}
		if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
			t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
		}
		if got := as.declaredAddress(); got != own {
			t.Fatalf("declared %q, want the wallet's %q", got, own)
		}
	})
	t.Run("an invalid config address is never declared", func(t *testing.T) {
		_, as, cfgPath, stateDir := enrolledButUndeclared(t)
		setConfigPayoutAddress(t, cfgPath, "cosmos1notours")
		if err := os.Remove(resumeStampPath(stateDir)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if shouldResume(mustLoadConfig(t, cfgPath), os.Getenv) {
			t.Fatal("a resume would be spawned with nothing it may declare")
		}
		if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
			t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
		}
		if n := as.declarationAttempts(); n != 0 {
			t.Fatalf("declared with only an invalid config address: %d attempts (%q)", n, as.declaredAddress())
		}
	})
	t.Run("nothing anywhere spawns no resume", func(t *testing.T) {
		_, _, cfgPath, stateDir := enrolledButUndeclared(t)
		if err := os.Remove(resumeStampPath(stateDir)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if shouldResume(mustLoadConfig(t, cfgPath), os.Getenv) {
			t.Fatal("a resume would be spawned after every search for an installation with nothing to declare")
		}
	})
	t.Run("status names a config address not yet recorded", func(t *testing.T) {
		_, _, cfgPath, _ := enrolledButUndeclared(t)
		setConfigPayoutAddress(t, cfgPath, participantAddress)
		out := statusText(t, cfgPath)
		if !strings.Contains(out, "payout address "+participantAddress+" (from mining.payout_address") {
			t.Fatalf("status does not name the config's address:\n%s", out)
		}
	})
}
