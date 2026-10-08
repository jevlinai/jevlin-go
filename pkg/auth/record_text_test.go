package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// plantedText carries what a sandboxed command would plant to act on the
// participant's terminal: an OSC 52 clipboard write, a one-byte C1 CSI and
// a right-to-left override. None of its bytes may ever be echoed back.
const plantedText = "x\x1b]52;c;ZXZpbA==\a\u009b2J\u202ey"

func assertNoPlantedBytes(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\a", "\u009b", "\u202e"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%s repeats a planted control character: %q", what, s)
		}
	}
}

// stringFieldsOf lists the JSON names of every string and string-slice
// field of a record type, so a field added to the type is planted here
// without anyone listing it.
func stringFieldsOf(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		k := f.Type.Kind()
		if k == reflect.String || (k == reflect.Slice && f.Type.Elem().Kind() == reflect.String) {
			names = append(names, jsonName(f))
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s has no string fields; the walk is broken", typ)
	}
	return names
}

func validAgentRegistrationJSON(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(AgentRegistration{
		AgentID: "agent-1", ClaimURL: "https://platform.example/claim/AB12", ClaimCode: "AB12-CD34",
		Status: "claimed", Scopes: []string{"search", "mining"}, ClaimExpiresAt: "2026-09-16T00:00:00Z",
		LastEnrollmentSlot: "twilight-slot-3", LastEnrollmentAt: "2026-09-16T00:00:00Z",
		SlotRefusal: "more than one slot offered",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAgentRegistrationRefusesAControlCharacterInEveryString(t *testing.T) {
	for _, field := range stringFieldsOf(t, reflect.TypeOf(AgentRegistration{})) {
		t.Run(field, func(t *testing.T) {
			s, dir := newStore(t)
			rec := validAgentRegistrationJSON(t)
			if _, isList := rec[field].([]any); isList {
				rec[field] = []any{"search", plantedText}
			} else {
				rec[field] = plantedText
			}
			raw, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, ok, err := s.LoadAgentRegistration()
			if ok || !errors.Is(err, ErrAgentRegistrationCorrupt) {
				t.Fatalf("a planted %s loaded: ok=%v err=%v", field, ok, err)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("the refusal does not name %s: %v", field, err)
			}
			assertNoPlantedBytes(t, "the refusal", err.Error())
		})
	}
}

func TestAValidAgentRegistrationStillLoads(t *testing.T) {
	s, dir := newStore(t)
	raw, err := json.Marshal(validAgentRegistrationJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadAgentRegistration(); !ok || err != nil {
		t.Fatalf("a valid record was refused: ok=%v err=%v", ok, err)
	}
}

func TestSaveAgentRegistrationRefusesWhatLoadWouldRefuse(t *testing.T) {
	s, dir := newStore(t)
	if err := s.SaveAgentRegistration(AgentRegistration{AgentID: "agent-1", Status: "claimed", Scopes: []string{plantedText}}); err == nil {
		t.Fatal("a record load would refuse was written")
	} else {
		assertNoPlantedBytes(t, "the refusal", err.Error())
	}
	if _, err := os.Lstat(filepath.Join(dir, "agent.json")); !os.IsNotExist(err) {
		t.Fatalf("agent.json was written: %v", err)
	}
}

// A health detail is often an error naming a path, which can hold ESC or
// BEL. MarkHealth writes it without them, and still writes the rest.
func TestMarkHealthWritesADetailATerminalCanPrint(t *testing.T) {
	s, _ := newStore(t)
	if err := s.MarkHealth(HealthCapture, HealthIntakeUnwritable, "open /tmp/"+plantedText+"/intake: denied"); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := s.LoadHealth(HealthCapture)
	if err != nil || !ok {
		t.Fatalf("the record MarkHealth wrote does not load: ok=%v err=%v", ok, err)
	}
	assertNoPlantedBytes(t, "the stored detail", rec.Detail)
	if !strings.Contains(rec.Detail, "intake: denied") {
		t.Fatalf("the detail lost its text: %q", rec.Detail)
	}
}

// The detail is printed by status and doctor; LoadHealth checked the
// version, the component and the reason, and not the detail beside them.
func TestLoadHealthRefusesADetailMarkHealthNeverWrites(t *testing.T) {
	for name, detail := range map[string]string{
		"escape":   plantedText,
		"over cap": strings.Repeat("d", healthDetailCap+1),
	} {
		t.Run(name, func(t *testing.T) {
			s, dir := newStore(t)
			raw, err := json.Marshal(HealthRecord{Version: healthVersion, Component: HealthFlush, Reason: HealthSubmissionFailed, Detail: detail})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, healthFlush), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := s.LoadHealth(HealthFlush); ok || err == nil {
				t.Fatalf("a planted detail loaded: ok=%v err=%v", ok, err)
			} else {
				assertNoPlantedBytes(t, "the refusal", err.Error())
			}
		})
	}
}

const validTestAddress = "twilight1kl0dn0rtwk46h9zcmazyyrruta290crh93rnlh"

// status prints the payout address and both halves of a held binding;
// connect declares the address. Each of the records refuses what a
// sandboxed command would plant, on load, and names no planted byte.
func TestPayoutRecordsRefuseAPlantedValueOnLoad(t *testing.T) {
	declared := func(s *Store) error {
		_, ok, err := s.LoadPayoutDeclared()
		return refusedUnlessOK(ok, err)
	}
	held := func(s *Store) error {
		_, ok, err := s.LoadPayoutBindingHeld()
		return refusedUnlessOK(ok, err)
	}
	legacy := func(s *Store) error {
		_, ok, err := s.LoadLegacyPayoutAddress()
		return refusedUnlessOK(ok, err)
	}
	for _, tc := range []struct {
		name, file string
		body       map[string]any
		load       func(s *Store) error
	}{
		{"legacy address/escape", "payout.json", map[string]any{"address": plantedText}, legacy},
		{"legacy address/not bech32", "payout.json", map[string]any{"address": "cosmos1kl0dn0rtwk46h9zcmazyyrruta290crhqxn5sp"}, legacy},
		{"declared/escape", "payout_declared.json", map[string]any{"address": plantedText}, declared},
		{"declared/not bech32", "payout_declared.json", map[string]any{"address": "twilight1notanaddress"}, declared},
		{"held/local", "payout_binding_held.json", map[string]any{"local": plantedText, "active": validTestAddress}, held},
		{"held/active", "payout_binding_held.json", map[string]any{"local": validTestAddress, "active": plantedText}, held},
		{"held/held_for", "payout_binding_held.json", map[string]any{"local": validTestAddress, "active": validTestAddress, "held_for": plantedText}, held},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := newStore(t)
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.file), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			err = tc.load(s)
			if err == nil {
				t.Fatalf("%s loaded %q", tc.file, fmt.Sprint(tc.body))
			}
			assertNoPlantedBytes(t, "the refusal", err.Error())
		})
	}
}

