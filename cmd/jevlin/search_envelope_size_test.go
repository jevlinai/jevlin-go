package main

// The envelope is what the host puts into the model's context, so its size
// is the agent's bill per search. The default view carries each page's text
// once: not again as the page's excerpts, not again as the candidates'
// citations, and not a second spelling of the page's URL.

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
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

// balanced_merged_live.json is one real answer, recorded on 2026-10-23 from
// the beta router (router-api.nyks.dev) by this client at a0ff6ca with
// -tier balanced -view merged -format json, the trace off, for the query
// "how does Go sync.Pool reuse objects across garbage collections": 12
// merged pages of 24, four providers with 8 citations each. It was checked
// before it was committed: no key, email, account or tenant identifier; its
// ids are a random request id, random provider run ids and the router's
// r_ page ids. The checksum keeps it the recording and not an edit of one.
const liveBalancedSHA256 = "b161bb809ed87c84646b0fd91e89b1a04e59bc45c2263e2c43290b7b44405a98"

// On a real search, where the repetition the default view drops is most of
// the bytes, the default envelope is at most half the full one. Bounded on
// this recording alone: the router's fixtures are too small to show it.
func TestTheDefaultViewIsLessThanHalfTheFull(t *testing.T) {
	raw := readSearchTestdata(t, "balanced_merged_live.json")
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != liveBalancedSHA256 {
		t.Fatal("balanced_merged_live.json is not the recording")
	}
	def := searchStdoutServing(t, raw, `{"version":1,"query":"q"}`)
	full := searchStdoutServing(t, raw, `{"version":1,"query":"q","view":"full"}`)
	// What v0.4.3 printed with no view: its default was full, and full is
	// that plus the named additions (TestTheFullViewOnlyAddsToWhatV043Sent).
	before, err := jsonWithout([]byte(strings.TrimSuffix(full, "\n")), fullViewAdditions)
	if err != nil {
		t.Fatal(err)
	}
	if pages := listOf(resultOf(t, decodeEnvelope(t, def))["merged"]); len(pages) != 12 {
		t.Fatalf("the default view has %d pages, the recording 12", len(pages))
	}
	t.Logf("default %d bytes; full %d bytes; v0.4.3's default %d bytes (%.0f%% of it)",
		len(def), len(full), len(before)+1, 100*float64(len(def))/float64(len(before)+1))
	if 2*len(def) > len(full) {
		t.Errorf("the default envelope is %d bytes, more than half the full one's %d", len(def), len(full))
	}
}
