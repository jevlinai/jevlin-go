package main

// `decide --stdin` and `decide --models` against a fake router: a bad request
// never reaches it, a good one arrives byte for byte, and every answer the
// router can give maps to the header an agent branches on.
//
// testdata/decide/router holds the router's own fixtures,
// docs/router-contracts/fixtures/decisions-success.json and
// decisions-refusal.json from Jevlin-core main at 8936916, byte for byte.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// decideStateMarker sits in every request's state, so a test can say the
// caller's content appeared nowhere it should not.
const decideStateMarker = "state-marker-fictional"

const decideMinimal = `{"version":1,"model":"typesafe/jev-1.14","state":"` + decideStateMarker + `","questions":{"q":{"type":"noul"}}}`

func runDecide(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	now := func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }
	code := decideMain(now, args, strings.NewReader(stdin), &out, &errOut, installedAs(envOf(env), args))
	return code, out.String(), errOut.String()
}

// decideServing runs one decide against a fake router answering status and
// raw, and returns the envelope, the exit code and the router.
func decideServing(t *testing.T, status int, raw, stdin string) (map[string]any, int, *fakeRouter) {
	t.Helper()
	return decideServingHeaders(t, status, raw, stdin, nil, "--stdin")
}

func decideServingHeaders(t *testing.T, status int, raw, stdin string, headers map[string]string, mode string) (map[string]any, int, *fakeRouter) {
	t.Helper()
	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(raw))
	})
	code, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, stdin, mode, "-config", cfg)
	if errOut != "" {
		t.Errorf("decide wrote on stderr: %q", errOut)
	}
	return decodeEnvelope(t, out), code, fr
}

func decideFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "decide", "router", name)) // #nosec G304 -- the test's own fixture directory, with names the tests write
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// rawAnswers reads result.answers from an envelope's bytes without decoding
// any answer, so a comparison sees exactly what was emitted.
func rawAnswers(t *testing.T, stdout string) map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Result struct {
			Answers map[string]json.RawMessage `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v in %s", err, stdout)
	}
	return env.Result.Answers
}

// compactAnswers is the router's answers with only the whitespace between
// tokens taken out, which the envelope must drop to stay one line.
func compactAnswers(t *testing.T, routerBody string) map[string]string {
	t.Helper()
	var body struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal([]byte(routerBody), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range body.Answers {
		var buf bytes.Buffer
		if err := json.Compact(&buf, v); err != nil {
			t.Fatal(err)
		}
		out[k] = buf.String()
	}
	return out
}

// Every malformed value answers fix_input, names its field, and never
// reaches the router.
func TestADecideRequestOutsideTheContractNeverReachesTheRouter(t *testing.T) {
	const m, s, q = `"model":"typesafe/jev-1.14",`, `"state":"x",`, `"questions":{"q":{"type":"noul"}}`
	source := `{"source_id":"` + fetchSourceID + `"}`
	six := `[` + strings.TrimSuffix(strings.Repeat(source+",", 6), ",") + `]`
	for _, tc := range []struct {
		name, stdin, code, field string
	}{
		{"no version", `{` + m + s + q + `}`, codeUnsupportedVersion, `"version"`},
		{"version 2", `{"version":2,` + m + s + q + `}`, codeUnsupportedVersion, `decide request version 1`},
		{"not JSON", `decide please`, codeInvalidJSON, `JSON object`},
		{"unknown field", `{"version":1,` + m + s + q + `,"tier":"fast"}`, codeUnknownField, `field`},

		{"no model", `{"version":1,` + s + q + `}`, codeMissingModel, `"model"`},
		{"model null", `{"version":1,"model":null,` + s + q + `}`, codeMissingModel, `"model"`},
		{"model not a string", `{"version":1,"model":7,` + s + q + `}`, codeInvalidModel, `"model"`},
		{"model blank", `{"version":1,"model":"  ",` + s + q + `}`, codeInvalidModel, `"model"`},
		{"model no slash", `{"version":1,"model":"jev-1.14",` + s + q + `}`, codeInvalidModel, `"model"`},
		{"model upper-case vendor", `{"version":1,"model":"TypeSafe/jev",` + s + q + `}`, codeInvalidModel, `"model"`},
		{"model two slashes", `{"version":1,"model":"a/b/c",` + s + q + `}`, codeInvalidModel, `"model"`},
		{"model trailing newline", `{"version":1,"model":"a/b\n",` + s + q + `}`, codeInvalidModel, `"model"`},

		{"no state", `{"version":1,` + m + q + `}`, codeMissingState, `"state"`},
		{"state null", `{"version":1,` + m + `"state":null,` + q + `}`, codeMissingState, `"state"`},

		{"no questions", `{"version":1,` + m + `"state":"x"}`, codeMissingQuestions, `"questions"`},
		{"questions null", `{"version":1,` + m + s + `"questions":null}`, codeMissingQuestions, `"questions"`},
		{"questions a list", `{"version":1,` + m + s + `"questions":[{"type":"noul"}]}`, codeInvalidQuestions, `"questions"`},
		{"questions a string", `{"version":1,` + m + s + `"questions":"q"}`, codeInvalidQuestions, `"questions"`},
		{"questions empty", `{"version":1,` + m + s + `"questions":{}}`, codeInvalidQuestions, `"questions"`},
		{"a question not an object", `{"version":1,` + m + s + `"questions":{"good":{"type":"noul"},"bad-one":"noul"}}`, codeInvalidQuestions, `"bad-one"`},
		{"a question null", `{"version":1,` + m + s + `"questions":{"nil-one":null}}`, codeInvalidQuestions, `"nil-one"`},

		{"options a list", `{"version":1,` + m + s + q + `,"options":[]}`, codeInvalidOptions, `"options"`},
		{"options null", `{"version":1,` + m + s + q + `,"options":null}`, codeInvalidOptions, `"options"`},

		{"sources empty", `{"version":1,` + m + s + q + `,"sources":[]}`, codeInvalidSources, `"sources"`},
		{"sources null", `{"version":1,` + m + s + q + `,"sources":null}`, codeInvalidSources, `"sources"`},
		{"sources an object", `{"version":1,` + m + s + q + `,"sources":` + source + `}`, codeInvalidSources, `"sources"`},
		{"six sources", `{"version":1,` + m + s + q + `,"sources":` + six + `}`, codeInvalidSources, `"sources"`},
		{"source not an object", `{"version":1,` + m + s + q + `,"sources":["s_x"]}`, codeInvalidSources, `"sources" entry 0`},
		{"source with a url instead", `{"version":1,` + m + s + q + `,"sources":[{"url":"https://a.test/"}]}`, codeInvalidSources, `"sources" entry 0`},
		{"source with a second key", `{"version":1,` + m + s + q + `,"sources":[{"source_id":"` + fetchSourceID + `","rank":1}]}`, codeInvalidSources, `"sources" entry 0`},
		{"source key in another case", `{"version":1,` + m + s + q + `,"sources":[{"SOURCE_ID":"` + fetchSourceID + `"}]}`, codeInvalidSources, `"sources" entry 0`},
		{"bad source_id", `{"version":1,` + m + s + q + `,"sources":[{"source_id":"s_0123"}]}`, codeInvalidSources, `"source_id"`},
		{"source_id null", `{"version":1,` + m + s + q + `,"sources":[{"source_id":null}]}`, codeInvalidSources, `"source_id"`},
		{"second source bad", `{"version":1,` + m + s + q + `,"sources":[` + source + `,{"source_id":"r_1"}]}`, codeInvalidSources, `"sources" entry 1`},

		{"max_chars 999", `{"version":1,` + m + s + q + `,"max_chars":999}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars 50001", `{"version":1,` + m + s + q + `,"max_chars":50001}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars a string", `{"version":1,` + m + s + q + `,"max_chars":"4000"}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars fractional", `{"version":1,` + m + s + q + `,"max_chars":4000.5}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars null", `{"version":1,` + m + s + q + `,"max_chars":null}`, codeInvalidMaxChars, `"max_chars"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, fr := decideServing(t, http.StatusOK, decideFixture(t, "decisions-success.json"), tc.stdin)
			if code != exitUsage || env["code"] != tc.code || env["action"] != actionFixInput ||
				env["retryable"] != false || env["status"] != statusUsage {
				t.Errorf("exit %d, %v / %v / %v / retryable %v; want %d, %s / usage_error / fix_input / false",
					code, env["code"], env["status"], env["action"], env["retryable"], exitUsage, tc.code)
			}
			if msg, _ := objOf(env["error"])["message"].(string); !strings.Contains(msg, tc.field) {
				t.Errorf("error.message %q does not name %s", msg, tc.field)
			}
			if n := len(fr.reqs); n != 0 {
				t.Errorf("the router received %d requests for a refused decide", n)
			}
		})
	}
}

// The ranges' ends are inside them, and what is inside a state, a question
// or the options is the router's to judge.
func TestWhatIsInsideADecideRequestIsTheRouters(t *testing.T) {
	source := `{"source_id":"` + fetchSourceID + `"}`
	five := `[` + strings.TrimSuffix(strings.Repeat(source+",", 5), ",") + `]`
	for _, stdin := range []string{
		`{"version":1,"model":"a/b","state":"","questions":{"q":{}}}`,
		`{"version":1,"model":"a-1/B._-9","state":[],"questions":{"q":{"type":"not-a-type","extra":[1]}}}`,
		`{"version":1,"model":"a/b","state":{},"questions":{"q":{}},"options":{"anything":{"at":"all"}}}`,
		`{"version":1,"model":"a/b","state":0,"questions":{"q":{}}}`,
		`{"version":1,"model":"a/b","state":"x","questions":{"q":{}},"sources":` + five + `,"max_chars":1000}`,
		`{"version":1,"model":"a/b","state":"x","questions":{"q":{}},"sources":[` + source + `],"max_chars":50000}`,
	} {
		env, code, _ := decideServing(t, http.StatusOK, decideFixture(t, "decisions-success.json"), stdin)
		if code != exitOK {
			t.Errorf("exit %d for %s: %v", code, stdin, env)
		}
	}
}

