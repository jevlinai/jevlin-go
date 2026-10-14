package auth

// The AS's region refusals (twilight-minis ADR-MINIS-0026): three §26
// codes on HTTP 451, meaning "not from here" rather than "not you". They
// are values here, as the other refusals are, so the driver, status and
// doctor branch on a code and never on prose.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"
)

const (
	// RegionRestricted: the connecting address is in a jurisdiction the
	// Slot cannot serve. Mining is refused from it.
	RegionRestricted = "REGION_RESTRICTED"
	// RegionIneligible: mining rewards are not available in the address's
	// jurisdiction. Search is unaffected.
	RegionIneligible = "REGION_INELIGIBLE"
	// RegionUnverified: the address is a known VPN, proxy or Tor exit, so
	// its region could not be verified. A hold, not a refusal: nothing is
	// forfeited, and a later attempt from a direct connection is decided
	// afresh.
	RegionUnverified = "REGION_UNVERIFIED"
)

// RegionRefusalCode reports the region code when err is the AS refusing a
// join on HTTP 451 with one of the three codes this client knows. Any other
// error, status or code is not a region refusal — a 451 carrying a code
// this version cannot explain is left to the generic path, which prints the
// AS's own message.
//
// A submission refused the same way never reaches it: Submit returns a
// RegionDeferredError naming the code and honoring the AS's Retry-After,
// because ADR-MINIS-0026 asks a client to retry a 451 later, and the AS
// accepts a held observation when it is resubmitted from an attributable
// address — and the collector defers it rather than counting an attempt.
func RegionRefusalCode(err error) (string, bool) {
	var refused *ASRefusedError
	if !errors.As(err, &refused) || refused.Status != http.StatusUnavailableForLegalReasons {
		return "", false
	}
	if !knownRegionCode(refused.Code) {
		return "", false
	}
	return refused.Code, true
}

func knownRegionCode(code string) bool {
	return code == RegionRestricted || code == RegionIneligible || code == RegionUnverified
}

// RegionHold reports whether a code is the temporary one.
func RegionHold(code string) bool { return code == RegionUnverified }

// RegionRefusalText is this client's own sentence for a region code — the
// AS's message is not stored, for the reason PayoutBindingHeld's reason is
// held to a closed set: the note lands in a directory a sandboxed command
// can rewrite, and status prints it as the client's word.
func RegionRefusalText(code string) string {
	switch code {
	case RegionRestricted:
		return "mining is not available from your region (the AS refused this address as sanctioned); search still works"
	case RegionIneligible:
		return "mining rewards are not available in your region; search still works"
	case RegionUnverified:
		return "rewards are on hold because this connection looks like a VPN, proxy or Tor exit; " +
			"connect directly and the next attempt is decided afresh — nothing is forfeited"
	default:
		return "the AS refused this address under its region policy (" + code + ")"
	}
}

// RegionRefusal is one note the driver leaves when the AS refuses a join
// under its region policy, so status and doctor can say why this
// installation is not in the epoch after the log line has scrolled away,
// and so the flush can hold back and later settle that epoch's records.
//
// One per (slot, epoch), kept together: a refusal of a newer epoch must
// not forget an older one whose records are still waiting, or those
// records lose their skip and run into the attempt limit (PR #85 review).
type RegionRefusal struct {
	SlotID      uint64 `json:"slot_id"`
	TargetEpoch uint64 `json:"target_epoch"`
	Code        string `json:"code"`
	// At is RFC 3339, UTC.
	At string `json:"at"`
}

const regionRefusalsFile = "region_refusals.json"

// maxRegionRefusals bounds the set the way maxEpochConflicts bounds the
// conflict set: an entry leaves as soon as a flush settles its epoch or an
// enrollment clears it, so only a slot this configuration no longer
// names could accumulate, and PruneRegionRefusalsForOtherSlots handles
// that; the cap is the backstop.
const maxRegionRefusals = 64

