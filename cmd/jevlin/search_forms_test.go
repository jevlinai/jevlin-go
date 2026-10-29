package main

// The query forms: objective and search_queries on the --stdin request,
// passed to the router (Jevlin-core #55), and retried away once when the
// router refuses them. The codes are written out rather than named by their
// constants, because they are the wire contract a caller branches on.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// sequencedRouter answers each request with the next of answers (the last
// one repeated) and keeps every body it received, in order.
type sequencedRouter struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func newSequencedRouter(t *testing.T, answers ...func(w http.ResponseWriter)) (*sequencedRouter, string, string) {
	t.Helper()
	sr := &sequencedRouter{}
	var fr *fakeRouter
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		fr.mu.Lock()
		raw := fr.body
		fr.mu.Unlock()
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("router body: %v", err)
		}
		sr.mu.Lock()
		sr.bodies = append(sr.bodies, body)
		n := len(sr.bodies)
		sr.mu.Unlock()
		answers[min(n, len(answers))-1](w)
	})
	return sr, cfg, root
}

func (sr *sequencedRouter) sent() []map[string]any {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return append([]map[string]any(nil), sr.bodies...)
}

func routerRefuses(code string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"the router's own words, which nothing reads","code":"` + code + `"}`))
	}
}

func routerAnswers(raw []byte) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = w.Write(raw) }
}

func TestQueryFormsTravelToTheRouter(t *testing.T) {
	raw, _ := readRouterFixture(t, "merged-view-success.main.json")
	_, sent, _ := searchServing(t, raw,
		`{"version":1,"query":"q","objective":"find what the user asked, in my words","search_queries":["q reworded","Q's vendor name"]}`)
	if sent["objective"] != "find what the user asked, in my words" {
		t.Errorf("objective: %v", sent["objective"])
	}
	forms, _ := sent["search_queries"].([]any)
	if len(forms) != 2 || forms[0] != "q reworded" || forms[1] != "Q's vendor name" {
		t.Errorf("search_queries: %v", sent["search_queries"])
	}
	if sent["query"] != "q" {
		t.Errorf("query: %v", sent["query"])
	}
}

func TestQueryFormsRefusedBeforeRouterCall(t *testing.T) {
	long := strings.Repeat("x", 4097)
	for _, tc := range []struct{ name, stdin, code, field string }{
		{"five search_queries", `{"version":1,"query":"q","search_queries":["a","b","c","d","e"]}`, "invalid_search_queries", `"search_queries"`},
		{"a non-string entry", `{"version":1,"query":"q","search_queries":["a",7]}`, "invalid_search_queries", `"search_queries"`},
		{"not an array", `{"version":1,"query":"q","search_queries":"a"}`, "invalid_search_queries", `"search_queries"`},
		{"an empty array", `{"version":1,"query":"q","search_queries":[]}`, "invalid_search_queries", `"search_queries"`},
		{"null search_queries", `{"version":1,"query":"q","search_queries":null}`, "invalid_search_queries", `"search_queries"`},
		{"a blank entry", `{"version":1,"query":"q","search_queries":["a"," "]}`, "invalid_search_queries", `"search_queries"`},
		{"an entry over 4096 bytes", `{"version":1,"query":"q","search_queries":["` + long + `"]}`, "invalid_search_queries", `"search_queries"`},
		{"objective not a string", `{"version":1,"query":"q","objective":["a"]}`, "invalid_objective", `"objective"`},
		{"null objective", `{"version":1,"query":"q","objective":null}`, "invalid_objective", `"objective"`},
		{"a blank objective", `{"version":1,"query":"q","objective":"  "}`, "invalid_objective", `"objective"`},
		{"an objective over 4096 bytes", `{"version":1,"query":"q","objective":"` + long + `"}`, "invalid_objective", `"objective"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, nil)
			h := fixedSearchOps(root)
			code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, tc.stdin, "-config", cfg)
			if code != exitUsage {
				t.Fatalf("exit %d, want %d: %s", code, exitUsage, out)
			}
			env := decodeEnvelope(t, out)
			if env["code"] != tc.code || env["action"] != "fix_input" {
				t.Errorf("code %v action %v, want %s and fix_input", env["code"], env["action"], tc.code)
			}
			if msg, _ := objOf(env["error"])["message"].(string); !strings.Contains(msg, tc.field) {
				t.Errorf("the message does not name %s: %q", tc.field, msg)
			}
			fr.mu.Lock()
			defer fr.mu.Unlock()
			if len(fr.reqs) != 0 {
				t.Error("a refused request reached the router")
			}
		})
	}
}

