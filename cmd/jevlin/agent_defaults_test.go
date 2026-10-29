package main

// What a `search --stdin` request gets when its caller sized nothing, the
// fast preset the user stored, and the bound that makes an agent search
// cheap to read.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// servingWith runs one --stdin search against a fake router that answers
// with answer(sent), after files are written beside the config.
func servingWith(t *testing.T, files map[string]string, stdin string, answer func(sent map[string]any) []byte) (out string, sent map[string]any) {
	t.Helper()
	var fr *fakeRouter
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		// The fake has already read the request body and kept it.
		fr.mu.Lock()
		raw := fr.body
		fr.mu.Unlock()
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write(answer(body))
	})
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := fixedSearchOps(root)
	code, stdout, errOut := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, errOut)
	}
	_, body := fr.last(t)
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("router body: %v", err)
	}
	return stdout, sent
}

func TestAnAgentSearchSizesWhatTheCallerDidNot(t *testing.T) {
	raw := readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json"))
	answer := func(map[string]any) []byte { return raw }
	for _, tc := range []struct {
		name, stdin     string
		page, limit, mc any
	}{
		{"nothing sized", `{"version":1,"query":"q"}`, float64(500), nil, nil},
		{"the caller's page cap", `{"version":1,"query":"q","page_chars":1200}`, float64(1200), nil, nil},
		{"rewordings widen the window", `{"version":1,"query":"q","search_queries":["a","b"]}`, float64(500), float64(20), nil},
		{"the caller's window", `{"version":1,"query":"q","search_queries":["a"],"merged_limit":16}`, float64(500), float64(16), nil},
		{"a whole-list budget is the caller's choice", `{"version":1,"query":"q","max_chars":3000}`, nil, nil, float64(3000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sent := servingWith(t, nil, tc.stdin, answer)
			for key, want := range map[string]any{"page_chars": tc.page, "merged_limit": tc.limit, "max_chars": tc.mc} {
				got, has := sent[key]
				if want == nil && has {
					t.Errorf("the router was sent %s %v, which nobody asked for", key, got)
				}
				if want != nil && got != want {
					t.Errorf("the router was sent %s %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestAPageCharsTheRouterWouldRefuseNeverLeaves(t *testing.T) {
	for _, stdin := range []string{
		`{"version":1,"query":"q","page_chars":199}`,
		`{"version":1,"query":"q","page_chars":4001}`,
		`{"version":1,"query":"q","page_chars":null}`,
		`{"version":1,"query":"q","page_chars":"500"}`,
		`{"version":1,"query":"q","page_chars":500.5}`,
	} {
		fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("the router was called") })
		_ = fr
		code, out, _ := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
		env := decodeEnvelope(t, out)
		if code != exitUsage || env["code"] != codeInvalidPageChars || env["action"] != actionFixInput {
			t.Errorf("%s: exit %d code %v action %v", stdin, code, env["code"], env["action"])
		}
	}
}

func TestTheFastPresetRidesOnlyWithTheFastTier(t *testing.T) {
	raw := readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json"))
	answer := func(map[string]any) []byte { return raw }
	for _, tc := range []struct {
		name  string
		files map[string]string
		stdin string
		want  any
	}{
		{"the stored tier is fast", map[string]string{"search-tier": "fast\n", "search-fast-set": "standard\n"}, `{"version":1,"query":"q"}`, "standard"},
		{"the caller asked fast", map[string]string{"search-fast-set": "economy\n"}, `{"version":1,"query":"q","tier":"fast"}`, "economy"},
		{"another tier carries none", map[string]string{"search-tier": "balanced\n", "search-fast-set": "standard\n"}, `{"version":1,"query":"q"}`, nil},
		{"no tier carries none", map[string]string{"search-fast-set": "standard\n"}, `{"version":1,"query":"q"}`, nil},
		{"fast with no preset stored", map[string]string{"search-tier": "fast\n"}, `{"version":1,"query":"q"}`, nil},
		{"a planted line is not a name", map[string]string{"search-tier": "fast\n", "search-fast-set": "x\" ,\"y\n"}, `{"version":1,"query":"q"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sent := servingWith(t, tc.files, tc.stdin, answer)
			got, has := sent["fast_set"]
			if tc.want == nil && has {
				t.Errorf("fast_set %v was sent", got)
			}
			if tc.want != nil && got != tc.want {
				t.Errorf("fast_set %v, want %v", got, tc.want)
			}
		})
	}
}

// An unknown preset is the router's 400 with its own code, and the caller
// has something to fix.
func TestAnUnknownFastPresetIsFixInput(t *testing.T) {
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown fast set; valid: economy, standard","code":"invalid_fast_set"}`))
	})
	_ = fr
	if err := os.WriteFile(filepath.Join(root, "search-fast-set"), []byte("gold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, `{"version":1,"query":"q","tier":"fast"}`, "-config", cfg)
	env := decodeEnvelope(t, out)
	if env["code"] != "invalid_fast_set" || env["action"] != actionFixInput || env["retryable"] != false {
		t.Errorf("code %v action %v retryable %v", env["code"], env["action"], env["retryable"])
	}
}

// routerHonoring answers the way the router's merged view does: merged_limit
// pages, each excerpt cut at page_chars. The page shape is the recorded
// fixture's, repeated, so every field a page carries is counted.
func routerHonoring(t *testing.T) func(map[string]any) []byte {
	raw := readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json"))
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	proto := doc["merged"].([]any)[0].(map[string]any)
	return func(sent map[string]any) []byte {
		limit := 12
		if n, ok := sent["merged_limit"].(float64); ok {
			limit = int(n)
		}
		cap := 4000
		if n, ok := sent["page_chars"].(float64); ok {
			cap = int(n)
		}
		var pages []any
		for i := 0; i < limit; i++ {
			p := map[string]any{}
			for k, v := range proto {
				p[k] = v
			}
			p["url"] = fmt.Sprintf("https://example.org/page/%d", i)
			p["title"] = fmt.Sprintf("Title of page %d", i)
			p["excerpt"] = strings.Repeat("x", cap)
			delete(p, "excerpts")
			p["source_id"] = fmt.Sprintf("s_%032x", i)
			pages = append(pages, p)
		}
		doc["merged"] = pages
		doc["merged_limit"] = limit
		doc["merged_total"] = limit
		out, _ := json.Marshal(doc)
		return out
	}
}

// The target: no agent search above about 20,000 characters, for the
// largest request an agent is taught to send, a query with three wordings.
func TestAgentSearchOutputStaysBounded(t *testing.T) {
	const bound = 20000
	stdin := `{"version":1,"query":"q","objective":"o","search_queries":["second wording","third wording"]}`
	out, sent := servingWith(t, nil, stdin, routerHonoring(t))
	if sent["merged_limit"] != float64(defaultRewordedLimit) || sent["page_chars"] != float64(defaultPageChars) {
		t.Fatalf("defaults not sent: %v", sent)
	}
	t.Logf("largest agent search output: %d characters (bound %d)", len(out), bound)
	if len(out) > bound {
		t.Errorf("the envelope is %d characters, over %d", len(out), bound)
	}
}

// A router that predates presets refuses a field it does not know, so the
// preset is sent only when the router lists them.
func TestNoFastSetGoesToARouterThatDoesNotListPresets(t *testing.T) {
	raw := readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json"))
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/fast-sets" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	})
	if err := os.WriteFile(filepath.Join(root, "search-fast-set"), []byte("standard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, `{"version":1,"query":"q","tier":"fast"}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	_, body := fr.last(t)
	if strings.Contains(string(body), "fast_set") {
		t.Errorf("the router was sent %s", body)
	}
}