// SaveRegionRefusal records the AS's verdict for (slotID, targetEpoch),
// replacing an earlier verdict for the same epoch and leaving every other
// epoch's note alone. An unknown code is an error rather than a note, so
// a word this client cannot explain is never written to be printed later.
func (s *Store) SaveRegionRefusal(slotID, targetEpoch uint64, code string) error {
	rec := RegionRefusal{SlotID: slotID, TargetEpoch: targetEpoch, Code: code, At: time.Now().UTC().Format(time.RFC3339)}
	if err := checkRegionRefusal(rec); err != nil {
		return err
	}
	set, err := s.loadRegionRefusals()
	if err != nil {
		return err
	}
	replaced := false
	for i := range set {
		if set[i].SlotID == slotID && set[i].TargetEpoch == targetEpoch {
			set[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		set = append(set, rec)
	}
	if len(set) > maxRegionRefusals {
		set = set[len(set)-maxRegionRefusals:]
	}
	return s.saveRegionRefusals(set)
}

// RegionRefusals returns every note on file. A file that does not decode,
// or holds a note that fails checkRegionRefusal, is an error, not a set:
// status and doctor print a code's sentence, so a code this client does
// not know is not one it wrote.
func (s *Store) RegionRefusals() ([]RegionRefusal, error) {
	return s.loadRegionRefusals()
}

// RegionRefusalFor returns the note for one (slot, epoch), ok=false when
// there is none.
func (s *Store) RegionRefusalFor(slotID, targetEpoch uint64) (RegionRefusal, bool, error) {
	set, err := s.loadRegionRefusals()
	if err != nil {
		return RegionRefusal{}, false, err
	}
	for _, rec := range set {
		if rec.SlotID == slotID && rec.TargetEpoch == targetEpoch {
			return rec, true, nil
		}
	}
	return RegionRefusal{}, false, nil
}

// LatestRegionRefusal is the slot's note for its highest epoch: the
// verdict status repeats, since it is the one the next join will be
// measured against.
func (s *Store) LatestRegionRefusal(slotID uint64) (RegionRefusal, bool, error) {
	set, err := s.loadRegionRefusals()
	if err != nil {
		return RegionRefusal{}, false, err
	}
	var latest RegionRefusal
	found := false
	for _, rec := range set {
		if rec.SlotID == slotID && (!found || rec.TargetEpoch > latest.TargetEpoch) {
			latest, found = rec, true
		}
	}
	return latest, found, nil
}

// ClearRegionRefusalFor removes the note for this slot and epoch: the AS
// has enrolled this installation in that epoch, so the note explaining
// why it was not is stale. Every other epoch's note is left — its records
// may still be waiting for the flush to settle them. A set that cannot be
// read is left too, since nothing in it can be matched.
func (s *Store) ClearRegionRefusalFor(slotID, targetEpoch uint64) error {
	set, err := s.loadRegionRefusals()
	if err != nil {
		return nil
	}
	return s.RemoveRegionRefusals(set, func(rec RegionRefusal) bool {
		return rec.SlotID == slotID && rec.TargetEpoch == targetEpoch
	})
}

// PruneRegionRefusalsForOtherSlots drops every note whose slot is not
// currentSlotID, as PruneEpochConflictsForOtherSlots does: the flush only
// ever settles the current slot's epochs, so a note for another slot could
// sit on file forever.
func (s *Store) PruneRegionRefusalsForOtherSlots(currentSlotID uint64) error {
	set, err := s.loadRegionRefusals()
	if err != nil {
		return err
	}
	return s.RemoveRegionRefusals(set, func(rec RegionRefusal) bool { return rec.SlotID != currentSlotID })
}

// RemoveRegionRefusals writes set back without the notes drop selects.
// Nothing is written when nothing is dropped.
func (s *Store) RemoveRegionRefusals(set []RegionRefusal, drop func(RegionRefusal) bool) error {
	kept := make([]RegionRefusal, 0, len(set))
	for _, rec := range set {
		if !drop(rec) {
			kept = append(kept, rec)
		}
	}
	if len(kept) == len(set) {
		return nil
	}
	return s.saveRegionRefusals(kept)
}

func (s *Store) saveRegionRefusals(set []RegionRefusal) error {
	if set == nil {
		set = []RegionRefusal{}
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("auth: encode region refusals: %w", err)
	}
	return s.saveStateFile(regionRefusalsFile, raw)
}

func (s *Store) loadRegionRefusals() ([]RegionRefusal, error) {
	raw, err := s.readSecret(regionRefusalsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var set []RegionRefusal
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, &decodeError{what: "region refusals", err: err}
	}
	for _, rec := range set {
		if err := checkRegionRefusal(rec); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func checkRegionRefusal(rec RegionRefusal) error {
	// Neither value is repeated: this file is in a directory a sandboxed
	// command can rewrite, and the error reaches status and doctor.
	if !knownRegionCode(rec.Code) {
		return errors.New("auth: region refusal note names a code this client does not know")
	}
	if _, err := time.Parse(time.RFC3339, rec.At); err != nil {
		return errors.New("auth: region refusal note's time is not RFC 3339")
	}
	return nil
}

// RegionDeferredError is Submit's answer to a 451 carrying one of the
// three region codes: the record is not this installation's fault and not
// permanent, so the collector keeps it without spending an attempt on it
// (Deferred), and tries again after the AS's Retry-After or its own
// longest backoff. A hold that lifts — the VPN comes off — then delivers
// the record instead of finding it quarantined (PR #85 review). The text
// is the same line a retryable refusal has always carried.
type RegionDeferredError struct {
	Status int
	Code   string
}

func (e *RegionDeferredError) Error() string {
	return fmt.Sprintf("auth: submission refused with status %d (%s)", e.Status, e.Code)
}

// Deferred is the collector's signal (collector.Deferrer).
func (e *RegionDeferredError) Deferred() bool { return true }