// The state, the questions, the options, the sources and max_chars go out as
// the caller wrote them: the spacing, the <, the key order. The expected
// bytes are written out, because a comparison through a decoder could not
// tell a re-encoding from the original.
func TestTheDecideBodyIsTheCallersBytes(t *testing.T) {
	sent := `"model":"typesafe/jev-1.14",` +
		`"state":{"b": 1,   "a": "<x>"},` +
		`"questions":{"z":{"type":"noul","instructions":"Is it <b>?"},  "a":{"type":"score","instructions":"How much?","criteria":["no",  "yes"]}},` +
		`"options":{"mode":  "web_grounding"},` +
		`"sources":[{"source_id":  "` + fetchSourceID + `"}],` +
		`"max_chars":4000`
	stdin := `{"version":1,` + sent + `}`
	want := `{` + sent + `}`
	env, code, fr := decideServing(t, http.StatusOK, decideFixture(t, "decisions-success.json"), stdin)
	if code != exitOK {
		t.Fatalf("exit %d: %v", code, env)
	}
	req, body := fr.last(t)
	if string(body) != want {
		t.Errorf("the router received\n%s\nwant\n%s", body, want)
	}
	t.Logf("full body on the wire: %s", body)
	if req.Method != http.MethodPost || req.URL.Path != "/v1/decisions" {
		t.Errorf("%s %s, want POST /v1/decisions", req.Method, req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sr-fictional-key" {
		t.Errorf("Authorization %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != clientIdentifier() {
		t.Errorf("User-Agent %q, want search's %q", got, clientIdentifier())
	}
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests; a decision is one POST", len(fr.reqs))
	}

	// A minimal request sends exactly its three keys.
	_, _, fr = decideServing(t, http.StatusOK, decideFixture(t, "decisions-success.json"),
		`{"version":1,"model":"typesafe/jev-1.14","state":{"b": 1,   "a": "<x>"},"questions":{"q":{"type":"noul"}}}`)
	_, body = fr.last(t)
	const minimal = `{"model":"typesafe/jev-1.14","state":{"b": 1,   "a": "<x>"},"questions":{"q":{"type":"noul"}}}`
	if string(body) != minimal {
		t.Errorf("the router received\n%s\nwant\n%s", body, minimal)
	}
	t.Logf("minimal body on the wire: %s", body)
}

// The router's fixture comes back field for field; each answer is the
// router's own bytes with only the whitespace between tokens gone.
func TestTheDecideResultIsTheRoutersAnswer(t *testing.T) {
	raw := decideFixture(t, "decisions-success.json")
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) })
	code, stdout, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	if code != exitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q: %s", code, errOut, stdout)
	}
	if n := strings.Count(stdout, "\n"); n != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Errorf("the envelope is %d lines; it is one object and one newline", n)
	}
	got := rawAnswers(t, stdout)
	want := compactAnswers(t, raw)
	if len(got) != len(want) {
		t.Errorf("answers %v, want %v", got, want)
	}
	for q, w := range want {
		if string(got[q]) != w {
			t.Errorf("answer %q is %s\nwant %s", q, got[q], w)
		}
	}

	env := decodeEnvelope(t, stdout)
	if env["ok"] != true || env["command"] != "decide" || env["action"] != actionNone || env["code"] != "ok" {
		t.Errorf("header %v", env)
	}
	var router map[string]any
	if err := json.Unmarshal([]byte(raw), &router); err != nil {
		t.Fatal(err)
	}
	result := resultOf(t, env)
	for _, k := range []string{"request_id", "model", "model_resolved", "citations", "usage"} {
		if !reflect.DeepEqual(result[k], router[k]) {
			t.Errorf("result.%s %v, the router's %v", k, result[k], router[k])
		}
	}
	if env["request_id"] != router["request_id"] {
		t.Errorf("request_id %v, the router's %v", env["request_id"], router["request_id"])
	}
	if _, has := result["sources"]; has {
		t.Errorf("a router that sends no sources got result.sources %v", result["sources"])
	}
}

// A refusal is an outcome: exit 0, the other answers standing.
func TestARefusalIsCarriedAsAnAnswer(t *testing.T) {
	raw := decideFixture(t, "decisions-refusal.json")
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) })
	code, stdout, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	env := decodeEnvelope(t, stdout)
	if code != exitOK || env["ok"] != true {
		t.Fatalf("exit %d: %v", code, env)
	}
	got := rawAnswers(t, stdout)
	want := compactAnswers(t, raw)
	if string(got["current"]) != `{"type":"refusal"}` {
		t.Errorf("the refusal is %s", got["current"])
	}
	if string(got["relevance"]) != want["relevance"] {
		t.Errorf("the score is %s\nwant %s", got["relevance"], want["relevance"])
	}
}

