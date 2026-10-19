package main

// result.merged is the router's merged view, mapped field for field
// (Jevlin-core's search-api-reference, "Merged view (opt-in)"). This client
// used to compute a second merge of its own over the candidates; that merge
// and the tests that pinned it are gone, and nothing here builds a merged
// list client-side.
//
// The fixtures are the router's own (testdata/search/router/README.md):
// its generated merged-view-success.json as the deployed router sends it
// and as Jevlin-core #55 sends it, byte for byte, and one response derived
// from the #55 one by the router's own code, with a second query form. Each
// test reads a fixture twice — as bytes for the fake router to serve, and
// as plain JSON to check the envelope against — and never through this
// client's decoder, which is what is under test.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readRouterFixture(t *testing.T, name string) (raw []byte, doc map[string]any) {
	t.Helper()
	raw = readSearchTestdata(t, filepath.Join("router", name))
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("router/%s: %v", name, err)
	}
	return raw, doc
}

// searchServing runs one --stdin search against a fake router that answers
// with raw, and returns the envelope, the body the router received and
// what the search wrote on stderr.
func searchServing(t *testing.T, raw []byte, stdin string) (env, sent map[string]any, stderr string) {
	t.Helper()
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	})
	h := fixedSearchOps(root)
	code, out, errOut := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	_, body := fr.last(t)
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("router body: %v", err)
	}
	return decodeEnvelope(t, out), sent, errOut
}

func resultOf(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", env)
	}
	return result
}

func listOf(v any) []any         { l, _ := v.([]any); return l }
func objOf(v any) map[string]any { m, _ := v.(map[string]any); return m }

// checkMergedAgainstTheRouter holds result.merged to the router's merged[]:
// the same pages in the same order, each mapped field for field.
func checkMergedAgainstTheRouter(t *testing.T, fixture, result map[string]any) {
	t.Helper()
	wire := listOf(fixture["merged"])
	got := listOf(result["merged"])
	if len(got) != len(wire) || len(wire) == 0 {
		t.Fatalf("result.merged has %d pages, the router sent %d", len(got), len(wire))
	}
	for i := range wire {
		w, g := objOf(wire[i]), objOf(got[i])
		if g["url"] != w["url"] || g["title"] != w["title"] {
			t.Errorf("page %d: %v / %v, the router's is %v / %v", i, g["url"], g["title"], w["url"], w["title"])
		}
		if g["snippet"] != w["excerpt"] {
			t.Errorf("page %d: snippet is not the router's excerpt:\n%v\n%v", i, g["snippet"], w["excerpt"])
		}
		if !reflect.DeepEqual(g["excerpts"], w["excerpts"]) {
			t.Errorf("page %d: excerpts %v, the router's %v", i, g["excerpts"], w["excerpts"])
		}
		if g["best_rank"] != w["best_rank"].(float64)-1 {
			t.Errorf("page %d: best_rank %v, want the router's %v less one", i, g["best_rank"], w["best_rank"])
		}
		// found_by: the providers, each once, in the router's order.
		var want []any
		seen := map[any]bool{}
		for _, fb := range listOf(w["found_by"]) {
			if p := objOf(fb)["provider"]; !seen[p] {
				seen[p] = true
				want = append(want, p)
			}
		}
		if !reflect.DeepEqual(g["found_by"], want) {
			t.Errorf("page %d: found_by %v, want %v", i, g["found_by"], want)
		}
		checkFoundIn(t, fixture, w, g)
	}
}

// checkFoundIn holds each found_in position to the router's answer:
// position i is the candidate of found_by[i], and the citation it names is
// the one the router listed the page at — the form and rank of that
// provider's first found_by entry on the wire.
func checkFoundIn(t *testing.T, fixture, wirePage, page map[string]any) {
	t.Helper()
	candidates := listOf(fixture["candidates"])
	foundBy, foundIn := listOf(page["found_by"]), listOf(page["found_in"])
	if len(foundIn) != len(foundBy) {
		t.Fatalf("%v: %d found_in positions for %d found_by providers", page["url"], len(foundIn), len(foundBy))
	}
	for i, pos := range foundIn {
		ci := int(objOf(pos)["candidate"].(float64))
		ti := int(objOf(pos)["citation"].(float64))
		cand := objOf(candidates[ci])
		if cand["provider"] != foundBy[i] {
			t.Errorf("%v: found_in[%d] names candidate %d, %v, not %v", page["url"], i, ci, cand["provider"], foundBy[i])
			continue
		}
		var first map[string]any
		for _, fb := range listOf(wirePage["found_by"]) {
			if objOf(fb)["provider"] == foundBy[i] {
				first = objOf(fb)
				break
			}
		}
		cit := objOf(listOf(cand["citations"])[ti])
		if cit["rank"] != first["rank"] || cit["form"] != first["form"] {
			t.Errorf("%v: found_in[%d] names %v's citation %d (rank %v, form %v); the router listed the page at rank %v, form %v",
				page["url"], i, foundBy[i], ti, cit["rank"], cit["form"], first["rank"], first["form"])
		}
	}
}

