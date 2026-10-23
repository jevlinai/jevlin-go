package main

// What the router already says about a search beyond its pages: each
// page's date and id, each provider's status, and whether the deadline cut
// the search short. Without them an agent cannot tell "nothing exists" from
// "an index timed out", cannot check a page's date, and cannot refer to a
// page later. Every expectation is read off the router's own fixture, never
// typed, and checked in both views, because these are signals, not bulk.

import (
	"reflect"
	"testing"
)

var bothViews = map[string]string{
	"default": `{"version":1,"query":"q"}`,
	"full":    `{"version":1,"query":"q","view":"full"}`,
}

// A page carries the router's published date when the router gave one and
// no published key when it did not, and every page carries its result_id.
func TestAPagesDateAndIDAreTheRouters(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-success.main.json")
	wire := listOf(fixture["merged"])
	dated := 0
	for _, w := range wire {
		if s, _ := objOf(w)["published"].(string); s != "" {
			dated++
		}
	}
	if dated == 0 || dated == len(wire) {
		t.Fatalf("the fixture must have pages with and without a date; %d of %d are dated", dated, len(wire))
	}
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			got := listOf(resultOf(t, env)["merged"])
			if len(got) != len(wire) {
				t.Fatalf("%d pages, the router sent %d", len(got), len(wire))
			}
			for i := range wire {
				w, g := objOf(wire[i]), objOf(got[i])
				if want, _ := w["published"].(string); want != "" {
					if g["published"] != want {
						t.Errorf("page %d: published %v, the router's %q", i, g["published"], want)
					}
				} else if _, has := g["published"]; has {
					t.Errorf("page %d: published %v, and the router gave none", i, g["published"])
				}
				if g["result_id"] != w["result_id"] || g["result_id"] == nil {
					t.Errorf("page %d: result_id %v, the router's %v", i, g["result_id"], w["result_id"])
				}
			}
		})
	}
}

// indexesOf is the router's indexes.by_provider as the envelope carries
// it: the same entries in the same order, with the four keys it keeps.
func indexesOf(fixture map[string]any) []any {
	var want []any
	for _, ix := range listOf(objOf(fixture["indexes"])["by_provider"]) {
		e := map[string]any{}
		for _, k := range []string{"provider", "status", "results", "latency_ms"} {
			if v, ok := objOf(ix)[k]; ok {
				e[k] = v
			}
		}
		want = append(want, e)
	}
	return want
}

// A degraded search says it was degraded: the arms that timed out, hit the
// deadline, were pending or were skipped are in result.indexes with their
// status and no result count, and usage.deadline_hit is true.
func TestADegradedSearchSaysWhichIndexesFailed(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-partial-failure.json")
	if objOf(fixture["usage"])["deadline_hit"] != true {
		t.Fatal("the fixture is not one whose deadline was hit")
	}
	want := indexesOf(fixture)
	failed := 0
	for _, ix := range want {
		if objOf(ix)["status"] != "ok" {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("the fixture has no failed arm")
	}
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			result := resultOf(t, env)
			if env["ok"] != true {
				t.Fatalf("a degraded search is still a search: %v", env)
			}
			if !reflect.DeepEqual(result["indexes"], want) {
				t.Errorf("indexes %v\nthe router's %v", result["indexes"], want)
			}
			if objOf(result["usage"])["deadline_hit"] != true {
				t.Errorf("usage %v does not say the deadline was hit", result["usage"])
			}
		})
	}
}

// A search where no arm answered names each arm's failure, an "error"
// among them, and carries no deadline_hit, because the router says false.
func TestASearchWithNoUsableArmNamesEachFailure(t *testing.T) {
	raw, fixture := readRouterFixture(t, "merged-view-no-usable.json")
	want := indexesOf(fixture)
	hasError := false
	for _, ix := range want {
		hasError = hasError || objOf(ix)["status"] == "error"
	}
	if !hasError || objOf(fixture["usage"])["deadline_hit"] != false {
		t.Fatal("the fixture needs an \"error\" arm and deadline_hit false")
	}
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			result := resultOf(t, env)
			if !reflect.DeepEqual(result["indexes"], want) {
				t.Errorf("indexes %v\nthe router's %v", result["indexes"], want)
			}
			if _, has := objOf(result["usage"])["deadline_hit"]; has {
				t.Errorf("usage %v carries deadline_hit; the router said false", result["usage"])
			}
			if pages := listOf(result["merged"]); len(pages) != 0 {
				t.Errorf("merged %v, the router's is empty", pages)
			}
		})
	}
}

// A router answer without an indexes block gets no indexes key, never an
// empty list standing in for "the router did not say".
func TestNoIndexesFromTheRouterIsNoIndexesKey(t *testing.T) {
	env, _, _ := searchServing(t, readSearchTestdata(t, "balanced_response.json"), `{"version":1,"query":"q","view":"full"}`)
	if v, has := resultOf(t, env)["indexes"]; has {
		t.Errorf("indexes %v, and the router sent none", v)
	}
}
