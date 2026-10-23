package main

// The envelope is what the host puts into the model's context, so its size
// is the agent's bill per search. The default view carries each page's text
// once: not again as the page's excerpts, not again as the candidates'
// citations, and not a second spelling of the page's URL.

import (
	"path/filepath"
	"testing"
)

// On every router fixture the default view carries no candidates and no
// page carries excerpts or canonical_url. The sizes are logged, not
// bounded: the router's fixtures have one-line snippets, so the ratio on
// them says little about a real search (see
// TestTheDefaultViewIsLessThanHalfTheFull for one).
func TestTheDefaultViewRepeatsNothing(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "search", "router", "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no router fixtures: %v", err)
	}
	for _, name := range names {
		t.Run(filepath.Base(name), func(t *testing.T) {
			raw, _ := readRouterFixture(t, filepath.Base(name))
			def := searchStdoutServing(t, raw, `{"version":1,"query":"q"}`)
			full := searchStdoutServing(t, raw, `{"version":1,"query":"q","view":"full"}`)
			result := resultOf(t, decodeEnvelope(t, def))
			if v, has := result["candidates"]; has {
				t.Errorf("the default view carries candidates: %v", v)
			}
			for i, p := range listOf(result["merged"]) {
				for _, k := range []string{"excerpts", "canonical_url"} {
					if v, has := objOf(p)[k]; has {
						t.Errorf("page %d carries %s %v in the default view", i, k, v)
					}
				}
			}
			t.Logf("default %d bytes, full %d bytes (%.0f%%)", len(def), len(full), 100*float64(len(def))/float64(len(full)))
		})
	}
}
