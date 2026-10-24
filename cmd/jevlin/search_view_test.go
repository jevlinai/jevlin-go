package main

// view: what a --stdin caller asks the envelope to carry. The router is
// asked for its merged view on every --stdin search, because the
// envelope's merged list is the router's; view decides only whether the
// envelope also carries the per-provider candidates.

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// TestViewMergedOmitsCandidates: view: merged carries no "candidates" key,
// and its merged list is the one "full" carries less each page's excerpts
// and canonical_url, which only "full" repeats; both asked the router for
// the same thing.
func TestViewMergedOmitsCandidates(t *testing.T) {
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json")))
	})
	h := fixedSearchOps(root)

	_, fullOut, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","view":"full"}`, "-config", cfg)
	fullEnv := decodeEnvelope(t, fullOut)
	fullResult, _ := fullEnv["result"].(map[string]any)
	if _, ok := fullResult["candidates"]; !ok {
		t.Fatalf("view:full has no candidates key: %s", fullOut)
	}

	_, mergedOut, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","view":"merged"}`, "-config", cfg)
	mergedEnv := decodeEnvelope(t, mergedOut)
	mergedResult, _ := mergedEnv["result"].(map[string]any)
	if _, ok := mergedResult["candidates"]; ok {
		t.Fatalf("view:merged still carries a candidates key: %s", mergedOut)
	}
	if pages, _ := mergedResult["merged"].([]any); len(pages) == 0 {
		t.Fatalf("view:merged has no merged pages: %s", mergedOut)
	}

	fullPages, _ := fullResult["merged"].([]any)
	repeated := 0
	for _, p := range fullPages {
		page, _ := p.(map[string]any)
		for _, k := range []string{"excerpts", "canonical_url"} {
			if _, ok := page[k]; ok {
				repeated++
				delete(page, k)
			}
		}
	}
	if repeated == 0 {
		t.Fatal("the full view carried no excerpts or canonical_url, so this compares nothing")
	}
	fullMerged, err1 := json.Marshal(fullPages)
	mergedMerged, err2 := json.Marshal(mergedResult["merged"])
	if err1 != nil || err2 != nil {
		t.Fatalf("marshal: %v %v", err1, err2)
	}
	if string(fullMerged) != string(mergedMerged) {
		t.Errorf("merged is not full's less excerpts and canonical_url:\nfull:   %s\nmerged: %s", fullMerged, mergedMerged)
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.reqs) != 2 {
		t.Fatalf("expected exactly 2 router calls, got %d", len(fr.reqs))
	}
}

// TestViewAbsentIsMerged: a request that never mentions "view" gets exactly
// what "merged" gets, byte for byte: the merged list without the
// candidates. Only an explicit "full" asks for those.
func TestViewAbsentIsMerged(t *testing.T) {
	_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json")))
	})
	h := fixedSearchOps(root)
	run := func(stdin string) string {
		code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, out)
		}
		return out
	}
	absent := run(`{"version":1,"query":"q"}`)
	if merged := run(`{"version":1,"query":"q","view":"merged"}`); absent != merged {
		t.Errorf("an absent view differs from \"merged\":\nabsent: %s\nmerged: %s", absent, merged)
	}
	result, _ := decodeEnvelope(t, absent)["result"].(map[string]any)
	if _, ok := result["candidates"]; ok {
		t.Errorf("an absent view carried candidates: %s", absent)
	}
	if pages, _ := result["merged"].([]any); len(pages) == 0 {
		t.Errorf("an absent view has no merged pages: %s", absent)
	}
}

// TestViewInvalidValueRefusedBeforeRouterCall: any value other than
// "full" or "merged" — including an explicit "" and the wrong JSON type
// — is fix_input, and never reaches the router.
func TestViewInvalidValueRefusedBeforeRouterCall(t *testing.T) {
	for _, tc := range []struct{ name, stdin string }{
		{"unknown word", `{"version":1,"query":"q","view":"summary"}`},
		{"empty string", `{"version":1,"query":"q","view":""}`},
		{"wrong type", `{"version":1,"query":"q","view":7}`},
		{"null", `{"version":1,"query":"q","view":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(routerBody))
			})
			h := fixedSearchOps(root)
			code, out, errOut := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, tc.stdin, "-config", cfg)
			if code != exitUsage {
				t.Fatalf("exit %d, want %d (%s)", code, exitUsage, out)
			}
			env := decodeEnvelope(t, out)
			if got := envField(t, env, "code"); got != codeInvalidView {
				t.Errorf("code %q, want %q", got, codeInvalidView)
			}
			if got := envField(t, env, "action"); got != actionFixInput {
				t.Errorf("action %q, want %q", got, actionFixInput)
			}
			if errOut != "" {
				t.Errorf("an expected protocol error also explained itself on stderr: %q", errOut)
			}
			fr.mu.Lock()
			defer fr.mu.Unlock()
			if len(fr.reqs) != 0 {
				t.Error("a refused request was sent to the router anyway")
			}
		})
	}
}
