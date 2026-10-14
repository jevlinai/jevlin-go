package collector

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/mining/spool"
)

// A record the caller says cannot be delivered yet is left exactly as it
// is, however many passes run: never submitted, no attempt counted, no
// retry deadline written, never quarantined for running out of attempts,
// and no failure in health. Everything else is delivered as usual.
func TestASkippedRecordIsLeftExactlyAsItIs(t *testing.T) {
	sp, err := spool.Open(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	sub := newScripted()
	c := New(sp, sub, Options{
		BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, MaxAttempts: 4,
		Skip: func(rec *spool.Record) bool { return rec.TargetEpoch == 1042 },
	})
	held := enqueue(t, sp, c, 1042)
	free := enqueue(t, sp, c, 1043)

	for range 10 {
		c.Drain(context.Background())
	}

	if n := sub.count(held); n != 0 {
		t.Fatalf("a skipped record was submitted %d times", n)
	}
	if n := sub.count(free); n != 1 {
		t.Fatalf("the other record was submitted %d times, want 1", n)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ClientRecordID != held {
		t.Fatalf("pending = %+v, want only the skipped record", pending)
	}
	if rec := pending[0]; rec.Attempts != 0 || !rec.NextAttemptAt.IsZero() || rec.TerminalReason != "" {
		t.Fatalf("the skipped record was touched: attempts=%d next=%v terminal=%q", rec.Attempts, rec.NextAttemptAt, rec.TerminalReason)
	}
	if h := c.Health(); h.SubmissionFailures != 0 || h.TerminalFailures != 0 || h.Quarantined != 0 || h.RetryBacklog != 0 {
		t.Fatalf("health counts a skipped record as a fault: %+v", h)
	}
}
