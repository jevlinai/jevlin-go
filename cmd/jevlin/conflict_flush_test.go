package main

import (
	"os"
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
				regionRefusalFlush(t, f)
			}
			if got := spoolAttempts(t, f.cfg.Mining.SpoolDir); len(got) != 1 || got[0] != 0 {
				t.Fatalf("spooled records' attempts = %v, want one record never attempted", got)
			}
			if n := as.submissionCalls.Load(); n != 0 {
				t.Fatalf("%d submission(s) for an epoch another installation holds", n)
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
