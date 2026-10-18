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
	// The AS answers REGION_RESTRICTED for a sanctioned jurisdiction and,
	// under an `unknown_country: refuse` policy, for an address whose
	// country it could not tell (twilight-minis #54). The sentence must be
	// true of both, so it accuses nobody of being in a sanctioned place.
	if text := RegionRefusalText(RegionRestricted); strings.Contains(text, "sanction") {
		t.Errorf("REGION_RESTRICTED's sentence %q claims a sanctioned region; the AS also sends it for an unknown one", text)
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

// One note per (slot, epoch), kept together: a refusal of a newer epoch
// does not forget an older one whose records are still waiting, the same
// epoch's verdict is replaced in place, and a word this client cannot name
// is neither written nor read back as a note.
func TestRegionRefusalNotesAreKeptPerEpochAndRejectWordsTheClientDidNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := store.RegionRefusals(); err != nil || len(set) != 0 {
		t.Fatalf("fresh store: %+v err=%v", set, err)
	}
	if _, ok, err := store.LatestRegionRefusal(7); err != nil || ok {
		t.Fatalf("fresh store latest: ok=%v err=%v", ok, err)
	}
	if err := store.SaveRegionRefusal(7, 1042, RegionIneligible); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRegionRefusal(7, 1043, RegionUnverified); err != nil {
		t.Fatal(err)
	}
	set, err := store.RegionRefusals()
	if err != nil || len(set) != 2 || set[0].TargetEpoch != 1042 || set[1].TargetEpoch != 1043 || set[0].At == "" {
		t.Fatalf("two epochs: %+v err=%v", set, err)
	}
	if rec, ok, _ := store.RegionRefusalFor(7, 1042); !ok || rec.Code != RegionIneligible {
		t.Fatalf("the older epoch's note was lost: %+v ok=%v", rec, ok)
	}
	if rec, ok, _ := store.LatestRegionRefusal(7); !ok || rec.TargetEpoch != 1043 {
		t.Fatalf("latest: %+v ok=%v", rec, ok)
	}
	// The same epoch's verdict is replaced, not duplicated.
	if err := store.SaveRegionRefusal(7, 1043, RegionRestricted); err != nil {
		t.Fatal(err)
	}
	if set, _ := store.RegionRefusals(); len(set) != 2 || set[1].Code != RegionRestricted {
		t.Fatalf("not replaced in place: %+v", set)
	}
	// A code this client cannot explain is never written...
	if err := store.SaveRegionRefusal(7, 1044, "REGION_PLANTED"); err == nil {
		t.Fatal("unknown code saved")
	}
	// ...and one planted on disk is not a note on load.
	if err := os.WriteFile(filepath.Join(dir, regionRefusalsFile),
		[]byte(`[{"slot_id":7,"target_epoch":1,"code":"see https://evil.example","at":"2026-10-14T00:00:00Z"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if set, err := store.RegionRefusals(); err == nil || len(set) != 0 {
		t.Fatalf("planted note accepted: %+v err=%v", set, err)
	}
	if _, ok, err := store.LatestRegionRefusal(7); err == nil || ok {
		t.Fatalf("planted note read as latest: ok=%v err=%v", ok, err)
	}
	// The bound: the oldest entries go first.
	if err := os.WriteFile(filepath.Join(dir, regionRefusalsFile), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	for e := uint64(1); e <= maxRegionRefusals+3; e++ {
		if err := store.SaveRegionRefusal(7, e, RegionUnverified); err != nil {
			t.Fatal(err)
		}
	}
	if set, _ := store.RegionRefusals(); len(set) != maxRegionRefusals || set[0].TargetEpoch != 4 {
		t.Fatalf("bound: %d entries, first epoch %d", len(set), set[0].TargetEpoch)
	}
}

// An enrollment in an epoch clears only the note for that slot and epoch:
// a note for another epoch may still be holding back records the flush has
// not settled, and dropping it early would send them into the attempt
// limit. A slot this configuration no longer names is pruned as a whole.
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
	for _, n := range []struct{ slot, epoch uint64 }{{7, 1042}, {7, 1043}, {8, 1042}} {
		if err := store.SaveRegionRefusal(n.slot, n.epoch, RegionUnverified); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearRegionRefusalFor(7, 1042); err != nil {
		t.Fatal(err)
	}
	set, _ := store.RegionRefusals()
	if len(set) != 2 {
		t.Fatalf("clearing one epoch touched the others: %+v", set)
	}
	for _, rec := range set {
		if rec.SlotID == 7 && rec.TargetEpoch == 1042 {
			t.Fatal("an enrollment in the note's own epoch left it in place")
		}
	}
	if err := store.PruneRegionRefusalsForOtherSlots(7); err != nil {
		t.Fatal(err)
	}
	if set, _ := store.RegionRefusals(); len(set) != 1 || set[0].SlotID != 7 || set[0].TargetEpoch != 1043 {
		t.Fatalf("prune: %+v", set)
	}
}

// Status repeats the AS's most recent verdict, which is the latest note by
// when it was recorded, not by epoch number: an operator who pins an
// earlier epoch and is refused there again has a newer verdict than the
// later epoch's older one. A tie in the one-second timestamps goes to the
// higher epoch.
func TestTheLatestRegionRefusalIsTheMostRecentlyRecorded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveRegionRefusals([]RegionRefusal{
		{SlotID: 7, TargetEpoch: 1043, Code: RegionIneligible, At: "2026-10-12T09:00:00Z"},
		{SlotID: 7, TargetEpoch: 1042, Code: RegionUnverified, At: "2026-10-13T09:00:00Z"},
		{SlotID: 8, TargetEpoch: 1050, Code: RegionRestricted, At: "2026-10-14T09:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := store.LatestRegionRefusal(7); err != nil || !ok || rec.TargetEpoch != 1042 {
		t.Fatalf("LatestRegionRefusal(7) = %+v ok=%v err=%v, want 1042, refused more recently", rec, ok, err)
	}
	if err := store.saveRegionRefusals([]RegionRefusal{
		{SlotID: 7, TargetEpoch: 1043, Code: RegionIneligible, At: "2026-10-13T09:00:00Z"},
		{SlotID: 7, TargetEpoch: 1042, Code: RegionUnverified, At: "2026-10-13T09:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := store.LatestRegionRefusal(7); rec.TargetEpoch != 1043 {
		t.Fatalf("a tie went to epoch %d, want the higher, 1043", rec.TargetEpoch)
	}
}