// The record beside credentials.json is validated on load too: a resume
// declares what it says.
func TestPayoutRecordRefusesAnInvalidAddressOnLoad(t *testing.T) {
	for _, bad := range []string{plantedText, "cosmos1kl0dn0rtwk46h9zcmazyyrruta290crhqxn5sp"} {
		p, home := newPayoutRecord(t)
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]string{"address": bad})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "payout.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := p.Load(); ok || err == nil {
			t.Fatalf("payout.json naming %q loaded", bad)
		} else {
			assertNoPlantedBytes(t, "the refusal", err.Error())
		}
	}
}

// The record is written only where it was opened: a state directory beside
// it, which is where releases before it kept payout.json, stays untouched.
func TestPayoutRecordNeverWritesTheStateDirectory(t *testing.T) {
	p, home := newPayoutRecord(t)
	state := filepath.Join(filepath.Dir(home), "state")
	s, err := OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Save(validTestAddress); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(state, "payout.json")); !os.IsNotExist(err) {
		t.Fatalf("payout.json was written to the state directory: %v", err)
	}
	if _, ok, err := s.LoadLegacyPayoutAddress(); ok || err != nil {
		t.Fatalf("the legacy loader found the new record: ok=%v err=%v", ok, err)
	}
}

// refusedUnlessOK folds a load's (ok, err) into the one question these
// tests ask: was the record accepted? nil means it was.
func refusedUnlessOK(ok bool, err error) error {
	if err == nil && !ok {
		return errors.New("not found")
	}
	return err
}

func TestPayoutRecordsRefuseToWriteWhatTheyWouldRefuseToLoad(t *testing.T) {
	s, dir := newStore(t)
	if err := s.SavePayoutDeclared("twilight1notanaddress"); err == nil {
		t.Fatal("an invalid declared address was written")
	}
	if err := s.SavePayoutBindingHeld(validTestAddress, plantedText, HeldReplacesActive); err == nil {
		t.Fatal("a held binding naming a planted active address was written")
	}
	for _, name := range []string{"payout_declared.json", "payout_binding_held.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was written: %v", name, err)
		}
	}
}
