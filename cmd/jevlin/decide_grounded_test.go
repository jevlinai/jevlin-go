package main

// The grounded models' two fields, consulted_pages and grounding, come
// through decide's envelope field for field, through the CLI and through the
// MCP tool's text and structuredContent alike.
//
// testdata/decide/router/decisions-grounded.json follows the shape of the
// response example in search-router's API.md, section "The grounded models"
// (3628810), field for field and in its order; the router publishes no
// fixture for these models yet. The shape follows the reference, not the
// bytes: the addresses are fictional (example.test), as every fixture here.

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
	const want = `"citations":[],"consulted_pages":[{"id":"s_4be1…","url":"https://payments.example.test/billing","title":"Billing"},` +
		`{"id":"s_77a0…","url":"https://payments.example.test/tax","title":"Tax","questions":["tax"]}],` +
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

// options.search_fields needs no flag of its own: options goes to the router
// as the caller wrote it, absent, a list of fields or an empty list.
func TestSearchFieldsGoOutInTheCallersOptions(t *testing.T) {
	for _, options := range []string{``, `,"options":{"search_fields": ["company"]}`, `,"options":{"search_fields":[]}`} {
		stdin := `{"version":1,"model":"jevlin/grounded-fast","state":{"company":"x"},"questions":{"q":{"type":"noul"}}` + options + `}`
		want := `{"model":"jevlin/grounded-fast","state":{"company":"x"},"questions":{"q":{"type":"noul"}}` + options + `}`
		_, code, fr := decideServing(t, http.StatusOK, decideGroundedFast, stdin)
		if code != exitOK {
			t.Fatalf("exit %d for %s", code, stdin)
		}
		if _, body := fr.last(t); string(body) != want {
			t.Errorf("the router received\n%s\nwant\n%s", body, want)
		}
	}
}

// The machine side's grounded and band types, with each field's type: a
// pointer is what keeps a 0 the router sent, and url has no omitempty, so
// an empty address is carried as sent. decideModelBand is shared by both
// sides.
func TestTheMachineGroundedFieldsAreDecided(t *testing.T) {
	for _, tc := range []struct {
		rt      reflect.Type
		decided []string
	}{
		{reflect.TypeFor[machineDecidePage](), []string{
			"ID string id,omitempty", "URL string url", "Title string title,omitempty", "Questions []string questions,omitempty"}},
		{reflect.TypeFor[machineDecideGrounding](), []string{
			"Rules string rules,omitempty", "Searches *int64 searches,omitempty", "SecondStage string second_stage,omitempty",
			"Degraded *bool degraded,omitempty", "Reconsidered []string reconsidered,omitempty"}},
		{reflect.TypeFor[decideModelBand](), []string{"UpTo *int64 up_to,omitempty", "Micros *int64 micros,omitempty"}},
	} {
		var got []string
		for i := range tc.rt.NumField() {
			f := tc.rt.Field(i)
			got = append(got, f.Name+" "+f.Type.String()+" "+f.Tag.Get("json"))
		}
		if !reflect.DeepEqual(got, tc.decided) {
			t.Errorf("%s's fields are %q; this test lists %q", tc.rt.Name(), got, tc.decided)
		}
	}
}

// A zero the router sends comes back: a band priced 0 (a free band), a band
// up to 0, and a page with an empty address.
func TestZerosTheRouterSendsAreCarried(t *testing.T) {
	const models = `{"models":[{"id":"a/b","vendor":"a","vendor_model":"b","cost":{"bands":[{"up_to":0,"micros":0},{"up_to":5,"micros":0}],"second_stage_failed_micros":0}}]}`
	env, code, _ := decideServingHeaders(t, http.StatusOK, models, "", nil, "--models")
	if got, want := resultOf(t, env)["models"], routerFieldsOf(t, models)["models"]; code != exitOK || !reflect.DeepEqual(got, want) {
		t.Errorf("exit %d, models %v\nthe router's %v", code, got, want)
	}
	const decision = `{"answers":{},"consulted_pages":[{"url":""},{"id":"s_1","url":"","title":""}]}`
	env, code, _ = decideServing(t, http.StatusOK, decision, decideMinimal)
	if got := resultOf(t, env)["consulted_pages"]; code != exitOK ||
		!reflect.DeepEqual(got, []any{map[string]any{"url": ""}, map[string]any{"id": "s_1", "url": ""}}) {
		t.Errorf("exit %d, consulted_pages %v", code, got)
	}
}

// A grounded field of the wrong type fails the decision as invalid_router_response,
// exit 4, exactly as a wrong-typed usage field always has: the client does not
// hand on half an answer it could not read.
func TestAWrongTypedGroundedFieldFailsTheDecisionAsUsageDoes(t *testing.T) {
	for _, body := range []string{
		`{"answers":{},"usage":{"cost_micros":"12"}}`,
		`{"answers":{},"grounding":"q1"}`,
		`{"answers":{},"grounding":{"searches":"2"}}`,
		`{"answers":{},"grounding":{"degraded":"no"}}`,
		`{"answers":{},"consulted_pages":{"url":"https://docs.example.test/"}}`,
	} {
		env, code, _ := decideServing(t, http.StatusOK, body, decideMinimal)
		if code != exitServerErr || env["code"] != string(faultInvalidResponse) || env["action"] != actionReport {
			t.Errorf("%s: exit %d, %v / %v; want %d, %s / report", body, code, env["code"], env["action"], exitServerErr, faultInvalidResponse)
		}
		if _, has := env["result"]; has {
			t.Errorf("%s: a decision it could not read carries a result", body)
		}
	}
}