// Today's router (main, as deployed): result.merged is its merged[], in
// its order, with the excerpts it sends, and no field #55 adds.
func TestTheMergedListIsTheRoutersInItsOrder(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-success.main.json")
	env, sent, stderr := searchServing(t, raw, `{"version":1,"query":"q"}`)
	if sent["view"] != "merged" {
		t.Fatalf("the search did not ask the router for its merged view: %v", sent)
	}
	result := resultOf(t, env)
	checkMergedAgainstTheRouter(t, fixture, result)
	for _, k := range []string{"coverage", "merged_limit", "merged_total", "excerpts_trimmed", "forms_dropped"} {
		if _, ok := result[k]; ok {
			t.Errorf("result carries %s, which this router never sent", k)
		}
	}
	for _, p := range listOf(result["merged"]) {
		for _, k := range []string{"votes", "groups", "exclusive", "matched_terms", "canonical_url"} {
			if _, ok := objOf(p)[k]; ok {
				t.Errorf("%v carries %s, which this router never sent", objOf(p)["url"], k)
			}
		}
	}
	if stderr != "" {
		t.Errorf("a normal search wrote on stderr: %q", stderr)
	}
}

// After Jevlin-core #55: the same mapping, plus the router's corroboration
// for each page and its account of the whole merge, passed through.
func TestTheMergedListCarriesTheRoutersEvidence(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-success.merge-v2.json")
	env, _, _ := searchServing(t, raw, `{"version":1,"query":"q","view":"merged"}`)
	result := resultOf(t, env)
	checkMergedAgainstTheRouter(t, fixture, result)
	wire, got := listOf(fixture["merged"]), listOf(result["merged"])
	for i := range wire {
		w, g := objOf(wire[i]), objOf(got[i])
		for _, k := range []string{"votes", "groups", "canonical_url"} {
			if g[k] != w[k] {
				t.Errorf("page %d: %s %v, the router's %v", i, k, g[k], w[k])
			}
		}
		if w["exclusive"] == true && g["exclusive"] != true {
			t.Errorf("page %d: not exclusive, the router says it is", i)
		}
	}
	if !reflect.DeepEqual(result["coverage"], fixture["coverage"]) {
		t.Errorf("coverage %v, the router's %v", result["coverage"], fixture["coverage"])
	}
	if result["merged_limit"] != fixture["merged_limit"] || result["merged_total"] != fixture["merged_total"] {
		t.Errorf("window %v of %v, the router's %v of %v",
			result["merged_limit"], result["merged_total"], fixture["merged_limit"], fixture["merged_total"])
	}
}

// One candidate lists a page under the query and again under a
// reformulation, its ranks restarting for each form, so a rank names a
// citation only together with its form. Every found_in position must name
// the citation the router listed the page at.
func TestFoundInNamesTheCitationByItsFormAndRank(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-two-forms.derived.json")
	env, _, _ := searchServing(t, raw, `{"version":1,"query":"q"}`)
	result := resultOf(t, env)
	checkMergedAgainstTheRouter(t, fixture, result)

	// The page only the reformulation found: exa's third citation under
	// form 1 is the sixth in its list. rank − 1 would name the third,
	// which is a different page.
	var github map[string]any
	for _, p := range listOf(result["merged"]) {
		if objOf(p)["url"] == "https://github.com/example/rag_eval_bench" {
			github = objOf(p)
		}
	}
	if github == nil {
		t.Fatal("the reformulation's page is not in result.merged")
	}
	if want := []any{map[string]any{"candidate": float64(1), "citation": float64(5)}}; !reflect.DeepEqual(github["found_in"], want) {
		t.Errorf("found_in %v, want %v", github["found_in"], want)
	}
	if !reflect.DeepEqual(github["matched_terms"], []any{"rag_eval_bench"}) {
		t.Errorf("matched_terms %v", github["matched_terms"])
	}
	cov := objOf(result["coverage"])
	if !reflect.DeepEqual(cov["by_form"], []any{float64(5), float64(3)}) || cov["reformulation_only"] != float64(1) {
		t.Errorf("coverage %v, want by_form [5 3] and reformulation_only 1", cov)
	}
}

