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
	"os"
	"path/filepath"
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
// join or a submission on HTTP 451 with one of the three codes this client
// knows. Any other error, status or code is not a region refusal — a 451
// carrying a code this version cannot explain is left to the generic path,
// which prints the AS's own message.
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

// RegionRefusal is the note the driver leaves when the AS refuses a join
// under its region policy, so status and doctor can say why this
// installation is not in the epoch after the log line has scrolled away.
type RegionRefusal struct {
	SlotID      uint64 `json:"slot_id"`
	TargetEpoch uint64 `json:"target_epoch"`
	Code        string `json:"code"`
	// At is RFC 3339, UTC.
	At string `json:"at"`
}

const regionRefusalFile = "region_refusal.json"

// SaveRegionRefusal records the refusal, replacing any earlier note: the
// latest verdict is the one status should repeat. An unknown code is an
// error rather than a note, so a word this client cannot explain is never
// written to be printed later.
func (s *Store) SaveRegionRefusal(slotID, targetEpoch uint64, code string) error {
	rec := RegionRefusal{SlotID: slotID, TargetEpoch: targetEpoch, Code: code, At: time.Now().UTC().Format(time.RFC3339)}
	if err := checkRegionRefusal(rec); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("auth: encode region refusal: %w", err)
	}
	return s.saveStateFile(regionRefusalFile, raw)
}

// LoadRegionRefusal returns the stored note, ok=false when there is none.
// A note that fails checkRegionRefusal is an error, not a note: status and
// doctor print its code's sentence, so a code this client does not know is
// not one it wrote.
func (s *Store) LoadRegionRefusal() (rec RegionRefusal, ok bool, err error) {
	raw, err := s.readSecret(regionRefusalFile)
	if errors.Is(err, fs.ErrNotExist) {
		return RegionRefusal{}, false, nil
	}
	if err != nil {
		return RegionRefusal{}, false, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return RegionRefusal{}, false, &decodeError{what: "region refusal", err: err}
	}
	if err := checkRegionRefusal(rec); err != nil {
		return RegionRefusal{}, false, err
	}
	return rec, true, nil
}

// ClearRegionRefusal removes the note, whatever it names.
func (s *Store) ClearRegionRefusal() error {
	err := os.Remove(filepath.Join(s.dir, regionRefusalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ClearRegionRefusalFor removes the note only when it names this slot and
// epoch: the AS has enrolled this installation in that epoch, so the note
// explaining why it was not is stale. A note for another epoch is left —
// its records may still be waiting for the flush to settle them — and so
// is one that cannot be read, since nothing in it can be matched.
func (s *Store) ClearRegionRefusalFor(slotID, targetEpoch uint64) error {
	rec, ok, err := s.LoadRegionRefusal()
	if err != nil || !ok || rec.SlotID != slotID || rec.TargetEpoch != targetEpoch {
		return nil
	}
	return s.ClearRegionRefusal()
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
