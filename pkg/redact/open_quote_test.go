package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// openQuoteGrid is testdata/trace_open_quote_grid.json: a quote a
// line-start name leaves open (each prefix before each first line), lines
// of other text, and then a secret assigned with a quoted value. The two
// scrubbers are held to it here and in cmd/jevlin's
// TestTheSharedSourceAgreesOnTheOpenQuoteGrid.
type openQuoteGrid struct {
	Prefixes    []string `json:"prefixes"`
	Firsts      []string `json:"firsts"`
	Gaps        []string `json:"gaps"`
	Laters      []string `json:"laters"`
	SecretWords []string `json:"secret_words"`
}

func (g openQuoteGrid) inputs() []string {
	var in []string
	for _, p := range g.Prefixes {
		for _, f := range g.Firsts {
			for _, gap := range g.Gaps {
				for _, l := range g.Laters {
					in = append(in, p+f+"\n"+gap+l)
				}
			}
		}
	}
	return in
}

func loadOpenQuoteGrid(t *testing.T) openQuoteGrid {
	t.Helper()
	raw, err := os.ReadFile("testdata/trace_open_quote_grid.json")
	if err != nil {
		t.Fatal(err)
	}
	var g openQuoteGrid
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// A quote a line-start name left open may run across lines, which is how a
// quoted key or a multi-line value is written; it must not run into a later
// secret's value and close at that value's opening quote, which left the
// rest of the value behind (`1. Set API_TOKEN="your token`, a step, then
// `DB_PASSWORD="correct horse battery"` kept `horse battery`). No word of
// either value may survive, and a second pass must change nothing.
func TestAnOpenQuoteNeverReachesALaterSecret(t *testing.T) {
	g := loadOpenQuoteGrid(t)
	inputs := g.inputs()
	if len(inputs) < 500 {
		t.Fatalf("fixture: the grid has %d inputs", len(inputs))
	}
	for _, in := range inputs {
		out := TraceText(in)
		for _, w := range g.SecretWords {
			if strings.Contains(out, w) {
				t.Errorf("%q kept %q:\n  %q", in, w, out)
				break
			}
		}
		if again := TraceText(out); again != out {
			t.Errorf("%q: a second pass changed the text\n  once:  %q\n  twice: %q", in, out, again)
		}
	}
}
