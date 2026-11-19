package main

// The grounded models' two fields, consulted_pages and grounding, come
// through decide's envelope field for field, through the CLI and through the
// MCP tool's text and structuredContent alike.
//
// testdata/decide/router/decisions-grounded.json is the response example in
// search-router's API.md, section "The grounded models", at 3628810, byte for
// byte: the router publishes no fixture for these models yet.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// decideGroundedFast is a fast model's answer: grounding has rules and
// searches only (contract.go's omitempty tags), and a searches of 0 is an
// answer that must not vanish.
const decideGroundedFast = `{"request_id":"r-fast","model":"jevlin/grounded-fast","model_resolved":"grounded-fast-1",
 "answers":{"q":{"type":"noul","noul":0.4}},"citations":[],
 "consulted_pages":[{"id":"s_1","url":"https://docs.example.test/a","title":"A"}],
 "grounding":{"rules":"q1","searches":0},
 "usage":{"cost_micros":1200,"cost_source":"modeled","byok":false}}`

// decideGroundedDegraded is an extended answer whose second stage failed.
const decideGroundedDegraded = `{"request_id":"r-ext","model":"jevlin/grounded-extended","answers":{"q":{"type":"noul","noul":0.6}},"citations":[],
 "consulted_pages":[{"url":"https://docs.example.test/b"}],
 "grounding":{"rules":"q1","searches":3,"second_stage":"failed","degraded":true,"reconsidered":["q"]}}`

func routerFieldsOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var router map[string]any
	if err := json.Unmarshal([]byte(raw), &router); err != nil {
		t.Fatal(err)
	}
	return router
}

// Each grounded answer's consulted_pages and grounding are the router's,
// field for field, with every false and zero it sent.
func TestTheGroundedFieldsAreTheRouters(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"extended, from the router's documentation", decideFixture(t, "decisions-grounded.json")},
		{"fast", decideGroundedFast},
		{"extended, degraded", decideGroundedDegraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, _ := decideServing(t, http.StatusOK, tc.raw, decideMinimal)
			if code != exitOK || env["ok"] != true {
				t.Fatalf("exit %d: %v", code, env)
			}
			router, result := routerFieldsOf(t, tc.raw), resultOf(t, env)
			for _, k := range []string{"consulted_pages", "grounding", "request_id", "model", "citations", "usage"} {
				if !reflect.DeepEqual(result[k], router[k]) {
					t.Errorf("result.%s %v, the router's %v", k, result[k], router[k])
				}
			}
		})
	}
}

// The envelope keeps the router's order: sources, then consulted_pages and
// grounding, then usage.
func TestTheGroundedFieldsSitWhereTheRouterPutsThem(t *testing.T) {
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(decideFixture(t, "decisions-grounded.json")))
	})
	_, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	const want = `"citations":[],"consulted_pages":[{"id":"s_4be1…","url":"https://stripe.com/billing","title":"Billing"},` +
		`{"id":"s_77a0…","url":"https://stripe.com/tax","title":"Tax","questions":["tax"]}],` +
		`"grounding":{"rules":"q1","searches":2,"second_stage":"ran","degraded":false,"reconsidered":["tax"]},"usage":{`
	if !strings.Contains(out, want) {
		t.Errorf("the envelope\n%s\ndoes not carry\n%s", out, want)
	}
}

// Through MCP the same fields reach the text item, and toolEnvelope holds
// structuredContent to that text's envelope.
func TestAnMCPHostSeesTheGroundedFields(t *testing.T) {
	raw := decideFixture(t, "decisions-grounded.json")
	_, env, isError, _ := runDecideTool(t, okHandler(raw), "decide", decideArgs)
	if isError || env["ok"] != true {
		t.Fatalf("isError=%v, envelope %v", isError, env)
	}
	router := routerFieldsOf(t, raw)
	for _, k := range []string{"consulted_pages", "grounding"} {
		if !reflect.DeepEqual(resultOf(t, env)[k], router[k]) {
			t.Errorf("the tool's result.%s %v, the router's %v", k, resultOf(t, env)[k], router[k])
		}
	}
}

// decideBandedModels lists the two grounded models as the hosted gateway
// prices them (API.md, "Keys and price"): fast one band, extended two and a
// price for a request whose second stage failed.
const decideBandedModels = `{"models":[
 {"id":"jevlin/grounded-fast","vendor":"jevlin","vendor_model":"grounded-fast","cost":{"bands":[{"up_to":10,"micros":1200}]},"grounded":true,"available":true},
 {"id":"jevlin/grounded-extended","vendor":"jevlin","vendor_model":"grounded-extended","cost":{"bands":[{"up_to":5,"micros":2500},{"up_to":10,"micros":4000}],"second_stage_failed_micros":1200},"grounded":true,"available":false}]}`

// A banded model's price comes through --models field for field.
func TestDecideModelsCarriesThePriceBands(t *testing.T) {
	env, code, _ := decideServingHeaders(t, http.StatusOK, decideBandedModels, "", nil, "--models")
	if code != exitOK || env["ok"] != true {
		t.Fatalf("exit %d: %v", code, env)
	}
	router := routerFieldsOf(t, decideBandedModels)
	if got := resultOf(t, env)["models"]; !reflect.DeepEqual(got, router["models"]) {
		t.Errorf("models %v\nthe router's %v", got, router["models"])
	}
}
