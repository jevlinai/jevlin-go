package main

// What `decide` prints for every answer it could already read is held byte
// for byte to testdata/decide/golden, captured from the client before the
// grounded models' fields were carried. A model that sends neither
// consulted_pages nor grounding must print exactly what it printed then, and
// a comparison through a decoder could not tell a reordered or re-escaped
// envelope from the original, so the goldens are compared as bytes.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// decideSourcesAnswer is a decision over pages the caller searched, with
// every field a source and the usage can carry. Fictional values.
const decideSourcesAnswer = `{"request_id":"01936f2a-7c00-7000-8000-0000000000a3","model":"typesafe/jev-1.14",
 "answers":{"best":{"type":"choice","choice":"a","probabilities":{"a":0.8,"b":0.2}}},
 "citations":[],
 "sources":[
  {"source_id":"s_0123456789abcdef0123456789abcdef","url":"https://docs.example.test/a","title":"Page <a> & more","chars":3987,"cached":false,"cost_micros":2000},
  {"source_id":"s_fedcba9876543210fedcba9876543210","url":"https://docs.example.test/b","title":"Page b","chars":2410,"cached":true,"cost_micros":0}],
 "usage":{"input_tokens":1820,"output_tokens":12,"grounded_calls":0,"cost_micros":2076,"pages_cost_micros":2000,"cost_source":"modeled","byok":false,"latency_ms":910}}`

// decideGoldenCase is one router answer and the golden the envelope must
// match. An empty mode is --stdin with decideMinimal.
type decideGoldenCase struct {
	name    string
	mode    string
	status  int
	body    string
	headers map[string]string
}

func decideGoldenCases(t *testing.T) []decideGoldenCase {
	t.Helper()
	return []decideGoldenCase{
		{name: "success", status: http.StatusOK, body: decideFixture(t, "decisions-success.json")},
		{name: "refusal", status: http.StatusOK, body: decideFixture(t, "decisions-refusal.json")},
		{name: "sources", status: http.StatusOK, body: decideSourcesAnswer},
		{name: "byte-for-byte", status: http.StatusOK,
			body: `{"request_id":"r","model":"a/b","answers":{"later":{"type":"refusal","reason_new":"a <field> this client has never heard of"},"earlier":{"type":"score","score":2.0,"probabilities":{"2":0.8,"0":0.05,"1":0.15},"legend":{"0":"a & b","1":"<i>","2":"é"},"confidence":1e-1}},"citations":[],"usage":{}}`},
		{name: "request-id-from-header", status: http.StatusOK, body: `{"answers":{}}`,
			headers: map[string]string{"X-Request-Id": "header-id"}},
		{name: "models", mode: "--models", status: http.StatusOK, body: decideModelsAnswer},
		{name: "models-empty", mode: "--models", status: http.StatusOK, body: `{"models":[]}`},
		{name: "models-no-cost", mode: "--models", status: http.StatusOK, body: `{"models":[{"id":"a/b","vendor":"a","vendor_model":"b"}]}`},
		{name: "upstream-failed-retryable", status: http.StatusServiceUnavailable,
			body:    `{"code":"upstream_failed","error":"vendor timeout","retryable":true}`,
			headers: map[string]string{"Retry-After": "7"}},
		{name: "invalid-request", status: http.StatusBadRequest, body: `{"code":"invalid_request","error":"question q needs a type"}`},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, body: `{"code":"some_422","error":"no"}`},
		{name: "conflict", status: http.StatusConflict, body: `{"code":"some_409","error":"no"}`},
	}
}

// decideGoldenOutput runs one case and returns stdout exactly.
func decideGoldenOutput(t *testing.T, tc decideGoldenCase) string {
	t.Helper()
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range tc.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(tc.status)
		_, _ = w.Write([]byte(tc.body))
	})
	mode, stdin := tc.mode, decideMinimal
	if mode == "" {
		mode = "--stdin"
	}
	if mode == "--models" {
		stdin = ""
	}
	_, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, stdin, mode, "-config", cfg)
	if errOut != "" {
		t.Errorf("decide wrote on stderr: %q", errOut)
	}
	return out
}

func decideGoldenPath(name string) string {
	return filepath.Join("testdata", "decide", "golden", name+".envelope")
}

// An answer without the grounded models' fields prints what it printed
// before they were carried, byte for byte, on every path the goldens cover.
func TestADecideWithoutGroundingPrintsItsGoldenByteForByte(t *testing.T) {
	for _, tc := range decideGoldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(decideGoldenPath(tc.name)) // #nosec G304 -- the test's own golden directory, with names the test writes
			if err != nil {
				t.Fatalf("no golden for %s: %v", tc.name, err)
			}
			if got := decideGoldenOutput(t, tc); got != string(want) {
				t.Errorf("decide printed\n%s\nthe golden %s holds\n%s", got, decideGoldenPath(tc.name), want)
			}
		})
	}
}
