package main

import "testing"

// `payout set` changes the address in force, and so the address this
// installation would declare: it records it beside credentials.json. It
// used not to, and status kept naming the old address, and once
// payout_declared.json was gone a resume reported the participant's own
// change as a hold.
func TestPayoutSetRecordsTheAddressItDeclared(t *testing.T) {
	_, as, cfgPath, stateDir := enrolledButUndeclared(t)
	if err := testPayoutRecord(t, stateDir).Save(plantedAddress); err != nil {
		t.Fatal(err)
	}
	if code := cmdPayout([]string{"set", participantAddress, "-config", cfgPath}); code != 0 {
		t.Fatalf("payout set exited %d", code)
	}
	if got := as.declaredAddress(); got != participantAddress {
		t.Fatalf("the AS has %q, want %q", got, participantAddress)
	}
	if got, ok, err := testPayoutRecord(t, stateDir).Load(); err != nil || !ok || got != participantAddress {
		t.Fatalf("the record beside credentials.json = %q ok=%v err=%v, want the address payout set declared", got, ok, err)
	}
}
