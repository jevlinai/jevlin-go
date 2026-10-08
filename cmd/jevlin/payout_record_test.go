package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

const (
	participantAddress = "twilight1lpdtlehaqn95mkcfgae8rut89s4pq9ayxdp4yc"
	plantedAddress     = "twilight1kl0dn0rtwk46h9zcmazyyrruta290crh93rnlh"
)

// plantStatePayout writes payout.json into the state directory the way a
// sandboxed command can, or the way a release before the payout record did.
func plantStatePayout(t *testing.T, stateDir, address string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"address": address})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "payout.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// enrolledButUndeclared connects, claims with the mining scope and enrolls,
// with no payout address on file anywhere: the window #51 is about, where
// the next resume declares whatever address it finds.
func enrolledButUndeclared(t *testing.T) (platform *stubPlatform, as *stubOnboardingAS, cfgPath, stateDir string) {
	t.Helper()
	withShortConnectTimings(t)
	platform = newStubPlatform(t)
	as = newStubAS(t)
	cfgPath, stateDir = connectConfig(t, platform.srv.URL, as.srv.URL)
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect failed: %s", errOut)
	}
	platform.claim("mining")
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("enrolling resume failed: %s", errOut)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.LastEnrollmentSlot == "" {
		t.Fatalf("not enrolled: %+v", reg)
	}
	if n := as.declarationAttempts(); n != 0 {
		t.Fatalf("declared before any address was on file: %d", n)
	}
	return platform, as, cfgPath, stateDir
}

// #51: a payout.json planted in the state directory, with mining enabled
// there too, was declared by the next resume, and a first declaration takes
// effect on arrival. The resume reads the record beside credentials.json
// only.
func TestTheResumeNeverDeclaresAnAddressOnlyTheStateDirNames(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	plantStatePayout(t, stateDir, plantedAddress)

	code, out, errOut := runConnect(t, cfgPath, nil, "-resume")
	if code != exitOK {
		t.Fatalf("resume exited %d\n%s\n%s", code, out, errOut)
	}
	if n := as.declarationAttempts(); n != 0 {
		t.Fatalf("the resume declared %q from the state directory (%d attempts)", as.declaredAddress(), n)
	}
}

// The participant's own address, beside credentials.json, is still
// declared by the resume unattended: #51's fix stopped the resume declaring
// at all, which left the window open longer for every late claimer.
func TestTheResumeDeclaresTheAddressBesideTheCredential(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	plantStatePayout(t, stateDir, plantedAddress)
	if err := testPayoutRecord(t, stateDir).Save(participantAddress); err != nil {
		t.Fatal(err)
	}

	if code, out, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("resume exited %d\n%s\n%s", code, out, errOut)
	}
	if got := as.declaredAddress(); got != participantAddress {
		t.Fatalf("declared %q, want the participant's own %q", got, participantAddress)
	}
}

// With no intake_dir the record would be ./payout.json, wherever the
// command ran: refused instead.
func TestThePayoutRecordNeedsAnIntakeDir(t *testing.T) {
	if _, err := payoutRecord(config.Miner{}); err == nil {
		t.Fatal("a payout record with no intake_dir was opened")
	}
	if _, _, err := loadPayoutAddress(config.Miner{}); err == nil {
		t.Fatal("a payout address was read with no intake_dir")
	}
}

// A terminal's answer is written beside credentials.json, never into the
// state directory.
func TestTheAddressQuestionWritesBesideTheCredential(t *testing.T) {
	platform := newStubPlatform(t)
	cfgPath, stateDir := scriptedMiningConfig(t, platform.srv.URL, "enabled = true\npayout_address = \""+participantAddress+"\"\n")
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustLoadConfig(t, cfgPath)
	if _, code := askMiningQuestion(strings.NewReader(""), nil, &strings.Builder{}, &strings.Builder{}, noEnv, cfg, store, false, false); code != exitOK {
		t.Fatalf("askMiningQuestion exited %d", code)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != participantAddress {
		t.Fatalf("record beside credentials.json = %q ok=%v err=%v", got, ok, err)
	}
	if lexists(filepath.Join(stateDir, "payout.json")) {
		t.Fatal("payout.json was written into the state directory")
	}
}
