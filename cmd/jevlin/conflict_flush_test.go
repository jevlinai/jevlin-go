package main

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// An epoch another installation of this participant holds can never give
// this one a capability, so its records cannot be delivered: each attempt
// fails at the exchange, counts toward the attempt limit, and reports a
// delivery fault, until the records are quarantined although nothing is
// wrong with them (issue #88). They wait instead, untouched, for the drop
// once the AS's target moves past the epoch. Both ways an epoch becomes
// conflicted are run through real flushes: the AS refusing the join, and
// the AS refusing the exchange because another installation holds the
// binding.
func TestAConflictedEpochsRecordsWaitWithoutAttemptsOrAFault(t *testing.T) {
	for name, set := range map[string]func(*asState){
		"ENROLLMENT_CONFLICT on join": func(s *asState) {
			s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
			s.joinRefusalCode = "ENROLLMENT_CONFLICT"
			// Contract §34: no accepted enrollment, no capability.
			s.capabilityTwilightError = "ENROLLMENT_NOT_FOUND"
		},
		"PROXY_BINDING_MISMATCH on exchange": func(s *asState) {
			s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
			s.capabilityTwilightError = "PROXY_BINDING_MISMATCH"
		},
	} {
		t.Run(name, func(t *testing.T) {
			as := newFakeAS(t)
			as.set(set)
			f := newFlushFixture(t, as)
			seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "conflicted-epoch")

			for range 3 {
				// Delivering a record starts with asking the AS for its
				// epoch's capability, and stops at the refusal before
				// anything is posted, so the exchange is what the AS sees
				// of a delivery attempt. The driver asks once a flush;
				// a second request is the record being submitted.
				exchanged := as.exchangesFor(1042)
				regionRefusalFlush(t, f)
				if n := as.exchangesFor(1042) - exchanged; n > 1 {
					t.Fatalf("a flush asked %d times for a capability for an epoch another installation holds: its record was submitted", n)
				}
			}
			if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 0 {
				t.Fatalf("spooled records' attempts = %v, want one record never attempted", got)
			}
			store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
				t.Fatalf("a conflicted epoch's waiting records were reported as a delivery fault: %+v", h)
			}
		})
	}
}

// The conflict entry is what holds its epoch's records back, so it goes
// only with them. Once the AS's target has moved past the epoch, a record
// the drop could not remove still has nothing to be delivered under; if
// its entry went anyway, the next flush submitted it, charged it an
// attempt, and no later drop ever named its epoch again. The removal is
// made to fail for real, by a spool directory that cannot be written for
// one flush.
func TestAConflictedRecordTheDropCouldNotRemoveStaysHeldUntilItIsRemoved(t *testing.T) {
	if !posixModes {
		t.Skip("needs POSIX mode bits to make a directory unwritable")
	}
	if os.Geteuid() == 0 {
		skipPermissionTest(t, "root removes through a read-only directory")
	}
	as := newFakeAS(t)
	as.set(func(s *asState) {
		s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
		s.capabilityTwilightError = "PROXY_BINDING_MISMATCH"
	})
	f := newFlushFixture(t, as)
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "conflicted-epoch")
	regionRefusalFlush(t, f)

	// The other installation holds the next epoch too, so nothing in this
	// installation's spool is ever deliverable.
	as.set(func(s *asState) { s.epoch = 1043 })
	spoolDir := f.cfg.Mining.SpoolDir
	if err := os.Chmod(spoolDir, 0o500); err != nil { // #nosec G302 -- the test's own directory
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spoolDir, 0o700) }) // #nosec G302 -- the test's own directory
	regionRefusalFlush(t, f)
	if err := os.Chmod(spoolDir, 0o700); err != nil { // #nosec G302 -- the test's own directory
		t.Fatal(err)
	}
	if spoolCount(t, spoolDir) != 1 {
		skipPermissionTest(t, "the spool directory's mode did not stop the drop's removal")
	}
	conflicts, err := store.EpochConflicts()
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, c := range conflicts {
		kept = kept || (c.SlotID == testSlotID && c.TargetEpoch == 1042)
	}
	if !kept {
		t.Fatalf("a failed drop removed the conflict entry that held its record back: conflicts = %+v", conflicts)
	}

	regionRefusalFlush(t, f)
	if n := spoolCount(t, spoolDir); n != 0 {
		t.Fatalf("the spool holds %d record(s) of an epoch the AS has moved past, attempts = %v; want the drop retried",
			n, spoolAttempts(t, spoolDir))
	}
}