// What a compact reply holds comes through exactly: a field this client has
// never heard of, a number written 2.0, a <, unsorted keys. Answers are
// carried, never decoded.
func TestAnAnswerIsCarriedByteForByte(t *testing.T) {
	const refusal = `{"type":"refusal","reason_new":"a <field> this client has never heard of"}`
	const score = `{"type":"score","score":2.0,"probabilities":{"2":0.8,"0":0.05,"1":0.15},"legend":{"0":"a & b","1":"<i>","2":"é"},"confidence":1e-1}`
	raw := `{"request_id":"r","model":"a/b","answers":{"later":` + refusal + `,"earlier":` + score + `},"citations":[],"usage":{}}`
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) })
	code, stdout, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	got := rawAnswers(t, stdout)
	if string(got["later"]) != refusal {
		t.Errorf("the refusal is %s\nwant %s", got["later"], refusal)
	}
	if string(got["earlier"]) != score {
		t.Errorf("the score is %s\nwant %s", got["earlier"], score)
	}
}

// The body's id wins; the header stands in when the body has none.
func TestADecideRequestIDComesFromTheBodyThenTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name, body, header, want string
	}{
		{"body and header", `{"request_id":"body-id","answers":{}}`, "header-id", "body-id"},
		{"body only", `{"request_id":"body-id","answers":{}}`, "", "body-id"},
		{"header only", `{"answers":{}}`, "header-id", "header-id"},
		{"blank body id", `{"request_id":"  ","answers":{}}`, "header-id", "header-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.header != "" {
				headers["X-Request-Id"] = tc.header
			}
			env, code, _ := decideServingHeaders(t, http.StatusOK, tc.body, decideMinimal, headers, "--stdin")
			if code != exitOK || env["request_id"] != tc.want || resultOf(t, env)["request_id"] != tc.want {
				t.Errorf("exit %d, request_id %v / %v, want %s", code, env["request_id"], resultOf(t, env)["request_id"], tc.want)
			}
		})
	}
}

// The router's decide types are copied into the envelope by conversion, so a
// field either side grows stops the build; this list makes the router's side
// a diff.
func TestTheRouterDecideFieldsAreDecided(t *testing.T) {
	for _, tc := range []struct {
		rt      reflect.Type
		decided []string
	}{
		{reflect.TypeFor[routerDecideResponse](), []string{
			"RequestID request_id", "Model model", "ModelResolved model_resolved,omitempty", "Answers answers",
			"Citations citations", "Sources sources,omitempty", "Usage usage,omitempty",
		}},
		{reflect.TypeFor[routerDecideCitation](), []string{"URL url", "Title title,omitempty", "Excerpts excerpts,omitempty"}},
		{reflect.TypeFor[routerDecideSource](), []string{
			"SourceID source_id,omitempty", "URL url,omitempty", "Title title,omitempty",
			"Chars chars,omitempty", "Cached cached,omitempty", "CostMicros cost_micros,omitempty",
		}},
		{reflect.TypeFor[routerDecideUsage](), []string{
			"InputTokens input_tokens,omitempty", "OutputTokens output_tokens,omitempty", "GroundedCalls grounded_calls,omitempty",
			"CostMicros cost_micros,omitempty", "PagesCostMicros pages_cost_micros,omitempty", "CostSource cost_source,omitempty",
			"BYOK byok,omitempty", "LatencyMS latency_ms,omitempty",
		}},
		{reflect.TypeFor[routerDecideModel](), []string{
			"ID id", "Vendor vendor", "VendorModel vendor_model", "Cost cost,omitempty", "Free free,omitempty",
			"Grounded grounded,omitempty", "Available available,omitempty",
		}},
		{reflect.TypeFor[decideModelCost](), []string{
			"PerRequestMicros per_request_micros,omitempty", "PerPromptKtokMicros per_prompt_ktok_micros,omitempty",
			"PerSearchCallMicros per_search_call_micros,omitempty",
		}},
		{reflect.TypeFor[decideModelFree](), []string{"RequestsPerDay requests_per_day,omitempty"}},
	} {
		var got []string
		for i := range tc.rt.NumField() {
			f := tc.rt.Field(i)
			got = append(got, f.Name+" "+f.Tag.Get("json"))
		}
		if !reflect.DeepEqual(got, tc.decided) {
			t.Errorf("%s's fields are %q; this test lists %q", tc.rt.Name(), got, tc.decided)
		}
	}
}

