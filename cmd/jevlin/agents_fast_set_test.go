package main

// `jevlin agents tier fast <preset>` stores the user's fast preset beside the
// tier preference; `tier status` lists what the router offers.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTierFastTakesAPresetAndDefaultClearsIt(t *testing.T) {
	m, ops := newFakeMachine("claude")
	presetFile := func() (string, bool) {
		for p, b := range m.files {
			if strings.HasSuffix(p, "/"+fastSetFile) {
				return strings.TrimSpace(string(b)), true
			}
		}
		return "", false
	}
	code, out, errOut := runAgents(t, ops, nil, "tier", "-config", testCfg, "fast", "economy")
	if code != exitOK || !strings.Contains(out, "search tier: fast") || !strings.Contains(out, "fast preset: economy") {
		t.Fatalf("tier fast economy: %d\n%s%s", code, out, errOut)
	}
	if got, ok := presetFile(); !ok || got != "economy" {
		t.Fatalf("preset file %q ok=%v", got, ok)
	}
	// The words may come in either order around the flags, and case is forgiven.
	if code, _, _ := runAgents(t, ops, nil, "tier", "-config", testCfg, "Fast", "Standard"); code != exitOK {
		t.Fatalf("tier Fast Standard: %d", code)
	}
	if got, _ := presetFile(); got != "standard" {
		t.Errorf("preset %q, want standard", got)
	}
	// A tier that is not fast takes no preset, and a name that is not one is refused.
	for _, args := range [][]string{{"balanced", "economy"}, {"fast", "a b"}, {"fast", "Eco;nomy!"}, {"fast", "x", "y"}} {
		if code, _, _ := runAgents(t, ops, nil, append([]string{"tier", "-config", testCfg}, args...)...); code != exitUsage {
			t.Errorf("tier %v: exit %d, want usage", args, code)
		}
	}
	if code, _, _ := runAgents(t, ops, nil, "tier", "default", "-config", testCfg); code != exitOK {
		t.Fatal("tier default")
	}
	if _, ok := presetFile(); ok {
		t.Error("default left the preset behind")
	}
}

func TestTierStatusListsThePresetsWithEstimatedPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fast-sets" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"default":"economy","sets":[{"name":"economy","providers":["serper","parallel-fast"],"estimated_cost_micros":1300},{"name":"standard","providers":["exa-lite","serper","parallel-fast"],"estimated_cost_micros":5300}]}`))
	}))
	t.Cleanup(srv.Close)
	cfg := writeTOML(t, "schema = 1\n\n[router]\nurl = \""+srv.URL+"\"\n")
	_, ops := newFakeMachine("claude")
	env := map[string]string{"JEVLIN_API_KEY": "k", "JEVLIN_CONFIG": cfg}
	code, out, errOut := runAgents(t, ops, env, "tier", "-config", cfg, "status")
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"economy", "standard", "serper, parallel-fast", "estimated $0.0013 per search", "estimated $0.0053 per search", "(the router's default)"} {
		if !strings.Contains(out, want) {
			t.Errorf("tier status is missing %q:\n%s", want, out)
		}
	}
}

// With no endpoint to ask, the stored name still shows and prices are
// said to be unavailable.
func TestTierStatusWhenThePresetListFailsSaysPricesAreUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }))
	t.Cleanup(srv.Close)
	cfg := writeTOML(t, "schema = 1\n\n[router]\nurl = \""+srv.URL+"\"\n")
	_, ops := newFakeMachine("claude")
	env := map[string]string{"JEVLIN_API_KEY": "k", "JEVLIN_CONFIG": cfg}
	if code, out, errOut := runAgents(t, ops, env, "tier", "-config", cfg, "fast", "economy"); code != exitOK {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	code, out, _ := runAgents(t, ops, env, "tier", "-config", cfg, "status")
	if code != exitOK || !strings.Contains(out, "fast preset: economy") || !strings.Contains(out, "prices are unavailable") {
		t.Errorf("%d\n%s", code, out)
	}
}

func TestTierStatusOnARouterWithoutPresetsSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	cfg := writeTOML(t, "schema = 1\n\n[router]\nurl = \""+srv.URL+"\"\n")
	_, ops := newFakeMachine("claude")
	env := map[string]string{"JEVLIN_API_KEY": "k", "JEVLIN_CONFIG": cfg}
	code, out, _ := runAgents(t, ops, env, "tier", "-config", cfg, "status")
	if code != exitOK || !strings.Contains(out, "not available on this router") {
		t.Errorf("%d\n%s", code, out)
	}
}
