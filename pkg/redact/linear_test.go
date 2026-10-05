package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// traceSlowInput is one row of testdata/trace_slow_inputs.json: a text that
// once took a scrubber time quadratic in its length, built to traceSourceCap
// bytes by repeating unit between prefix and suffix. cmd/jevlin's
// TestTheSharedSourceScrubsAdversarialInputsInLinearTime builds the same
// texts for the JavaScript scrubber.
type traceSlowInput struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Unit   string `json:"unit"`
	Suffix string `json:"suffix"`
}

// traceSourceCap is the largest entry either scrubber is given whole: an
// entry above it is omitted rather than scrubbed (hookTailBytes in
// cmd/jevlin, TRACE_SOURCE_CAP in the shared source).
const traceSourceCap = 256 * 1024

// traceLinearBound is how long TraceText may take over traceSourceCap bytes
// of any slow input. Every one of them took 8 s or more before it was made
// linear (an address after an address: 8.2 s) and takes under 70 ms after,
// so the bound fails the quadratic code by ten times and passes the linear
// code by ten times on the machine that measured it, which leaves a slower
// CI runner its own factor. The race detector slows this package about
// twenty times, and the bound with it.
const traceLinearBound = 800 * time.Millisecond

func (in traceSlowInput) build() string {
	n := (traceSourceCap - len(in.Prefix) - len(in.Suffix)) / len(in.Unit)
	return in.Prefix + strings.Repeat(in.Unit, n) + in.Suffix
}

func loadTraceSlowInputs(t *testing.T) []traceSlowInput {
	t.Helper()
	raw, err := os.ReadFile("testdata/trace_slow_inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []traceSlowInput
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("no slow inputs")
	}
	return inputs
}

// A model can write any of these, and the scrub runs before a search goes
// out. Each is timed at the largest size the scrubber is ever given; the
// best of three runs is what is held to the bound, so one descheduled run
// on a busy machine does not fail it, and a quadratic step fails all three.
func TestTraceTextIsLinearOnAdversarialInputs(t *testing.T) {
	bound := traceLinearBound
	if raceDetector {
		bound *= 25
	}
	for _, in := range loadTraceSlowInputs(t) {
		text := in.build()
		best := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			TraceText(text)
			if d := time.Since(start); d < best {
				best = d
			}
			if best <= bound {
				break
			}
		}
		if best > bound {
			t.Errorf("%s: TraceText took %v over %d bytes, bound %v", in.Name, best, len(text), bound)
		}
	}
}
