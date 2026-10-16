package main

import (
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