// A flush that resolves no target (the AS has nothing open, as at every
// epoch boundary, or the lookup failed) still delivers what is spooled,
// through deliverOnly, and it holds back the same records the ordinary
// pass does: an epoch another installation holds, and one the AS refused
// to enroll this installation in. Without the hold there, every boundary
// spent an attempt, and a capability exchange, on records that cannot be
// delivered. The exchange is counted at the AS: deliverOnly's driver asks
// for no capability, so any exchange for the held epoch is a delivery.
func TestAFlushWithNoTargetHoldsBackTheRecordsItCannotDeliver(t *testing.T) {
	for name, set := range map[string]func(*asState){
		"another installation holds the epoch": func(s *asState) {
			s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
			s.capabilityTwilightError = "PROXY_BINDING_MISMATCH"
		},
		"the AS refused the join under its region policy": func(s *asState) {
			s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
			s.joinRefusalCode, s.joinRefusalStatus = auth.RegionRestricted, http.StatusUnavailableForLegalReasons
			s.capabilityTwilightError = "ENROLLMENT_NOT_FOUND"
		},
	} {
		t.Run(name, func(t *testing.T) {
			as := newFakeAS(t)
			as.set(set)
			f := newFlushFixture(t, as)
			seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "held-epoch")
			regionRefusalFlush(t, f)

			as.set(func(s *asState) { s.epoch = -1 })
			exchanged := as.exchangesFor(1042)
			_, stderr := regionRefusalFlush(t, f)
			if !strings.Contains(stderr, "no target epoch this run") {
				t.Fatalf("the second flush did not go through deliverOnly:\n%s", stderr)
			}
			if n := as.exchangesFor(1042) - exchanged; n != 0 {
				t.Fatalf("a flush with no target asked %d time(s) for a capability for a held epoch", n)
			}
			if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 0 {
				t.Fatalf("spooled records' attempts = %v, want one record never attempted", got)
			}
			store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
				t.Fatalf("a held record was reported as a delivery fault by a flush with no target: %+v", h)
			}
		})
	}
}

// failedJoinFlush is one flush in which a record is charged a real
// delivery failure: the AS answers the join with a transient error, so
// there is no enrollment and the record's exchange is refused. It leaves
// the record with one attempt and the flush's submission_failed on file,
// the state an installation is in when the AS has not yet said that
// another installation holds the epoch.
func failedJoinFlush(t *testing.T, as *fakeAS, f *flushFixture, store *auth.Store) {
	t.Helper()
	as.set(func(s *asState) {
		s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
		s.joinRefusalCode, s.joinRefusalStatus = "TEMPORARILY_UNAVAILABLE", http.StatusServiceUnavailable
		s.capabilityTwilightError = "ENROLLMENT_NOT_FOUND"
	})
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "before-the-conflict-was-known")
	regionRefusalFlush(t, f)
	if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 1 {
		t.Fatalf("fixture: attempts = %v after a failed delivery, want one record with one attempt", got)
	}
	if h, ok, _ := store.LoadHealth(auth.HealthFlush); !ok || h.Reason != auth.HealthSubmissionFailed {
		t.Fatalf("fixture: flush health = %+v ok=%v after a failed delivery, want submission_failed", h, ok)
	}
}