// Each router answer that is not a success maps to the header an agent acts
// on, by status and the router's exact code, never its message; and none is
// ever sent twice, because a decision is billed.
func TestEachDecideFailureMapsToItsAction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		exit      int
		code      string
		retryable bool
		action    string
		message   string
	}{
		{"unknown model", 400, `{"code":"unknown_model","error":"see the models route"}`, exitClientErr, "unknown_model", false, actionFixInput, "jevlin decide --models"},
		{"invalid request", 400, `{"code":"invalid_request","error":"question q needs a type"}`, exitClientErr, "invalid_request", false, actionFixInput, "question q needs a type"},
		{"malformed json", 400, `{"code":"malformed_json","error":"unknown field"}`, exitClientErr, "malformed_json", false, actionFixInput, "unknown field"},
		{"upstream rejected", 400, `{"code":"upstream_rejected","error":"the vendor says <mode> is wrong"}`, exitClientErr, "upstream_rejected", false, actionFixInput, "the vendor says <mode> is wrong"},
		{"payload too large", 413, `{"code":"payload_too_large","error":"over 2 MiB"}`, exitClientErr, "payload_too_large", false, actionFixInput, "over 2 MiB"},
		{"account cannot decide", 403, `{"code":"decisions_unavailable","error":"claim this agent"}`, exitClientErr, "decisions_unavailable", false, actionConnect, "claim this agent"},
		{"other 403", 403, `{"code":"scope","error":"no"}`, exitClientErr, "scope", false, actionCheckAccess, "no"},
		{"no endpoint, 404", 404, `404 page not found`, exitClientErr, "decisions_not_supported", false, actionReport, "no decisions"},
		{"no endpoint, 404 with a code", 404, `{"code":"not_found","error":"no route"}`, exitClientErr, "decisions_not_supported", false, actionReport, ""},
		{"no endpoint, 405", 405, ``, exitClientErr, "decisions_not_supported", false, actionReport, "no decisions"},
		{"switched off", 503, `{"code":"decisions_disabled","error":"not enabled"}`, exitServerErr, "decisions_disabled", false, actionReport, "not enabled"},
		{"switched off, no message", 503, `{"code":"decisions_disabled"}`, exitServerErr, "decisions_disabled", false, actionReport, "switched off"},
		{"model unavailable", 503, `{"code":"model_unavailable","error":"no working key"}`, exitServerErr, "model_unavailable", false, actionReport, "no working key"},
		{"upstream failed, retryable", 503, `{"code":"upstream_failed","error":"vendor timeout","retryable":true}`, exitServerErr, "upstream_failed", true, actionRetry, "vendor timeout"},
		{"upstream failed, not retryable", 503, `{"code":"upstream_failed","error":"vendor gone","retryable":false}`, exitServerErr, "upstream_failed", false, actionReport, "vendor gone"},
		{"upstream failed, says nothing", 503, `{"code":"upstream_failed","error":"vendor gone"}`, exitServerErr, "upstream_failed", false, actionReport, "vendor gone"},
		{"upstream failed, retryable not a bool", 503, `{"code":"upstream_failed","error":"x","retryable":"yes"}`, exitServerErr, "upstream_failed", false, actionReport, ""},
		{"another 503, the platform paused", 503, `{"code":"paused","error":"paused"}`, exitServerErr, "paused", true, actionRetry, "paused"},
		{"bare 503", 503, ``, exitServerErr, "router_error", true, actionRetry, ""},
		{"bare 502", 502, ``, exitServerErr, "router_error", true, actionRetry, ""},
		{"key refused", 401, `{"code":"unauthorized","error":"bad key"}`, exitClientErr, "unauthorized", false, actionLogin, "bad key"},
		{"credits exhausted", 402, `{"code":"credits_exhausted","error":"out"}`, exitClientErr, "credits_exhausted", false, actionReport, "out"},
		{"spend ceiling", 402, `{"code":"spend_ceiling","error":"daily"}`, exitClientErr, "spend_ceiling", false, actionReport, "daily"},
		{"rate limited", 429, `{"code":"rate_limited","error":"slow down"}`, exitClientErr, "rate_limited", true, actionRetry, "slow down"},
		{"2xx without answers", 200, `{"request_id":"r"}`, exitServerErr, string(faultInvalidResponse), false, actionReport, "no answers"},
		{"2xx with null answers", 200, `{"request_id":"r","answers":null}`, exitServerErr, string(faultInvalidResponse), false, actionReport, "no answers"},
		{"2xx answers a list", 200, `{"request_id":"r","answers":[]}`, exitServerErr, string(faultInvalidResponse), false, actionReport, ""},
		{"2xx not JSON", 200, `<html>`, exitServerErr, string(faultInvalidResponse), false, actionReport, ""},
		{"2xx with a second value", 200, `{"answers":{}}{"answers":{}}`, exitServerErr, string(faultInvalidResponse), false, actionReport, ""},
		{"2xx with bytes that are not UTF-8", 200, "{\"answers\":{\"q\":{\"type\":\"x\xff\"}}}", exitServerErr, string(faultInvalidResponse), false, actionReport, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, fr := decideServing(t, tc.status, tc.body, decideMinimal)
			if code != tc.exit || env["exit_code"] != float64(tc.exit) || env["code"] != tc.code ||
				env["retryable"] != tc.retryable || env["action"] != tc.action || env["ok"] != false || env["command"] != "decide" {
				t.Errorf("exit %d, %v / retryable %v / %v; want %d, %s / %v / %s",
					code, env["code"], env["retryable"], env["action"], tc.exit, tc.code, tc.retryable, tc.action)
			}
			if msg, _ := objOf(env["error"])["message"].(string); !strings.Contains(msg, tc.message) {
				t.Errorf("error.message %q does not carry %q", msg, tc.message)
			}
			if _, has := env["result"]; has {
				t.Errorf("a failed decide carries a result: %v", env["result"])
			}
			if len(fr.reqs) != 1 {
				t.Errorf("%d requests; a decision is never retried", len(fr.reqs))
			}
		})
	}
}

