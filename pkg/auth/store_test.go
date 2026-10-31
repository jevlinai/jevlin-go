package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// The trace key is created once, owner-only, and reused; a symlinked or
// malformed key is refused rather than trusted.
func TestTraceKeyGeneratedOnceAndStable(t *testing.T) {
	s, dir := newStore(t)
	k1, err := s.TraceKey()
	if err != nil || len(k1) != traceKeyLen {
		t.Fatalf("TraceKey: %d bytes, %v", len(k1), err)
	}
	k2, err := s.TraceKey()
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("reload produced a different key: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, traceKeyFile))
	if err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Fatalf("trace.key perms = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	other, _ := newStore(t)
	if k3, err := other.TraceKey(); err != nil || bytes.Equal(k1, k3) {
		t.Fatalf("distinct installations share a trace key: %v", err)
	}

	short, shortDir := newStore(t)
	if err := os.WriteFile(filepath.Join(shortDir, traceKeyFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := short.TraceKey(); err == nil {
		t.Fatal("malformed trace.key accepted")
	}

	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, bytes.Repeat([]byte("k"), traceKeyLen), 0o600); err != nil {
		t.Fatal(err)
	}
	linked, linkedDir := newStore(t)
	if err := os.Symlink(target, filepath.Join(linkedDir, traceKeyFile)); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := linked.TraceKey(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked trace.key not refused: %v", err)
	}
}

// EnsureTraceKey is what setup and connect call, outside any sandbox, so that
// a search which cannot write the state directory finds the key and only
// reads it. It must say truthfully whether it made the key (setup reports a
// change from it), leave an existing key's bytes alone, and refuse what
// TraceKey refuses rather than replace it.
func TestEnsureTraceKeyMakesTheKeyOnceAndSaysSo(t *testing.T) {
	s, dir := newStore(t)
	path := TraceKeyPath(dir)
	if path != filepath.Join(dir, "trace.key") {
		t.Fatalf("TraceKeyPath = %q, want trace.key in the state directory", path)
	}
	created, err := s.EnsureTraceKey()
	if err != nil || !created {
		t.Fatalf("first EnsureTraceKey = %v, %v; want created", created, err)
	}
	first, err := os.ReadFile(path) // #nosec G304 -- the test's own state directory
	if err != nil || len(first) != traceKeyLen {
		t.Fatalf("trace.key is %d bytes, %v; want %d", len(first), err, traceKeyLen)
	}
	if info, err := os.Stat(path); err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Fatalf("trace.key perms = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	created, err = s.EnsureTraceKey()
	if err != nil || created {
		t.Fatalf("second EnsureTraceKey = %v, %v; want not created", created, err)
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(first, again) { // #nosec G304 -- same path
		t.Fatal("EnsureTraceKey rewrote an existing key")
	}
	if key, err := s.TraceKey(); err != nil || !bytes.Equal(key, first) {
		t.Fatalf("TraceKey after EnsureTraceKey = %x, %v; want the key on disk", key, err)
	}

	// A key that is not 32 bytes is refused and left where it lies.
	bad, badDir := newStore(t)
	badPath := TraceKeyPath(badDir)
	if err := os.WriteFile(badPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := bad.EnsureTraceKey(); err == nil || created {
		t.Fatalf("EnsureTraceKey over a short key = %v, %v; want a refusal", created, err)
	}
	if kept, _ := os.ReadFile(badPath); string(kept) != "short" { // #nosec G304 -- same path
		t.Fatalf("EnsureTraceKey replaced a key it refused: %q", kept)
	}
}

// PRIV-008 permission assertion: loose modes refuse the store.
func TestPermissionHazardsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics (Windows posture is documented best-effort, ADR-0008)")
	}
	s, dir := newStore(t)
	if _, err := s.TraceKey(); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, traceKeyFile)
	if err := os.Chmod(keyPath, 0o644); err != nil { // #nosec G302 -- deliberately loose: the test proves refusal
		t.Fatal(err)
	}
	if _, err := s.TraceKey(); err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("world-readable key not refused: %v", err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TraceKey(); err != nil {
		t.Fatalf("restored perms still refused: %v", err)
	}

	// Loose state directory refuses OpenStore entirely. The hazard is set with
	// Chmod, not the mode MkdirAll is given: that passes through the umask,
	// and under 0077 it would leave a 0700 directory with nothing to refuse.
	looseDir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(looseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(looseDir, 0o755); err != nil { // #nosec G302 -- deliberately loose: the test proves refusal
		t.Fatal(err)
	}
	if _, err := OpenStore(looseDir); err == nil {
		t.Fatal("group/world-accessible state dir accepted")
	}

	// Symlinked record refuses.
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(`{"agent_id":"a","status":"unclaimed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, dir2 := newStore(t)
	if err := os.Symlink(target, filepath.Join(dir2, agentRegistrationFile)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.LoadAgentRegistration(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked record not refused: %v", err)
	}
}

// Agent onboarding design §3: the client's cache of its own platform
// identity round-trips, including the fields a resumed poll updates.
func TestSaveLoadAgentRegistrationRoundTrips(t *testing.T) {
	s, _ := newStore(t)
	if _, ok, err := s.LoadAgentRegistration(); err != nil || ok {
		t.Fatalf("fresh store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	want := AgentRegistration{
		AgentID:        "agent-1",
		Status:         "unclaimed",
		ClaimExpiresAt: "2026-09-16T00:00:00Z",
	}
	if err := s.SaveAgentRegistration(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LoadAgentRegistration()
	if err != nil || !ok || got.AgentID != want.AgentID ||
		got.Status != want.Status || got.ClaimExpiresAt != want.ClaimExpiresAt ||
		len(got.Scopes) != 0 {
		t.Fatalf("got %+v ok=%v err=%v, want %+v", got, ok, err, want)
	}

	// Later overwrites earlier: a resumed poll advances the same record,
	// it does not create a second one.
	claimed := want
	claimed.Status = "claimed"
	claimed.Scopes = []string{"credits", "search"}
	if err := s.SaveAgentRegistration(claimed); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.LoadAgentRegistration()
	if err != nil || !ok {
		t.Fatalf("reload after advance: ok=%v err=%v", ok, err)
	}
	if got.Status != "claimed" || len(got.Scopes) != 2 {
		t.Fatalf("advance did not persist: %+v", got)
	}
}

// The registration is what a resumed process (a detached connect after a
// later search) reads before making any network call, so it must survive
// closing and reopening the store.
func TestAgentRegistrationSurvivesAcrossStoreReopens(t *testing.T) {
	_, dir := newStore(t)
	s1, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := AgentRegistration{AgentID: "agent-1", Status: "claimed", Scopes: []string{"search"}}
	if err := s1.SaveAgentRegistration(rec); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s2.LoadAgentRegistration()
	if err != nil || !ok || got.AgentID != "agent-1" || got.Status != "claimed" {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(filepath.Join(dir, "agent.json"))
	if err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Fatalf("agent.json perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestPendingRegistrationJournalIsStrictAndOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenRegistrationJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := PendingRegistration{
		AgentID:        "agent-journal",
		Key:            "sr-journal-key",
		ClaimURL:       "https://platform.jevlin.ai/claim/JOURNAL-1",
		ClaimCode:      "JOURNAL-1",
		ClaimExpiresAt: "2026-09-16T00:00:00Z",
		Status:         "unclaimed",
		PollIntervalMS: 2000,
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok || got.AgentID != want.AgentID || got.Key != want.Key || got.ClaimURL != want.ClaimURL || got.PollIntervalMS != want.PollIntervalMS {
		t.Fatalf("got %+v ok=%v err=%v, want %+v", got, ok, err, want)
	}
	info, err := os.Stat(filepath.Join(dir, "registration_pending.json"))
	if err != nil || (posixModes && info.Mode().Perm() != 0o600) {
		t.Fatalf("registration journal perms = %v, want 0600", info.Mode().Perm())
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Load(); err != nil || ok {
		t.Fatalf("cleared journal still loads: ok=%v err=%v", ok, err)
	}

	// Unknown fields are rejected rather than silently becoming an
	// unreviewed recovery input.
	if err := os.WriteFile(filepath.Join(dir, "registration_pending.json"), []byte(`{"v":1,"agent_id":"a","key":"sr-k","claim_url":"https://platform.jevlin.ai/c","claim_code":"c","claim_expires_at":"","status":"unclaimed","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Load(); err == nil || ok || !strings.Contains(err.Error(), "a field this client does not write") {
		t.Fatalf("journal with unknown field accepted: ok=%v err=%v", ok, err)
	}
}

// The origins a journal records are origins and nothing more: a path, a
// query, credentials or another scheme is refused on save, so what a
// publication records is what a binding compares.
func TestPendingRegistrationJournalHoldsOriginsOnly(t *testing.T) {
	s, err := OpenRegistrationJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := PendingRegistration{
		AgentID: "agent-journal", Key: "sr-journal-key", ClaimURL: "https://platform.jevlin.ai/claim/J",
		Status: "unclaimed",
	}
	for _, ok := range []string{"", "https://agents.example", "http://127.0.0.1:8080", "https://[::1]:8443"} {
		rec := base
		rec.Router, rec.AgentsAPI = ok, ok
		if err := s.Save(rec); err != nil {
			t.Errorf("origin %q refused: %v", ok, err)
			continue
		}
		got, loaded, err := s.Load()
		if err != nil || !loaded || got.Router != ok || got.AgentsAPI != ok {
			t.Errorf("origin %q did not round-trip: %+v %v %v", ok, got, loaded, err)
		}
	}
	for _, bad := range []string{"https://agents.example/", "https://agents.example/v1", "https://agents.example?x=1",
		"https://u:p@agents.example", "ftp://agents.example", "agents.example", "https://"} {
		for _, field := range []string{"router", "agents_api"} {
			rec := base
			if field == "router" {
				rec.Router = bad
			} else {
				rec.AgentsAPI = bad
			}
			if err := s.Save(rec); err == nil {
				t.Errorf("%s %q accepted", field, bad)
			}
		}
	}
}

func TestPendingExpiredReplacementJournalBindsPreviousIdentity(t *testing.T) {
	s, err := OpenRegistrationJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := PendingRegistration{
		AgentID:         "agent-new",
		Key:             "sr-new-key",
		ClaimURL:        "https://platform.jevlin.ai/claim/NEW",
		ClaimCode:       "NEW",
		ClaimExpiresAt:  "2026-09-16T00:00:00Z",
		Status:          "unclaimed",
		ReplaceExpired:  true,
		PreviousAgentID: "agent-old",
		PreviousKey:     "sr-old-key",
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok || !got.ReplaceExpired || got.PreviousAgentID != want.PreviousAgentID || got.PreviousKey != want.PreviousKey {
		t.Fatalf("replacement journal lost previous binding: got=%+v ok=%v err=%v", got, ok, err)
	}
}