// A fault recorded before the AS said another installation holds the
// epoch was about records that can never be delivered. Once they are all
// that is left, and held, it is cleared, though no delivery came: one
// never can. Before, the held record still counted as a retry backlog,
// then as failed progress, and only an accepted delivery cleared the
// fault, which an installation that never holds an epoch never has; it
// outlived the records it was about (issue #88).
func TestAFaultAboutRecordsAnotherInstallationHoldsClearsOnceOnlyTheyRemain(t *testing.T) {
	as := newFakeAS(t)
	f := newFlushFixture(t, as)
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	failedJoinFlush(t, as, f, store)

	as.set(func(s *asState) { s.joinRefusalCode, s.joinRefusalStatus = "ENROLLMENT_CONFLICT", 0 })
	regionRefusalFlush(t, f)
	if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 1 {
		t.Fatalf("attempts = %v once the epoch was known to be another installation's, want the record held as it was", got)
	}
	if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
		t.Fatalf("only held records are left, and the fault about them stays: %+v", h)
	}

	as.set(func(s *asState) { s.epoch = 1043 })
	regionRefusalFlush(t, f)
	if n := spoolCount(t, f.cfg.Mining.SpoolDir); n != 0 {
		t.Fatalf("the spool holds %d record(s) of an epoch the AS has moved past", n)
	}
	if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
		t.Fatalf("the records are dropped, and a fault about them is on file: %+v", h)
	}
}

// The fault an older version left, about records of an epoch another
// installation holds, is cleared by the flush whose drop removes them:
// gone without a delivery that could ever have happened. The older
// version recorded the conflict and kept the fault, so that is the state
// planted here; the flush has nothing held and nothing deliverable left.
func TestAFaultAboutRecordsTheDropRemovedIsClearedWithThem(t *testing.T) {
	as := newFakeAS(t)
	f := newFlushFixture(t, as)
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	failedJoinFlush(t, as, f, store)
	if err := store.SaveEpochConflict(testSlotID, 1042); err != nil {
		t.Fatal(err)
	}

	// The next epoch is this installation's own.
	as.set(func(s *asState) {
		s.epoch, s.joinRefusalCode, s.joinRefusalStatus, s.capabilityTwilightError = 1043, "", 0, ""
	})
	stdout, _ := regionRefusalFlush(t, f)
	if !strings.Contains(stdout, "dropped 1 observation(s)") {
		t.Fatalf("the flush did not drop the conflicted epoch's record:\n%s", stdout)
	}
	if n := spoolCount(t, f.cfg.Mining.SpoolDir); n != 0 {
		t.Fatalf("the spool holds %d record(s) after the drop", n)
	}
	if h, ok, _ := store.LoadHealth(auth.HealthFlush); ok {
		t.Fatalf("the records a fault was about were dropped, and the fault stays: %+v", h)
	}
}

// Held records say nothing about a failure this run recorded itself. A
// flush whose current-target lookup fails marks that, then delivers what
// it can through deliverOnly; that every queued record is held must not
// clear the lookup's failure, which a later lookup clears when it works.
func TestHeldRecordsDoNotClearThisRunsTargetFailure(t *testing.T) {
	as := newFakeAS(t)
	as.set(func(s *asState) {
		s.epoch, s.joinable, s.acceptSubmissions = 1042, true, true
		s.capabilityTwilightError = "PROXY_BINDING_MISMATCH"
	})
	f := newFlushFixture(t, as)
	store, err := auth.OpenStoreExisting(f.cfg.Mining.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	seedIntakeRecord(t, f.cfg.Miner.IntakeDir, "conflicted-epoch")
	regionRefusalFlush(t, f)

	as.set(func(s *asState) { s.targetStatus = http.StatusServiceUnavailable })
	regionRefusalFlush(t, f)
	if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 0 {
		t.Fatalf("fixture: attempts = %v, want the one record held", got)
	}
	h, ok, err := store.LoadHealth(auth.HealthFlush)
	if err != nil || !ok || h.Reason != auth.HealthSubmissionFailed || !strings.HasPrefix(h.Detail, currentTargetHealthPrefix) {
		t.Fatalf("this run's current-target failure was not kept: %+v ok=%v err=%v", h, ok, err)
	}
}
