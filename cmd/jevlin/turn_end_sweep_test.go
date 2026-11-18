package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A queued turn record is the sender's to remove. When the sender never
// runs, the record of scrubbed assistant text would otherwise stay on
// disk for good; the sweep that takes stale search marks takes it too,
// once it is old enough that no sender is coming. Fresh files of either
// kind, directories, and files of other kinds are left alone.
func TestTheSweepTakesUnsentTurnRecordsAndStaleMarksOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	plant := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldRecord := plant("a"+turnEndSuffix, turnEndMaxAge+time.Minute)
	freshRecord := plant("b"+turnEndSuffix, turnEndMaxAge-time.Minute)
	oldMark := plant("c"+turnSearchedSuffix, turnSearchedMaxAge+time.Minute)
	freshMark := plant("d"+turnSearchedSuffix, turnEndMaxAge+time.Minute) // old for a record, fresh for a mark
	other := plant("e.json", turnSearchedMaxAge+time.Minute)
	sub := filepath.Join(dir, "f"+turnEndSuffix)
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	sweepTurnMarks(dir, now)

	for _, p := range []string{oldRecord, oldMark} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s: still there after the sweep (err=%v)", filepath.Base(p), err)
		}
	}
	for _, p := range []string{freshRecord, freshMark, other, sub} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: gone after the sweep: %v", filepath.Base(p), err)
		}
	}
}
