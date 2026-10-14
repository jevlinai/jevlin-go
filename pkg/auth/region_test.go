package auth

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A region refusal is exactly a 451 carrying one of the three codes this
// client knows; everything else stays on the generic path.
func TestRegionRefusalCodeIsStrict(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
		ok   bool
	}{
		"restricted":            {&ASRefusedError{Status: 451, Code: RegionRestricted}, RegionRestricted, true},
		"ineligible":            {&ASRefusedError{Status: 451, Code: RegionIneligible}, RegionIneligible, true},
		"unverified":            {&ASRefusedError{Status: 451, Code: RegionUnverified}, RegionUnverified, true},
		"451 with unknown code": {&ASRefusedError{Status: 451, Code: "REGION_SOMETHING_NEW"}, "", false},
		"451 without a code":    {&ASRefusedError{Status: 451}, "", false},
		"known code on 403":     {&ASRefusedError{Status: http.StatusForbidden, Code: RegionRestricted}, "", false},
		"conflict":              {&ASRefusedError{Status: 409, Code: "ENROLLMENT_CONFLICT"}, "", false},
		"not an AS refusal":     {errors.New("auth: submission transport failed"), "", false},
		"nil":                   {nil, "", false},
	} {
		code, ok := RegionRefusalCode(tc.err)
		if code != tc.code || ok != tc.ok {
			t.Errorf("%s: got (%q, %v) want (%q, %v)", name, code, ok, tc.code, tc.ok)
		}
	}
	if !RegionHold(RegionUnverified) || RegionHold(RegionRestricted) || RegionHold(RegionIneligible) {
		t.Error("RegionHold is wrong about which code is the temporary one")
	}
	for _, code := range []string{RegionRestricted, RegionIneligible, RegionUnverified} {
		if text := RegionRefusalText(code); text == "" || strings.Contains(text, code) {
			t.Errorf("%s: text %q should be a sentence, not the code", code, text)
		}
	}
	if !strings.Contains(RegionRefusalText("REGION_X"), "REGION_X") {
		t.Error("an unknown code's text should at least name the code")
	}
	// The hold is retried within the epoch (ADR-MINIS-0026 §2: the next
	// attempt from an attributable address is decided afresh), so its
	// sentence must not send the participant to wait for the next epoch.
	if hold := RegionRefusalText(RegionUnverified); strings.Contains(hold, "next epoch") || !strings.Contains(hold, "next attempt") {
		t.Errorf("the hold's sentence %q must say the next attempt is decided afresh, not the next epoch", hold)
	}
}

func TestRegionRefusalNoteRoundTripsAndRejectsWordsItDidNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadRegionRefusal(); err != nil || ok {
		t.Fatalf("fresh store: ok=%v err=%v", ok, err)
	}
	if err := store.SaveRegionRefusal(7, 1042, RegionIneligible); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := store.LoadRegionRefusal()
	if err != nil || !ok || rec.SlotID != 7 || rec.TargetEpoch != 1042 || rec.Code != RegionIneligible || rec.At == "" {
		t.Fatalf("round trip: %+v ok=%v err=%v", rec, ok, err)
	}
	// Replaced, not appended: the latest verdict is the one status repeats.
	if err := store.SaveRegionRefusal(7, 1043, RegionUnverified); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := store.LoadRegionRefusal(); rec.TargetEpoch != 1043 || rec.Code != RegionUnverified {
		t.Fatalf("not replaced: %+v", rec)
	}
	// A code this client cannot explain is never written...
	if err := store.SaveRegionRefusal(7, 1044, "REGION_PLANTED"); err == nil {
		t.Fatal("unknown code saved")
	}
	// ...and one planted on disk is not a note on load.
	if err := os.WriteFile(filepath.Join(dir, regionRefusalFile),
		[]byte(`{"slot_id":7,"target_epoch":1,"code":"see https://evil.example","at":"2026-10-14T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadRegionRefusal(); err == nil || ok {
		t.Fatalf("planted note accepted: ok=%v err=%v", ok, err)
	}
	if err := store.ClearRegionRefusal(); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearRegionRefusal(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
	if _, ok, err := store.LoadRegionRefusal(); err != nil || ok {
		t.Fatalf("after clear: ok=%v err=%v", ok, err)
	}
}

// An enrollment in an epoch clears only a note naming that slot and epoch:
// a note for another epoch may still be holding back records the flush has
// not settled, and dropping it early would send them into the attempt
// limit.
func TestClearRegionRefusalForClearsOnlyItsOwnEpoch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClearRegionRefusalFor(7, 1042); err != nil {
		t.Fatalf("no note: %v", err)
	}
	if err := store.SaveRegionRefusal(7, 1042, RegionUnverified); err != nil {
		t.Fatal(err)
	}
	for _, other := range []struct{ slot, epoch uint64 }{{7, 1043}, {8, 1042}} {
		if err := store.ClearRegionRefusalFor(other.slot, other.epoch); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.LoadRegionRefusal(); !ok {
			t.Fatalf("an enrollment in slot %d epoch %d cleared the note for slot 7 epoch 1042", other.slot, other.epoch)
		}
	}
	if err := store.ClearRegionRefusalFor(7, 1042); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.LoadRegionRefusal(); ok {
		t.Fatal("an enrollment in the note's own epoch left it in place")
	}
}
