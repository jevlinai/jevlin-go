package main

// What the router says about the request itself (Jevlin-core #68): which
// filters it held the pages to and which build answered, and, once the
// router sends it, each page's name across searches. Every expectation for
// the first two is read off the beta recording that carries them, never
// typed; source_id has no recording yet, so its response is built here from
// a router fixture rather than written to a file beside the router's own.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const filtersRecording = "beta-balanced-filters-2026-10-23.json"

// result.filters_applied is the router's filters_applied, field for field,
// in both views: both filters, their providers, the not_sent list empty and
// still a list, and the router's dropped count.
func TestTheFilterReportIsTheRouters(t *testing.T) {
	raw, fixture := readRouterFixture(t, filtersRecording)
	want := fixture["filters_applied"]
	if len(objOf(want)) != 2 {
		t.Fatalf("the recording's filters_applied is %v; it must carry both filters", want)
	}
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			if got := resultOf(t, env)["filters_applied"]; !reflect.DeepEqual(got, want) {
				t.Errorf("filters_applied %v\nthe router's %v", got, want)
			}
		})
	}
}

// result.decision.build is the router's decision.build, in both views.
func TestTheBuildThatAnsweredIsTheRouters(t *testing.T) {
	raw, fixture := readRouterFixture(t, filtersRecording)
	want := objOf(fixture["decision"])["build"]
	if len(objOf(want)) != 2 {
		t.Fatalf("the recording's decision.build is %v", want)
	}
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			if got := objOf(resultOf(t, env)["decision"])["build"]; !reflect.DeepEqual(got, want) {
				t.Errorf("decision.build %v, the router's %v", got, want)
			}
		})
	}
}

// A router before #68 says nothing about filters or its build, and the
// envelope says nothing either: no filters_applied key standing in for
// "unknown", which is exactly what tells a caller its filter may not have
// been applied.
func TestNoFilterReportFromTheRouterIsNoKey(t *testing.T) {
	raw, _ := readRouterFixture(t, "merged-view-success.main.json")
	for view, stdin := range map[string]string{
		"default": `{"version":1,"query":"q","recency":"year","domain_filter":["python.org"]}`,
		"full":    `{"version":1,"query":"q","recency":"year","domain_filter":["python.org"],"view":"full"}`,
	} {
		t.Run(view, func(t *testing.T) {
			env, sent, _ := searchServing(t, raw, stdin)
			if sent["recency"] != "year" {
				t.Fatalf("the filter did not travel: %v", sent)
			}
			result := resultOf(t, env)
			if v, has := result["filters_applied"]; has {
				t.Errorf("filters_applied %v, and the router sent none", v)
			}
			if v, has := objOf(result["decision"])["build"]; has {
				t.Errorf("decision.build %v, and the router sent none", v)
			}
		})
	}
}

// One filter on the request is one key in the router's report, and the
// envelope does not grow the other.
func TestAFilterReportWithOneFilterCarriesOne(t *testing.T) {
	_, fixture := readRouterFixture(t, filtersRecording)
	delete(objOf(fixture["filters_applied"]), "domain_filter")
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	env, _, _ := searchServing(t, raw, `{"version":1,"query":"q","recency":"year"}`)
	got := objOf(resultOf(t, env)["filters_applied"])
	if _, has := got["domain_filter"]; has || got["recency"] == nil {
		t.Errorf("filters_applied %v; want recency alone", got)
	}
}

// withSourceIDs is the main-shape router fixture with a source_id on every
// page but the last, the way a router after track A sends them: the id is
// the router's, so the test makes one per URL and expects it back.
func withSourceIDs(t *testing.T) (raw []byte, ids []string) {
	t.Helper()
	_, fixture := readRouterFixture(t, "merged-view-success.main.json")
	pages := listOf(fixture["merged"])
	if len(pages) < 2 {
		t.Fatal("the fixture needs two pages")
	}
	for i, p := range pages {
		id := ""
		if i < len(pages)-1 {
			id = fmt.Sprintf("s_%032x", i+1)
			objOf(p)["source_id"] = id
		}
		ids = append(ids, id)
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return raw, ids
}

// A page's source_id is carried in every view when the router sent one, and
// a page without one carries no key.
func TestASourceIDIsCarriedInEveryView(t *testing.T) {
	raw, ids := withSourceIDs(t)
	for view, stdin := range bothViews {
		t.Run(view, func(t *testing.T) {
			env, _, _ := searchServing(t, raw, stdin)
			pages := listOf(resultOf(t, env)["merged"])
			if len(pages) != len(ids) {
				t.Fatalf("%d pages, the router sent %d", len(pages), len(ids))
			}
			for i, p := range pages {
				got, has := objOf(p)["source_id"]
				switch {
				case ids[i] == "" && has:
					t.Errorf("page %d: source_id %v, and the router sent none", i, got)
				case ids[i] != "" && got != ids[i]:
					t.Errorf("page %d: source_id %v, the router's %q", i, got, ids[i])
				}
			}
		})
	}
}

// The full view with source_ids is still v0.4.3's bytes plus the named
// additions: the main fixture's capture, with source_id the only difference.
func TestTheFullViewWithSourceIDsOnlyAddsThem(t *testing.T) {
	raw, _ := withSourceIDs(t)
	want := v043Capture(t, "full-a0ff6ca/merged-view-success.main.envelope.json")
	out := searchStdoutServing(t, raw, `{"version":1,"query":"q","view":"full"}`)
	if !strings.Contains(out, `"source_id":"s_`) {
		t.Fatal("the envelope carries no source_id; the comparison below would prove nothing")
	}
	got, err := jsonWithout([]byte(strings.TrimSuffix(out, "\n")), fullViewDropped())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Errorf("with source_id removed, the full view is not v0.4.3's:\n%s\n%s", got, want)
	}
}
