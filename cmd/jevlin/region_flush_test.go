package main

// The AS's region refusals (twilight-minis ADR-MINIS-0026) driven through
// runFlush, which is what production runs: one flush process per call,
// each with a new epochDriver. A test that ticks one driver twice is
// testing a process that does not exist, so everything here that claims a
// behavior across flushes runs two flushes.

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/mining/spool"
)

func regionRefusalFlush(t *testing.T, f *flushFixture) (stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	if _, code := runFlush(ctx, f.cfg, f.cfgPath, true, &out, &errb); code != exitOK {
		t.Fatalf("runFlush exit %d\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	return out.String(), errb.String()
}

// ADR-MINIS-0026 §2 and §6: all three refusals share 451 so the client
// treats them as "retry later", and the AS decides each attempt afresh. So
// every flush that asks the AS asks again — and the participant is told
// once per epoch and code, not once per flush process.
func TestARegionRefusalIsRetriedEachFlushAndExplainedOnce(t *testing.T) {
	for _, code := range []string{auth.RegionRestricted, auth.RegionIneligible, auth.RegionUnverified} {
		t.Run(code, func(t *testing.T) {
			as := newFakeAS(t)
			as.set(func(s *asState) {
				s.epoch, s.joinable, s.joinRefusalCode, s.joinRefusalStatus = 1042, true, code, http.StatusUnavailableForLegalReasons
			})
			f := newFlushFixture(t, as)
			_, first := regionRefusalFlush(t, f)
			_, second := regionRefusalFlush(t, f)

			if n := as.joinCalls.Load(); n != 2 {
				t.Fatalf("join calls = %d across two flushes, want 2: a 451 is retried later", n)
			}
			text := auth.RegionRefusalText(code)
			if n := strings.Count(first+second, text); n != 1 {
				t.Fatalf("the refusal was explained %d times across two flushes, want once\nfirst: %s\nsecond: %s", n, first, second)
			}
		})
	}
}

func spoolAttempts(t *testing.T, spoolDir string) []int {
	t.Helper()
	sp, err := spool.Open(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var attempts []int
	for _, rec := range pending {
		attempts = append(attempts, rec.Attempts)
	}
	return attempts
}

// An epoch the AS refused to enroll this installation in has no
// capability to give its observations, so asking for one is a credential
// complaint about nothing and delivering them is an attempt that cannot
// succeed — fifty of those and the record is quarantined, with a delivery
// fault on status, though nothing about the record is wrong. So after a
// refusal the flush asks for no capability, and the epoch's records wait
// untouched. When a later join succeeds — the hold lifted — they deliver.
func TestARegionRefusedEpochsRecordsWaitAndDeliverOnceJoined(t *testing.T) {
	for _, code := range []string{auth.RegionRestricted, auth.RegionIneligible, auth.RegionUnverified} {
		t.Run(code, func(t *testing.T) {
			as := newFakeAS(t)
			as.set(func(s *asState) {
				s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
				s.joinRefusalCode, s.joinRefusalStatus = code, http.StatusUnavailableForLegalReasons
				// Contract §34: no accepted enrollment, no capability.
				s.capabilityTwilightError = "ENROLLMENT_NOT_FOUND"
			})
			f := newFlushFixture(t, as)
			seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "refused-epoch")

			for range 2 {
				_, stderr := regionRefusalFlush(t, f)
				if strings.Contains(stderr, "no participation capability") {
					t.Fatalf("a region refusal was followed by a capability complaint:\n%s", stderr)
				}
			}
			if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 0 {
				t.Fatalf("spooled records' attempts = %v, want one record never attempted", got)
			}
			if n := as.submissionCalls.Load(); n != 0 {
				t.Fatalf("%d submission(s) for an epoch this installation is not in", n)
			}
			store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
				t.Fatalf("a refused epoch's waiting records were reported as a delivery fault: %+v", h)
			}

			as.set(func(s *asState) { s.joinRefusalCode, s.joinRefusalStatus, s.capabilityTwilightError = "", 0, "" })
			regionRefusalFlush(t, f)
			if n := spoolCount(t, f.cfg.Mining.SpoolDir); n != 0 || as.submissionCalls.Load() != 1 {
				t.Fatalf("after the join the spool holds %d and the AS saw %d submission(s), want 0 and 1",
					n, as.submissionCalls.Load())
			}
		})
	}
}

// Once the AS's current target has moved past an epoch it refused to
// enroll this installation in, no capability for that epoch can ever
// exist, and the records waiting on it are dropped quietly — the rule
// dropConflictedObservationsPastTarget applies to an epoch another
// installation holds. Not before: a hold may still lift within the epoch.
// The drop runs before this run's join, which may replace the note with a
// refusal of the new epoch, so a second refusal cannot strand the first
// epoch's records behind a note that no longer names them.
func TestARegionRefusedEpochsRecordsDropOnceTheTargetMovesPast(t *testing.T) {
	for _, next := range []struct {
		name    string
		refused bool
	}{{"the next epoch is joined", false}, {"the next epoch is refused too", true}} {
		t.Run(next.name, func(t *testing.T) {
			as := newFakeAS(t)
			as.set(func(s *asState) {
				s.epoch, s.joinable = 1042, true
				s.joinRefusalCode, s.joinRefusalStatus = auth.RegionRestricted, http.StatusUnavailableForLegalReasons
				s.capabilityTwilightError = "ENROLLMENT_NOT_FOUND"
			})
			f := newFlushFixture(t, as)
			seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "refused-epoch")
			if stdout, _ := regionRefusalFlush(t, f); strings.Contains(stdout, "dropped") {
				t.Fatalf("dropped while the target is still the refused epoch:\n%s", stdout)
			}
			if n := spoolCount(t, f.cfg.Mining.SpoolDir); n != 1 {
				t.Fatalf("spool count = %d, want the refused epoch's record waiting", n)
			}

			as.set(func(s *asState) {
				s.epoch = 1043
				if !next.refused {
					s.joinRefusalCode, s.joinRefusalStatus, s.capabilityTwilightError = "", 0, ""
				}
			})
			stdout, stderr := regionRefusalFlush(t, f)
			if n := spoolCount(t, f.cfg.Mining.SpoolDir); n != 0 {
				t.Fatalf("spool count = %d after the target moved past the refused epoch, want 0", n)
			}
			if !strings.Contains(stdout, "dropped 1 observation") || strings.Contains(stderr, "dropped") {
				t.Fatalf("the drop should be one informational stdout line\nstdout: %s\nstderr: %s", stdout, stderr)
			}
			store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			note, ok, err := store.LoadRegionRefusal()
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case next.refused && (!ok || note.TargetEpoch != 1043):
				t.Fatalf("note = %+v ok=%v, want the refusal of 1043", note, ok)
			case !next.refused && ok:
				t.Fatalf("note = %+v outlived the epoch it named and a successful join", note)
			}
		})
	}
}

