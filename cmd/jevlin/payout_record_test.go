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
	if !lexists(filepath.Join(stateDir, "payout.json")) {
		t.Fatal("a payout.json that was not adopted was removed")
	}
	if _, ok, _ := testPayoutRecord(t, stateDir).Load(); ok {
		t.Fatal("an address from the state directory was recorded")
	}
	if strings.Contains(out, plantedAddress) {
		t.Fatalf("the resume repeated the planted address:\n%s", out)
	}
}

// The review: "the AS has exactly this address in force" was the whole
// adoption rule, and a sandboxed command can make it true by itself. It runs
// `jevlin payout set` with its own address in the window before the
// participant's first declaration, plants a matching payout.json, and the
// next foreground connect recorded the attacker's address beside
// credentials.json as the participant's, where later declarations read it
// and status shows it. Only this installation's own wallet address is
// adopted now; this one is not recorded, declared or repeated.
func TestAnAddressTheSandboxMadeActiveIsNotAdopted(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	legacy := plantStatePayout(t, stateDir, plantedAddress)
	as.setActiveAddress(plantedAddress)

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if n := as.declarationAttempts(); n != 0 {
		t.Fatalf("declared %q (%d attempts)", as.declaredAddress(), n)
	}
	if _, ok, _ := testPayoutRecord(t, stateDir).Load(); ok {
		t.Fatal("the sandbox's address was recorded as the participant's")
	}
	if !lexists(legacy) {
		t.Fatal("a payout.json that was not adopted was removed")
	}
	if strings.Contains(out+errOut, plantedAddress) {
		t.Fatalf("connect repeated the planted address:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "is not used") || !strings.Contains(out, "jevlin mining enable") {
		t.Fatalf("connect did not say what to do:\n%s", out)
	}
	var text, textErr strings.Builder
	_ = statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv)
	if strings.Contains(text.String(), plantedAddress) || !strings.Contains(text.String(), "jevlin mining enable") {
		t.Fatalf("status repeated the planted address or gave no next step:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "a payout.json in the state directory is not used") {
		t.Fatalf("status does not say the state directory's payout.json is not used:\n%s", text.String())
	}
}

// A participant who upgraded has, usually, the wallet they made at the
// terminal: the payout.json an older version kept names it, and the wallet
// lives beside credentials.json, which the state directory cannot forge.
// That address is recorded, the state directory's copy removed, and
// declared if it is not settled yet.
func TestALegacyAddressThatIsTheInstallationsOwnWalletIsAdopted(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	walletDir, err := defaultWalletDir()
	if err != nil {
		t.Fatal(err)
	}
	writeWalletFixture(t, walletDir)
	own := walletFixtureAddress(t)
	legacy := plantStatePayout(t, stateDir, own)

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != own {
		t.Fatalf("record beside credentials.json = %q ok=%v err=%v", got, ok, err)
	}
	if lexists(legacy) {
		t.Fatal("the adopted address is still in the state directory")
	}
	if got := as.declaredAddress(); got != own {
		t.Fatalf("declared %q, want the installation's own wallet %q", got, own)
	}
}

// A scripted install's answer is the config's [mining] payout_address, in a
// directory no sandbox can write. With no record beside credentials.json
// (an upgrade from a version that kept it in the state directory), the
// resume records it and declares it, with no terminal and no prompt.
func TestTheConfigsPayoutAddressIsRecordedAndDeclared(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	plantStatePayout(t, stateDir, plantedAddress)
	raw, err := os.ReadFile(cfgPath) // #nosec G304 -- the test's own config
	if err != nil {
		t.Fatal(err)
	}
	cfgText := strings.Replace(string(raw), "[mining]\n", "[mining]\npayout_address = \""+participantAddress+"\"\n", 1)
	if cfgText == string(raw) {
		t.Fatalf("the fixture config has no [mining] table:\n%s", raw)
	}
	writeFileT(t, cfgPath, cfgText)
	// The enrolling resume above stamped its attempt; the next search's
	// resume comes after the cooldown.
	if err := os.Remove(resumeStampPath(stateDir)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !shouldResume(mustLoadConfig(t, cfgPath), noEnv) {
		t.Fatal("no resume would be spawned for the config's address")
	}

	if code, out, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("resume exited %d\n%s\n%s", code, out, errOut)
	}
	if got := as.declaredAddress(); got != participantAddress {
		t.Fatalf("declared %q, want the config's %q", got, participantAddress)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != participantAddress {
		t.Fatalf("record beside credentials.json = %q ok=%v err=%v", got, ok, err)
	}
}

// The ordinary state of an enrolled installation with nothing to adopt: no
// payout.json anywhere. A connect makes no standing call for it and says
// nothing about an older version's file.
func TestNothingInTheStateDirectoryMeansNoAdoptionAndNoWords(t *testing.T) {
	_, as, cfgPath, _ := enrolledButUndeclared(t)
	before := as.standingReads()
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if as.standingReads() != before {
		t.Fatalf("connect asked the AS where this participant is paid with nothing to adopt (%d calls)", as.standingReads()-before)
	}
	if strings.Contains(out+errOut, "payout.json") {
		t.Fatalf("connect spoke of a payout.json that is not there:\n%s%s", out, errOut)
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

// An older version's held note survives an upgrade beside the payout.json it
// was about. Once that address is the installation's own wallet and the AS
// has since put it in force (an operator activated the change), adopting it
// leaves no stale HELD for status to show for ever.
func TestAnAdoptionClearsAnOldHeldNote(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	walletDir, err := defaultWalletDir()
	if err != nil {
		t.Fatal(err)
	}
	writeWalletFixture(t, walletDir)
	own := walletFixtureAddress(t)
	plantStatePayout(t, stateDir, own)
	store := mustStore(t, stateDir)
	if err := store.SavePayoutBindingHeld(own, plantedAddress, auth.HeldReplacesActive); err != nil {
		t.Fatal(err)
	}
	as.setActiveAddress(own)

	if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect exited %d\n%s\n%s", code, out, errOut)
	}
	if _, ok, err := store.LoadPayoutBindingHeld(); err != nil || ok {
		t.Fatalf("the old held note survived the adoption: ok=%v err=%v", ok, err)
	}
	var text, textErr strings.Builder
	_ = statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv)
	if strings.Contains(text.String(), "HELD") {
		t.Fatalf("status still shows HELD:\n%s", text.String())
	}
}
