package collector

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/mining/spool"
)

type heldByTheAS struct{}

func (e *heldByTheAS) Error() string {
	return "auth: submission refused with status 451 (REGION_UNVERIFIED)"
}
func (e *heldByTheAS) Deferred() bool { return true }

// A submission the AS defers — a region hold on the connecting address —
// is kept for a later attempt without spending one: however many times
// the hold repeats, the record is never quarantined for running out of
// attempts, its next attempt honors Retry-After, and health counts a
// deferral, not a fault. An ordinary retryable failure keeps the limit.
func TestADeferredSubmissionSpendsNoAttempt(t *testing.T) {
	sp, err := spool.Open(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	sub := newScripted()
	c := New(sp, sub, Options{
		BaseBackoff: time.Millisecond, MaxBackoff: time.Minute, MaxAttempts: 4,
		Now: func() time.Time { return now },
	})
	held := enqueue(t, sp, c, 1042)
	failing := enqueue(t, sp, c, 1043)
	sub.answers[held] = func(int) (bool, bool, time.Duration, error) {
		return false, false, 10 * time.Minute, &heldByTheAS{}
	}
	sub.answers[failing] = func(int) (bool, bool, time.Duration, error) {
		return false, false, 0, errors.New("auth: submission refused with status 503")
	}

	for range 10 {
		c.Drain(context.Background())
		now = now.Add(time.Hour) // every deadline is due again
	}

	pending, err := sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var got *spool.Record
	for i := range pending {
		if pending[i].ClientRecordID == held {
			got = pending[i]
		}
	}
	if got == nil {
		t.Fatalf("the deferred record left the queue: pending=%+v", pending)
	}
	if got.Attempts != 0 || got.TerminalReason != "" {
		t.Fatalf("a deferral spent attempts: attempts=%d terminal=%q", got.Attempts, got.TerminalReason)
	}
	// The last pass ran an hour before `now` was last advanced.
	if want := now.Add(-time.Hour + 10*time.Minute); !got.NextAttemptAt.Equal(want) {
		t.Fatalf("next attempt = %v, want Retry-After honored: %v", got.NextAttemptAt, want)
	}
	if n := sub.count(held); n != 10 {
		t.Fatalf("the deferred record was submitted %d times across 10 due passes, want 10", n)
	}
	h := c.Health()
	if h.Deferred != 10 {
		t.Fatalf("health: %+v, want 10 deferrals", h)
	}
	// The ordinary failure beside it still ran out of attempts, and it is
	// the only thing quarantined.
	if n := sub.count(failing); n != 4 {
		t.Fatalf("the ordinary failure was submitted %d times, want the 4-attempt limit", n)
	}
	if h.TerminalFailures != 1 || h.Quarantined != 1 {
		t.Fatalf("health: %+v, want exactly the ordinary failure terminal and quarantined", h)
	}
}