func loadRegionNote(t *testing.T, f *flushFixture) (auth.RegionRefusal, bool) {
	t.Helper()
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	note, ok, err := store.LoadRegionRefusal()
	if err != nil {
		t.Fatal(err)
	}
	return note, ok
}

func saveRegionNote(t *testing.T, f *flushFixture, epoch uint64, code string) {
	t.Helper()
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRegionRefusal(testSlotID, epoch, code); err != nil {
		t.Fatal(err)
	}
}

// A refusal is followed by a join made some other way — `jevlin join`
// once the VPN is off, or another run — and the AS now reports this
// installation in the epoch. The note names that epoch, so it is stale: a
// flush that finds the join held clears it, or status would go on saying
// rewards are on hold while doctor says the installation is in.
func TestAJoinTheASAlreadyHoldsClearsThatEpochsRegionNote(t *testing.T) {
	as := newFakeAS(t)
	as.set(func(s *asState) { s.epoch, s.joinable, s.joinStatus = 1042, false, auth.JoinAccepted })
	f := newFlushFixture(t, as)
	saveRegionNote(t, f, 1042, auth.RegionUnverified)

	regionRefusalFlush(t, f)
	if note, ok := loadRegionNote(t, f); ok {
		t.Fatalf("the note %+v outlived a join the AS holds", note)
	}
}

// `jevlin join` is the other way a participant joins, and the one doctor's
// fix names. It keeps the same note the flush keeps: a refusal is recorded
// for status and doctor to repeat, and a join the AS accepts, or already
// holds, clears it.
func TestJevlinJoinKeepsTheRegionNote(t *testing.T) {
	as := newFakeAS(t)
	as.set(func(s *asState) {
		s.epoch, s.joinable = 1042, true
		s.joinRefusalCode, s.joinRefusalStatus = auth.RegionUnverified, http.StatusUnavailableForLegalReasons
	})
	f := newFlushFixture(t, as)
	join := func() (int, string) {
		var code int
		stderr := captureStderr(t, func() {
			captureStdout(t, func() { code = cmdJoin([]string{"-config", f.cfgPath}) })
		})
		return code, stderr
	}

	if code, stderr := join(); code == 0 || !strings.Contains(stderr, auth.RegionUnverified) {
		t.Fatalf("a refused join exited %d with stderr %q", code, stderr)
	}
	if note, ok := loadRegionNote(t, f); !ok || note.TargetEpoch != 1042 || note.Code != auth.RegionUnverified {
		t.Fatalf("after a refused join the note is %+v ok=%v", note, ok)
	}

	as.set(func(s *asState) { s.joinRefusalCode, s.joinRefusalStatus = "", 0 })
	if code, stderr := join(); code != 0 {
		t.Fatalf("an accepted join exited %d: %s", code, stderr)
	}
	if note, ok := loadRegionNote(t, f); ok {
		t.Fatalf("the note %+v outlived an accepted join", note)
	}

	saveRegionNote(t, f, 1042, auth.RegionUnverified)
	as.set(func(s *asState) { s.joinable, s.joinStatus = false, auth.JoinAccepted })
	if code, stderr := join(); code != 0 {
		t.Fatalf("a join the AS already holds exited %d: %s", code, stderr)
	}
	if note, ok := loadRegionNote(t, f); ok {
		t.Fatalf("the note %+v outlived a join the AS holds", note)
	}
}