// found_in is read in step with found_by. When the router's answer does
// not let one provider's citation be found, the page carries no positions
// at all: a shorter list would pair every later position with the wrong
// provider. No router fixture can hold this, because it is the router
// breaking its own contract, so the test builds it from the main-shape
// fixture: brave's citation of the arxiv page is taken out of brave's list,
// and the merged view still names brave at rank 2.
func TestAPageWhosePositionCannotBeFoundCarriesNone(t *testing.T) {
	_, fixture := readRouterFixture(t, "merged-view-success.main.json")
	brave := objOf(listOf(fixture["candidates"])[0])
	if brave["provider"] != "brave" {
		t.Fatalf("the fixture's first candidate is %v, not brave", brave["provider"])
	}
	brave["citations"] = listOf(brave["citations"])[:1] // keep rank 1 only; the arxiv page was rank 2
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	env, _, _ := searchServing(t, raw, `{"version":1,"query":"q"}`)
	for _, p := range listOf(resultOf(t, env)["merged"]) {
		page := objOf(p)
		switch page["url"] {
		case "https://arxiv.org/abs/2603.01234":
			if got := listOf(page["found_in"]); len(got) != 0 {
				t.Errorf("found_in %v for a page whose brave citation is gone; want none", got)
			}
			if len(listOf(page["found_by"])) != 4 {
				t.Errorf("found_by %v; the router's four providers should stay", page["found_by"])
			}
		case "https://example.org/rag-eval":
			// brave's rank-1 citation is still there: this page keeps its positions.
			if len(listOf(page["found_in"])) != 2 {
				t.Errorf("found_in %v; want both positions", page["found_in"])
			}
		}
	}
}

// A 200 with no merged key, to a search that asked for the merged view, is
// the router breaking its contract. The search still ran: ok, an empty
// list rather than none, the candidates intact, and nothing on stderr,
// which machine mode never writes. balanced_response.json is a real
// response recorded without view: "merged", so it has no merged key.
func TestARouterThatOmitsItsMergedViewStillAnswersOK(t *testing.T) {
	env, _, stderr := searchServing(t, readSearchTestdata(t, "balanced_response.json"), `{"version":1,"query":"q"}`)
	if env["ok"] != true {
		t.Fatalf("not ok: %v", env)
	}
	result := resultOf(t, env)
	if merged, ok := result["merged"].([]any); !ok || len(merged) != 0 {
		t.Errorf("result.merged is %#v, want []", result["merged"])
	}
	if len(listOf(result["candidates"])) == 0 {
		t.Error("the candidates did not survive the missing merged view")
	}
	if stderr != "" {
		t.Errorf("machine mode wrote on stderr: %q", stderr)
	}
}

// view: "merged" travels on every --stdin search, whatever the caller
// asked; the caller's view decides only whether the envelope carries the
// candidates.
func TestEveryStdinSearchAsksForTheMergedView(t *testing.T) {
	raw, _ := readRouterFixture(t, "merged-view-success.main.json")
	for _, tc := range []struct {
		name, stdin    string
		wantCandidates bool
	}{
		{"absent", `{"version":1,"query":"q"}`, true},
		{"full", `{"version":1,"query":"q","view":"full"}`, true},
		{"merged", `{"version":1,"query":"q","view":"merged"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, sent, _ := searchServing(t, raw, tc.stdin)
			if sent["view"] != "merged" {
				t.Errorf("router body: view %v, want merged", sent["view"])
			}
			if _, has := resultOf(t, env)["candidates"]; has != tc.wantCandidates {
				t.Errorf("candidates present %v, want %v", has, tc.wantCandidates)
			}
		})
	}
}

// routerCitation is copied into the envelope's machineCitation field by
// field, leaving out rank and form, which are the router's keys for finding
// found_in's positions. A field the router grows would be dropped by that
// copy without anyone deciding to: this list makes the decision a diff.
func TestTheRouterCitationsFieldsAreDecided(t *testing.T) {
	decided := []string{"URL url", "Title title,omitempty", "Snippet snippet,omitempty", "Rank rank,omitempty", "Form form,omitempty"}
	var got []string
	rt := reflect.TypeFor[routerCitation]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		got = append(got, f.Name+" "+f.Tag.Get("json"))
	}
	if !reflect.DeepEqual(got, decided) {
		t.Errorf("routerCitation's fields are %q; this test lists %q.\n"+
			"Decide whether the envelope's machineCitation carries the new one (machineResultOf), then update the list.", got, decided)
	}
}

// The two router fixtures are the router's bytes, and the derived one is
// what its README says it is: a fixture edited by hand would test this
// client against a router that never existed.
func TestTheRouterFixturesAreTheOnesRecorded(t *testing.T) {
	dir := filepath.Join("testdata", "search", "router")
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS")) // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	listed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("SHA256SUMS: malformed line %q", line)
		}
		listed++
		raw, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 G703 -- a name from the fixed SHA256SUMS beside it, in testdata
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256.Sum256(raw); hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s does not match SHA256SUMS", name)
		}
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) != listed {
		t.Errorf("%d JSON fixtures, %d listed in SHA256SUMS (%v)", len(entries), listed, err)
	}
}