// retry_after_ms is present only where the answer says to retry, or is a 429.
func TestDecideRetryAfter(t *testing.T) {
	retry := map[string]string{"Retry-After": "7"}
	for _, tc := range []struct {
		name, body string
		status     int
		present    bool
	}{
		{"upstream failed, retryable", `{"code":"upstream_failed","error":"x","retryable":true}`, 503, true},
		{"upstream failed, not retryable", `{"code":"upstream_failed","error":"x","retryable":false}`, 503, false},
		{"rate limited", `{"code":"rate_limited","error":"x"}`, 429, true},
		{"switched off", `{"code":"decisions_disabled","error":"x"}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _ := decideServingHeaders(t, tc.status, tc.body, decideMinimal, retry, "--stdin")
			got, has := env["retry_after_ms"]
			if has != tc.present || (has && got != float64(7000)) {
				t.Errorf("retry_after_ms %v (present %v), want present %v", got, has, tc.present)
			}
		})
	}
}

// The transport's own failures are search's: a dropped connection and an
// expired deadline may be retried, and neither sends a second request.
func TestDecideTransportFailures(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, nil)
	fr.srv.Close()
	code, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	env := decodeEnvelope(t, out)
	if code != exitTransport || env["code"] != "transport_failed" || env["action"] != actionRetry || errOut != "" {
		t.Errorf("exit %d, %v, %v, stderr %q", code, env["code"], env["action"], errOut)
	}

	fr, cfg, _ = newFakeRouter(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	code, out, errOut = runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg, "-timeout", "50ms")
	env = decodeEnvelope(t, out)
	if code != exitTransport || env["code"] != string(faultTimeout) || env["retryable"] != true || errOut != "" {
		t.Errorf("exit %d, %v, retryable %v, stderr %q", code, env["code"], env["retryable"], errOut)
	}
	// Under the fake's lock: nothing came back from this handler, so no
	// response orders its write before this read. At most one, because a
	// slow machine can time out before the request arrives.
	fr.mu.Lock()
	sent := len(fr.reqs)
	fr.mu.Unlock()
	if sent > 1 {
		t.Errorf("%d requests; a timed-out decision is not sent again", sent)
	}
}

const decideModelsAnswer = `{"models":[
 {"id":"parallel/decisions-fast","vendor":"parallel","vendor_model":"decisions-fast","cost":{"per_request_micros":1000},"grounded":true,"available":false},
 {"id":"typesafe/jev-1.14","vendor":"typesafe","vendor_model":"jev-1.14","cost":{"per_prompt_ktok_micros":40,"per_search_call_micros":5000},"free":{"requests_per_day":50},"grounded":false,"available":true}]}`

// --models is one GET with the same key, and the list comes back field for
// field, including a false that absence would not say.
func TestDecideModelsIsTheRoutersList(t *testing.T) {
	env, code, fr := decideServingHeaders(t, http.StatusOK, decideModelsAnswer, "", nil, "--models")
	if code != exitOK || env["ok"] != true || env["command"] != "decide" || env["action"] != actionNone {
		t.Fatalf("exit %d: %v", code, env)
	}
	var router map[string]any
	if err := json.Unmarshal([]byte(decideModelsAnswer), &router); err != nil {
		t.Fatal(err)
	}
	if got := resultOf(t, env)["models"]; !reflect.DeepEqual(got, router["models"]) {
		t.Errorf("models %v\nthe router's %v", got, router["models"])
	}
	req, body := fr.last(t)
	if req.Method != http.MethodGet || req.URL.Path != "/v1/decisions/models" {
		t.Errorf("%s %s, want GET /v1/decisions/models", req.Method, req.URL.Path)
	}
	if len(body) != 0 {
		t.Errorf("a GET carried a body: %s", body)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sr-fictional-key" {
		t.Errorf("Authorization %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != clientIdentifier() {
		t.Errorf("User-Agent %q, want %q", got, clientIdentifier())
	}
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests", len(fr.reqs))
	}
}

type decideUnreadable struct{ read *atomic.Int32 }

func (u decideUnreadable) Read([]byte) (int, error) {
	u.read.Add(1)
	return 0, errors.New("stdin must not be read")
}

// --models reads nothing from stdin, whatever is waiting there.
func TestDecideModelsIgnoresStdin(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(decideModelsAnswer)) })
	var read atomic.Int32
	var out, errOut strings.Builder
	args := []string{"--models", "-config", cfg}
	code := decideMain(time.Now, args, decideUnreadable{&read}, &out, &errOut,
		installedAs(envOf(map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}), args))
	if code != exitOK || read.Load() != 0 || errOut.Len() != 0 {
		t.Errorf("exit %d, %d reads of stdin, stderr %q: %s", code, read.Load(), errOut.String(), out.String())
	}

	// And content on stdin changes nothing.
	code, stdout, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, "not a request", "--models", "-config", cfg)
	if code != exitOK || len(fr.reqs) != 2 {
		t.Errorf("exit %d with %d requests: %s", code, len(fr.reqs), stdout)
	}
}

func TestDecideModelsFailuresMapToTheirActions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		exit      int
		code      string
		retryable bool
		action    string
	}{
		{"no endpoint, 404", 404, `404 page not found`, exitClientErr, "decisions_not_supported", false, actionReport},
		{"no endpoint, 405", 405, ``, exitClientErr, "decisions_not_supported", false, actionReport},
		{"switched off", 503, `{"code":"decisions_disabled","error":"off"}`, exitServerErr, "decisions_disabled", false, actionReport},
		{"overloaded", 503, `{"code":"overloaded","error":"busy"}`, exitServerErr, "overloaded", true, actionRetry},
		{"key refused", 401, `{"code":"unauthorized","error":"bad key"}`, exitClientErr, "unauthorized", false, actionLogin},
		{"scope", 403, `{"code":"scope","error":"no"}`, exitClientErr, "scope", false, actionCheckAccess},
		{"not expected, classified as search", 402, `{"code":"credits_exhausted","error":"out"}`, exitClientErr, "credits_exhausted", false, actionReport},
		{"rate limited", 429, `{"code":"rate_limited","error":"slow"}`, exitClientErr, "rate_limited", true, actionRetry},
		{"2xx without models", 200, `{}`, exitServerErr, string(faultInvalidResponse), false, actionReport},
		{"2xx with null models", 200, `{"models":null}`, exitServerErr, string(faultInvalidResponse), false, actionReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, _ := decideServingHeaders(t, tc.status, tc.body, "", nil, "--models")
			if code != tc.exit || env["code"] != tc.code || env["retryable"] != tc.retryable || env["action"] != tc.action {
				t.Errorf("exit %d, %v / retryable %v / %v; want %d, %s / %v / %s",
					code, env["code"], env["retryable"], env["action"], tc.exit, tc.code, tc.retryable, tc.action)
			}
		})
	}
}

// An empty list is a valid answer, and comes back as [], not null.
func TestAnEmptyDecideModelsListIsAnAnswer(t *testing.T) {
	env, code, _ := decideServingHeaders(t, http.StatusOK, `{"models":[]}`, "", nil, "--models")
	if models, ok := resultOf(t, env)["models"].([]any); code != exitOK || !ok || len(models) != 0 {
		t.Errorf("exit %d, models %v", code, resultOf(t, env)["models"])
	}
}

func TestDecideStdinAndModelsTogetherIsAUsageError(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, nil)
	code, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "--models", "-config", cfg)
	env := decodeEnvelope(t, out)
	if code != exitUsage || env["code"] != codeInvalidFlags || env["action"] != actionFixInput || len(fr.reqs) != 0 || errOut != "" {
		t.Errorf("exit %d, %v, %v, %d requests, stderr %q", code, env["code"], env["action"], len(fr.reqs), errOut)
	}
}

// With neither mode there is no decide: a usage error for the person who
// typed it, nothing on stdout, as for fetch without --stdin.
func TestDecideNeedsAMode(t *testing.T) {
	var out, errOut strings.Builder
	code := decideMain(time.Now, []string{"-config", filepath.Join(t.TempDir(), "none.toml")}, strings.NewReader(""), &out, &errOut, envOf(nil))
	if code != exitUsage || out.Len() != 0 || !strings.Contains(errOut.String(), "--models") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestDecideHelpNamesBothForms(t *testing.T) {
	var out, errOut strings.Builder
	code := decideMain(time.Now, []string{"-h"}, strings.NewReader(""), &out, &errOut, envOf(nil))
	if code != exitOK || !strings.Contains(out.String(), "jevlin decide --stdin") ||
		!strings.Contains(out.String(), "jevlin decide --models") {
		t.Errorf("exit %d: %q", code, out.String())
	}
}

// The state the caller sent appears on neither stream, on any path a request
// can take after it is read.
func TestTheStateNeverAppearsInAnyOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"success", 200, decideFixture(t, "decisions-success.json")},
		{"refused by the router", 400, `{"code":"invalid_request","error":"no"}`},
		{"upstream failed", 503, `{"code":"upstream_failed","error":"x","retryable":true}`},
		{"not an answer", 200, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
			if errOut != "" {
				t.Errorf("stderr %q", errOut)
			}
			if strings.Contains(out, decideStateMarker) {
				t.Errorf("the envelope repeats the state: %s", out)
			}
		})
	}
	// Refused on the way in.
	_, cfg, _ := newFakeRouter(t, nil)
	stdin := `{"version":1,"model":"nope","state":"` + decideStateMarker + `","questions":{"q":{}}}`
	_, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, stdin, "--stdin", "-config", cfg)
	if errOut != "" || strings.Contains(out, decideStateMarker) {
		t.Errorf("stderr %q, stdout %s", errOut, out)
	}
}

// With no key in the environment the decide uses the one stored in
// credentials.json; the key never reaches argv or the envelope, and with no
// key anywhere nothing is sent.
func TestDecideUsesTheStoredKeyAndNeverArgv(t *testing.T) {
	fr, cfgPath, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"request_id":"r","answers":{}}`))
	})
	cfg, _, err := loadConfig(cfgPath, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	const stored = "sr-stored-fictional-0123456789"
	if err := writeCredentials(credentialsPath(cfg.Search), credentials{APIKey: stored}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--stdin", "-config", cfgPath}
	code, out, _ := runDecide(t, map[string]string{}, decideMinimal, args...)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	req, _ := fr.last(t)
	if got := req.Header.Get("Authorization"); got != "Bearer "+stored {
		t.Errorf("Authorization %q, want the stored key", got)
	}
	if strings.Contains(out, stored) {
		t.Error("the envelope repeats the key")
	}

	if err := os.Remove(credentialsPath(cfg.Search)); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runDecide(t, map[string]string{}, decideMinimal, args...)
	env := decodeEnvelope(t, out)
	if code != exitClientErr || env["action"] != actionConnect || len(fr.reqs) != 1 {
		t.Errorf("exit %d, %v, %d requests; want %d, connect, still 1", code, env["action"], len(fr.reqs), exitClientErr)
	}
}

// The stored key goes only to the router it records, on both forms.
func TestDecideSendsTheStoredKeyOnlyToItsRouter(t *testing.T) {
	answering := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{},"models":[]}`))
	}
	for _, tc := range []struct {
		name, mode string
	}{{"decide", "--stdin"}, {"models", "--models"}} {
		t.Run(tc.name, func(t *testing.T) {
			own, cfg, root := newFakeRouter(t, answering)
			other, otherConns := countingServer(t, http.HandlerFunc(answering))
			otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
			storeKey(t, cfg, credentials{Router: origin(t, own.srv.URL)})
			env := map[string]string{"JEVLIN_CONFIG": cfg}

			code, out, _ := runDecide(t, env, decideMinimal, tc.mode, "-config", otherCfg)
			assertOriginRefusal(t, code, out, origin(t, own.srv.URL), origin(t, other.URL),
				root, cfg, otherCfg, credentialsPath(searchBlockOf(t, cfg)))
			if n := otherConns(); n != 0 {
				t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
			}

			if code, out, _ := runDecide(t, env, decideMinimal, tc.mode, "-config", cfg); code != exitOK {
				t.Fatalf("the key's own router: exit %d\n%s", code, out)
			}
			if req, _ := own.last(t); req.Header.Get("Authorization") != "Bearer "+boundKey {
				t.Errorf("the key's own router was sent Authorization %q", req.Header.Get("Authorization"))
			}
		})
	}
}

// A decide carries the sr- key, so a redirect to another origin is refused
// before anything is sent there, counted in connections.
func TestADecideRedirectedOffTheRoutersOriginSendsNothingThere(t *testing.T) {
	var dialed atomic.Int32
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{}}`))
	}))
	other.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dialed.Add(1)
		}
	}
	other.Start()
	t.Cleanup(other.Close)

	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/decisions", http.StatusTemporaryRedirect)
	})
	code, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg)
	env := decodeEnvelope(t, out)
	if code == exitOK || env["ok"] != false {
		t.Errorf("a decide redirected off-origin succeeded: %v", env)
	}
	if n := dialed.Load(); n != 0 {
		t.Errorf("the other origin was dialed %d times", n)
	}
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests to the router", len(fr.reqs))
	}
}

// A second -config is refused before anything is read or sent, as for search
// and fetch.
func TestDecideRefusesASecondConfig(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, nil)
	for _, mode := range []string{"--stdin", "--models"} {
		code, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, mode, "-config", cfg, "-config", cfg)
		env := decodeEnvelope(t, out)
		if code != exitUsage || env["code"] != codeInvalidFlags || len(fr.reqs) != 0 {
			t.Errorf("%s: exit %d, %v, %d requests; want %d, %s, none", mode, code, env["code"], len(fr.reqs), exitUsage, codeInvalidFlags)
		}
	}
}
