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