// A router that predates the forms refuses the whole request over them, as
// malformed_json. The search is retried once without them and answers ok,
// saying in the envelope that the forms were dropped, and nowhere else;
// everything else the first request carried goes again.
func TestARouterThatRefusesTheFormsGetsOneRetryWithoutThem(t *testing.T) {
	raw, _ := readRouterFixture(t, "merged-view-success.main.json")
	sr, cfg, root := newSequencedRouter(t, routerRefuses("malformed_json"), routerAnswers(raw))
	h := fixedSearchOps(root)
	code, out, errOut := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","tier":"balanced","objective":"why","search_queries":["q2"]}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	env := decodeEnvelope(t, out)
	if env["ok"] != true || resultOf(t, env)["forms_dropped"] != true {
		t.Errorf("ok %v, forms_dropped %v; want both true", env["ok"], resultOf(t, env)["forms_dropped"])
	}
	bodies := sr.sent()
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want the first and one retry", len(bodies))
	}
	first, retry := bodies[0], bodies[1]
	if first["objective"] == nil || first["search_queries"] == nil {
		t.Fatalf("the first request did not carry the forms: %v", first)
	}
	for _, k := range []string{"objective", "search_queries"} {
		if _, ok := retry[k]; ok {
			t.Errorf("the retry still carries %s: %v", k, retry)
		}
	}
	for _, k := range []string{"query", "tier", "view", "trace", "session_id"} {
		if !jsonEqual(first[k], retry[k]) || retry[k] == nil {
			t.Errorf("the retry changed %s: %v, then %v", k, first[k], retry[k])
		}
	}
	if withoutOriginNotice(errOut) != "" {
		t.Errorf("machine mode wrote on stderr: %q", errOut)
	}
}

// malformed_json licenses the retry only for a request that carried the
// forms: to any other request it is answered as it came, once.
func TestMalformedJSONWithoutTheFormsIsNotRetried(t *testing.T) {
	sr, cfg, root := newSequencedRouter(t, routerRefuses("malformed_json"))
	h := fixedSearchOps(root)
	code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"}, `{"version":1,"query":"q"}`, "-config", cfg)
	if code == exitOK {
		t.Fatalf("exit 0 for a refused request: %s", out)
	}
	if n := len(sr.sent()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	if result := decodeEnvelope(t, out)["result"]; result != nil {
		t.Errorf("a refused search carries a result: %v", result)
	}
}

// A request that carried the forms is retried on malformed_json and on
// nothing else: any other 400 is the router's answer, the one POST is all
// it costs, and forms_dropped (which says a retry ran) is not set.
func TestAnotherRefusalOfARequestCarryingTheFormsIsNotRetried(t *testing.T) {
	sr, cfg, root := newSequencedRouter(t, routerRefuses("invalid_request"))
	h := fixedSearchOps(root)
	code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","objective":"why","search_queries":["q2"]}`, "-config", cfg)
	if code == exitOK {
		t.Fatalf("exit 0 for a refused request: %s", out)
	}
	bodies := sr.sent()
	if len(bodies) != 1 {
		t.Fatalf("%d requests, want exactly 1", len(bodies))
	}
	if bodies[0]["objective"] == nil || bodies[0]["search_queries"] == nil {
		t.Fatalf("the request did not carry the forms, so this test proves nothing: %v", bodies[0])
	}
	env := decodeEnvelope(t, out)
	if env["result"] != nil {
		t.Errorf("a refused search carries a result: %v", env["result"])
	}
	if strings.Contains(out, "forms_dropped") {
		t.Errorf("forms_dropped is in the envelope though no retry ran: %s", out)
	}
}

// The retry is never recursive: a router that refuses the retry too gets no
// third request, and its refusal is the answer.
func TestTheFormsRetryIsTheOnlyRetry(t *testing.T) {
	sr, cfg, root := newSequencedRouter(t, routerRefuses("malformed_json"))
	h := fixedSearchOps(root)
	code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","search_queries":["q2"]}`, "-config", cfg)
	if code == exitOK {
		t.Fatalf("exit 0 though the router refused both requests: %s", out)
	}
	if n := len(sr.sent()); n != 2 {
		t.Errorf("%d requests, want the first and one retry", n)
	}
}

// A router old enough to refuse the trace predates the forms too, and the
// trace's retry is the only one: it leaves both out.
func TestTheTraceRetryDropsTheFormsToo(t *testing.T) {
	raw, _ := readRouterFixture(t, "merged-view-success.main.json")
	sr, cfg, root := newSequencedRouter(t, routerRefuses("trace_unsupported"), routerAnswers(raw))
	h := fixedSearchOps(root)
	code, out, _ := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q","objective":"why","search_queries":["q2"]}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	bodies := sr.sent()
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want 2", len(bodies))
	}
	if bodies[0]["trace"] == nil {
		t.Fatalf("the first request carried no trace, so this test proves nothing: %v", bodies[0])
	}
	for _, k := range []string{"trace", "session_id", "objective", "search_queries"} {
		if _, ok := bodies[1][k]; ok {
			t.Errorf("the retry still carries %s", k)
		}
	}
	if resultOf(t, decodeEnvelope(t, out))["forms_dropped"] != true {
		t.Error("forms_dropped is not set though the retry left the forms out")
	}
}

func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}
