package main

// `fetch --stdin` against a fake router: a bad request never reaches it,
// a good one arrives as the exact body the contract names, and every
// answer the router can give maps to the header an agent branches on. The
// router's answer is the planned contract's (router track A), built here
// because no router serves /v1/fetch yet; every identifier is synthetic.

import (
	"encoding/json"
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

func runFetchStdin(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	now := func() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }
	code := fetchMain(now, append([]string{"--stdin"}, args...), strings.NewReader(stdin), &out, &errOut, installedAs(envOf(env), args))
	return code, out.String(), errOut.String()
}

// fetchServing runs one fetch against a fake router that answers with
// status and raw, and returns the envelope, the exit code and the router.
func fetchServing(t *testing.T, status int, raw, stdin string) (map[string]any, int, *fakeRouter) {
	t.Helper()
	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(raw))
	})
	code, out, errOut := runFetchStdin(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, stdin, "-config", cfg)
	if errOut != "" {
		t.Errorf("fetch wrote on stderr: %q", errOut)
	}
	return decodeEnvelope(t, out), code, fr
}

const fetchSourceID = "s_0123456789abcdef0123456789abcdef"

// fetchRouterAnswer is the planned contract's response, with one page of
// each status the router names.
const fetchRouterAnswer = `{"request_id":"01a1-fetch-fictional",
 "pages":[
  {"source_id":"` + fetchSourceID + `","url":"https://docs.python.org/3.14/whatsnew/3.14.html","title":"What's new in Python 3.14",
   "status":"ok","content":"Free-threaded CPython is now supported.\u0000<b>","offset":0,"next_offset":10000,"content_length":20000,
   "truncated":true,"full_length":246051,
   "provider":"tavily","cached":false,"cost_micros":2000},
  {"url":"https://github.com/python/cpython/issues/1","status":"failed","reason":"upstream_failed","provider":"tavily","cached":false,"cost_micros":0},
  {"url":"https://example.test/not-searched","status":"not_recent"},
  {"source_id":"s_ffffffffffffffffffffffffffffffff","status":"invalid"}],
 "usage":{"cost_micros":2000,"latency_ms":812}}`

// Every malformed value answers fix_input, names its field, and never
// reaches the router.
func TestAFetchRequestOutsideTheContractNeverReachesTheRouter(t *testing.T) {
	page := `{"source_id":"` + fetchSourceID + `"}`
	pages := func(n int) string {
		return `[` + strings.TrimSuffix(strings.Repeat(page+",", n), ",") + `]`
	}
	long := strings.Repeat("é", fetchQueryMaxRune+1) // characters, not bytes
	for _, tc := range []struct {
		name, stdin, code, field string
	}{
		{"no pages", `{"version":1}`, codeMissingPages, `"pages"`},
		{"zero pages", `{"version":1,"pages":[]}`, codeInvalidPages, `"pages"`},
		{"six pages", `{"version":1,"pages":` + pages(6) + `}`, codeInvalidPages, `"pages"`},
		{"pages null", `{"version":1,"pages":null}`, codeInvalidPages, `"pages"`},
		{"pages not a list", `{"version":1,"pages":"s_x"}`, codeInvalidPages, `"pages"`},
		{"entry not an object", `{"version":1,"pages":["https://a.test/"]}`, codeInvalidPages, `"pages" entry 0`},
		{"both", `{"version":1,"pages":[{"source_id":"` + fetchSourceID + `","url":"https://a.test/"}]}`, codeInvalidPages, `exactly one of "source_id" or "url"`},
		{"neither", `{"version":1,"pages":[{}]}`, codeInvalidPages, `exactly one of "source_id" or "url"`},
		{"bad source_id", `{"version":1,"pages":[{"source_id":"s_0123"}]}`, codeInvalidSourceID, `"source_id"`},
		{"upper-case source_id", `{"version":1,"pages":[{"source_id":"s_0123456789ABCDEF0123456789ABCDEF"}]}`, codeInvalidSourceID, `"source_id"`},
		{"source_id null", `{"version":1,"pages":[{"source_id":null}]}`, codeInvalidSourceID, `"source_id"`},
		{"second entry bad", `{"version":1,"pages":[` + page + `,{"source_id":"r_1"}]}`, codeInvalidSourceID, `"pages" entry 1`},
		{"non-http url", `{"version":1,"pages":[{"url":"file:///etc/passwd"}]}`, codeInvalidURL, `"url"`},
		{"relative url", `{"version":1,"pages":[{"url":"/docs/page"}]}`, codeInvalidURL, `"url"`},
		{"url without host", `{"version":1,"pages":[{"url":"https:///path"}]}`, codeInvalidURL, `"url"`},
		{"max_chars 999", `{"version":1,"pages":` + pages(1) + `,"max_chars":999}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars 50001", `{"version":1,"pages":` + pages(1) + `,"max_chars":50001}`, codeInvalidMaxChars, `"max_chars"`},
		{"max_chars a string", `{"version":1,"pages":` + pages(1) + `,"max_chars":"10000"}`, codeInvalidMaxChars, `"max_chars"`},
		{"negative offset", `{"version":1,"pages":` + pages(1) + `,"offset":-1}`, codeInvalidOffset, `"offset"`},
		{"fractional offset", `{"version":1,"pages":` + pages(1) + `,"offset":1.5}`, codeInvalidOffset, `"offset"`},
		{"501-character query", `{"version":1,"pages":` + pages(1) + `,"query":"` + long + `"}`, codeInvalidQuery, `"query"`},
		{"blank query", `{"version":1,"pages":` + pages(1) + `,"query":"  "}`, codeInvalidQuery, `"query"`},
		{"unknown field", `{"version":1,"pages":` + pages(1) + `,"tier":"fast"}`, codeUnknownField, `field`},
		{"unknown field in a page", `{"version":1,"pages":[{"url":"https://a.test/","rank":1}]}`, codeUnknownField, `"pages" entry 0`},
		{"no version", `{"pages":` + pages(1) + `}`, codeUnsupportedVersion, `"version"`},
		{"version 2", `{"version":2,"pages":` + pages(1) + `}`, codeUnsupportedVersion, `fetch request version 1`},
		{"not JSON", `pages please`, codeInvalidJSON, `JSON object`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, fr := fetchServing(t, http.StatusOK, fetchRouterAnswer, tc.stdin)
			if code != exitUsage || env["code"] != tc.code || env["action"] != actionFixInput || env["retryable"] != false {
				t.Errorf("exit %d, %v / %v / retryable %v; want %d, %s / fix_input / false",
					code, env["code"], env["action"], env["retryable"], exitUsage, tc.code)
			}
			if msg, _ := objOf(env["error"])["message"].(string); !strings.Contains(msg, tc.field) {
				t.Errorf("error.message %q does not name %s", msg, tc.field)
			}
			if n := len(fr.reqs); n != 0 {
				t.Errorf("the router received %d requests for a refused fetch", n)
			}
		})
	}
}

// The range's ends are inside it.
func TestTheFetchBoundsAreInclusive(t *testing.T) {
	page := `{"url":"https://a.test/"}`
	five := `[` + strings.TrimSuffix(strings.Repeat(page+",", 5), ",") + `]`
	for _, stdin := range []string{
		`{"version":1,"pages":` + five + `,"max_chars":1000,"offset":0}`,
		`{"version":1,"pages":[` + page + `],"max_chars":50000,"query":"` + strings.Repeat("é", fetchQueryMaxRune) + `"}`,
	} {
		env, code, _ := fetchServing(t, http.StatusOK, fetchRouterAnswer, stdin)
		if code != exitOK {
			t.Errorf("exit %d for a request inside the bounds: %v", code, env)
		}
	}
}

// What the router receives is the request without its version, each page
// as the caller named it, with the stored options and nothing else, posted
// to /v1/fetch with the participant's key.
func TestTheFetchBodyIsTheRequestWithoutItsVersion(t *testing.T) {
	stdin := `{"version":1,"pages":[{"source_id":"` + fetchSourceID + `"},{"url":"https://example.test/a?b=c#d"}],` +
		`"max_chars":10000,"offset":20000,"query":"free-threaded build"}`
	env, code, fr := fetchServing(t, http.StatusOK, fetchRouterAnswer, stdin)
	if code != exitOK {
		t.Fatalf("exit %d: %v", code, env)
	}
	req, body := fr.last(t)
	if req.Method != http.MethodPost || req.URL.Path != "/v1/fetch" {
		t.Errorf("%s %s, want POST /v1/fetch", req.Method, req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sr-fictional-key" {
		t.Errorf("Authorization %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != clientIdentifier() {
		t.Errorf("User-Agent %q, want search's %q", got, clientIdentifier())
	}
	var sent, want any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(`{"pages":[{"source_id":"`+fetchSourceID+`"},{"url":"https://example.test/a?b=c#d"}],`+
		`"max_chars":10000,"offset":20000,"query":"free-threaded build"}`), &want)
	if !reflect.DeepEqual(sent, want) {
		t.Errorf("the router received %s\nwant %v", body, want)
	}
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests; a fetch is one POST", len(fr.reqs))
	}

	// An option left out is left out of the body.
	_, _, fr = fetchServing(t, http.StatusOK, fetchRouterAnswer, `{"version":1,"pages":[{"url":"https://example.test/"}]}`)
	_, body = fr.last(t)
	if string(body) != `{"pages":[{"url":"https://example.test/"}]}` {
		t.Errorf("the router received %s", body)
	}
}

// The router's pages come back field for field, in its order, whatever
// each one's status; a call with failed, not_recent and invalid pages is
// still a call that worked.
func TestTheFetchResultIsTheRoutersPages(t *testing.T) {
	env, code, _ := fetchServing(t, http.StatusOK, fetchRouterAnswer, `{"version":1,"pages":[{"source_id":"`+fetchSourceID+`"}]}`)
	if code != exitOK || env["ok"] != true || env["command"] != "fetch" || env["action"] != actionNone {
		t.Fatalf("exit %d: %v", code, env)
	}
	var router map[string]any
	if err := json.Unmarshal([]byte(fetchRouterAnswer), &router); err != nil {
		t.Fatal(err)
	}
	result := resultOf(t, env)
	if !reflect.DeepEqual(result["pages"], router["pages"]) {
		t.Errorf("pages %v\nthe router's %v", result["pages"], router["pages"])
	}
	if !reflect.DeepEqual(result["usage"], router["usage"]) {
		t.Errorf("usage %v, the router's %v", result["usage"], router["usage"])
	}
	if env["request_id"] != "01a1-fetch-fictional" || result["request_id"] != "01a1-fetch-fictional" {
		t.Errorf("request_id %v / %v", env["request_id"], result["request_id"])
	}
	var statuses []any
	for _, p := range listOf(result["pages"]) {
		statuses = append(statuses, objOf(p)["status"])
	}
	if want := []any{"ok", "failed", "not_recent", "invalid"}; !reflect.DeepEqual(statuses, want) {
		t.Errorf("statuses %v, want %v", statuses, want)
	}
}

// The router's fetch types are copied into the envelope by conversion, so
// a field either side grows stops the build; this list makes the router's
// side a diff, as TestTheRouterCitationsFieldsAreDecided does for search.
func TestTheRouterFetchFieldsAreDecided(t *testing.T) {
	for _, tc := range []struct {
		rt      reflect.Type
		decided []string
	}{
		{reflect.TypeFor[routerFetchPage](), []string{
			"SourceID source_id,omitempty", "URL url,omitempty", "Title title,omitempty", "Status status",
			"Content content,omitempty", "Offset offset,omitempty", "NextOffset next_offset,omitempty",
			"ContentLength content_length,omitempty", "Truncated truncated,omitempty", "FullLength full_length,omitempty",
			"Reason reason,omitempty", "Provider provider,omitempty", "Cached cached,omitempty",
			"CostMicros cost_micros,omitempty",
		}},
		{reflect.TypeFor[routerFetchUsage](), []string{"CostMicros cost_micros,omitempty", "LatencyMS latency_ms,omitempty"}},
		{reflect.TypeFor[routerFetchResponse](), []string{"RequestID request_id", "Pages pages", "Usage usage,omitempty"}},
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

// Each router answer that is not a success maps to the header an agent
// acts on, by status and the router's exact code, never its message.
func TestEachFetchFailureMapsToItsAction(t *testing.T) {
	stdin := `{"version":1,"pages":[{"source_id":"` + fetchSourceID + `"}]}`
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		exit      int
		code      string
		retryable bool
		action    string
	}{
		{"unclaimed account", 403, `{"code":"fetch_unavailable","error":"fetch needs a claimed account"}`, exitClientErr, "fetch_unavailable", false, actionConnect},
		{"other 403", 403, `{"code":"forbidden","error":"no"}`, exitClientErr, "forbidden", false, actionCheckAccess},
		{"no endpoint, 404", 404, `404 page not found`, exitClientErr, "fetch_not_supported", false, actionReport},
		{"no endpoint, 404 with a code", 404, `{"code":"not_found","error":"no route"}`, exitClientErr, "fetch_not_supported", false, actionReport},
		{"no endpoint, 405", 405, ``, exitClientErr, "fetch_not_supported", false, actionReport},
		{"fetch switched off", 503, `{"code":"fetch_disabled","error":"fetch is not enabled on this router"}`, exitClientErr, "fetch_disabled", false, actionReport},
		{"another 503, the platform paused", 503, `{"code":"platform_paused","error":"paused"}`, exitServerErr, "platform_paused", true, actionRetry},
		{"bare 503", 503, ``, exitServerErr, "router_error", true, actionRetry},
		{"upstream failed", 502, `{"code":"upstream_failed","error":"every fetch failed"}`, exitServerErr, "upstream_failed", true, actionRetry},
		{"bare 502", 502, ``, exitServerErr, "router_error", true, actionRetry},
		{"key refused", 401, `{"code":"unauthorized","error":"bad key"}`, exitClientErr, "unauthorized", false, actionLogin},
		{"rate limited", 429, `{"code":"rate_limited","error":"slow down"}`, exitClientErr, "rate_limited", true, actionRetry},
		{"bad request", 400, `{"code":"invalid_request","error":"pages"}`, exitClientErr, "invalid_request", false, actionFixInput},
		{"2xx without pages", 200, `{"request_id":"r"}`, exitServerErr, string(faultInvalidResponse), false, actionReport},
		{"2xx not JSON", 200, `<html>`, exitServerErr, string(faultInvalidResponse), false, actionReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, fr := fetchServing(t, tc.status, tc.body, stdin)
			if code != tc.exit || env["exit_code"] != float64(tc.exit) || env["code"] != tc.code ||
				env["retryable"] != tc.retryable || env["action"] != tc.action || env["ok"] != false {
				t.Errorf("exit %d, %v / retryable %v / %v; want %d, %s / %v / %s",
					code, env["code"], env["retryable"], env["action"], tc.exit, tc.code, tc.retryable, tc.action)
			}
			if _, has := env["result"]; has {
				t.Errorf("a failed fetch carries a result: %v", env["result"])
			}
			if len(fr.reqs) != 1 {
				t.Errorf("%d requests; a failed fetch is not retried", len(fr.reqs))
			}
		})
	}
}

// With no key in the environment the fetch uses the one stored in
// credentials.json, as search does; the key reaches the router in
// Authorization and never in the process's arguments.
func TestFetchUsesTheStoredKeyAndNeverArgv(t *testing.T) {
	fr, cfgPath, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fetchRouterAnswer))
	})
	cfg, _, err := loadConfig(cfgPath, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	const stored = "sr-stored-fictional-0123456789"
	if err := writeCredentials(credentialsPath(cfg.Miner), credentials{APIKey: stored}); err != nil {
		t.Fatal(err)
	}
	args := []string{"-config", cfgPath}
	code, out, _ := runFetchStdin(t, map[string]string{}, `{"version":1,"pages":[{"url":"https://example.test/"}]}`, args...)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	req, _ := fr.last(t)
	if got := req.Header.Get("Authorization"); got != "Bearer "+stored {
		t.Errorf("Authorization %q, want the stored key", got)
	}
	for _, a := range append([]string{"--stdin"}, args...) {
		if strings.Contains(a, stored) {
			t.Errorf("the key is in argv: %q", a)
		}
	}
	if strings.Contains(out, stored) {
		t.Error("the envelope repeats the key")
	}

	// And with no key anywhere, nothing is sent and the action is connect.
	if err := os.Remove(credentialsPath(cfg.Miner)); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runFetchStdin(t, map[string]string{}, `{"version":1,"pages":[{"url":"https://example.test/"}]}`, args...)
	env := decodeEnvelope(t, out)
	if code != exitClientErr || env["action"] != actionConnect || len(fr.reqs) != 1 {
		t.Errorf("exit %d, %v, %d requests; want %d, connect, still 1", code, env["action"], len(fr.reqs), exitClientErr)
	}
}

// A fetch carries the sr- key, so a redirect to another origin is refused
// before anything is sent there, counted in connections: the second
// server is never dialed. A redirect within the router's origin is
// followed, as a search's is.
func TestAFetchRedirectedOffTheRoutersOriginSendsNothingThere(t *testing.T) {
	var dialed atomic.Int32
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fetchRouterAnswer))
	}))
	other.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dialed.Add(1)
		}
	}
	other.Start()
	t.Cleanup(other.Close)

	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/fetch":
			http.Redirect(w, r, other.URL+"/v1/fetch", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	})
	code, out, _ := runFetchStdin(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"},
		`{"version":1,"pages":[{"url":"https://example.test/"}]}`, "-config", cfg)
	env := decodeEnvelope(t, out)
	if code == exitOK || env["ok"] != false {
		t.Errorf("a fetch redirected off-origin succeeded: %v", env)
	}
	if n := dialed.Load(); n != 0 {
		t.Errorf("the other origin was dialed %d times", n)
	}
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests to the router", len(fr.reqs))
	}

	// Same origin: followed, with the key.
	fr, cfg, _ = newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/fetch" {
			http.Redirect(w, r, "/v1/fetch-moved", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(fetchRouterAnswer))
	})
	code, out, _ = runFetchStdin(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"},
		`{"version":1,"pages":[{"url":"https://example.test/"}]}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("a same-origin redirect was not followed: %s", out)
	}
	if len(fr.reqs) != 2 {
		t.Errorf("%d requests; want the redirect and the request it named", len(fr.reqs))
	}
	if last, _ := fr.last(t); last.URL.Path != "/v1/fetch-moved" || last.Header.Get("Authorization") != "Bearer sr-fictional-key" {
		t.Errorf("the followed request carried Authorization %q", last.Header.Get("Authorization"))
	}
}

// Without --stdin there is no fetch: a usage error, nothing on stdout.
func TestFetchNeedsStdin(t *testing.T) {
	var out, errOut strings.Builder
	code := fetchMain(time.Now, []string{"-config", filepath.Join(t.TempDir(), "none.toml")}, strings.NewReader(""), &out, &errOut, envOf(nil))
	if code != exitUsage || out.Len() != 0 || !strings.Contains(errOut.String(), "--stdin") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

// A second -config is refused before anything is read or sent, as for a
// search: an allow rule ends after the installed -config, and a second one
// could otherwise send the key to another config's router.
// A second -config after the rendered prefix, naming another router, is
// refused before either config's router is dialed: the allow rule ends
// after the installed -config ... --stdin, so whatever follows it must not
// choose where the key goes.
func TestFetchRefusesASecondConfigNamingAnotherRouter(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, nil)
	a, aConns := countingServer(t, nil)
	b, bConns := countingServer(t, nil)
	cfgA := sameStateOtherURL(t, cfg, fr.srv.URL, a.URL)
	cfgB := sameStateOtherURL(t, cfg, fr.srv.URL, b.URL)
	var out, errOut strings.Builder
	code := fetchMain(time.Now, []string{"-config", cfgA, "--stdin", "-config", cfgB},
		strings.NewReader(`{"version":1,"pages":[{"url":"https://example.test/"}]}`), &out, &errOut,
		envOf(map[string]string{"JEVLIN_CONFIG": cfgA, apiKeyEnv: boundKey}))
	if env := decodeEnvelope(t, out.String()); code != exitUsage || env["code"] != codeInvalidFlags || env["action"] != actionFixInput {
		t.Errorf("exit %d, %v, %v; want %d, %s, %s\n%s", code, env["code"], env["action"], exitUsage, codeInvalidFlags, actionFixInput, out.String())
	}
	if n, m := aConns(), bConns(); n != 0 || m != 0 || len(fr.reqs) != 0 {
		t.Errorf("connections: %d to the first config's router, %d to the second's, %d request(s) to the stub; want none", n, m, len(fr.reqs))
	}
}

func TestFetchRefusesASecondConfig(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, nil)
	code, out, _ := runFetchStdin(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"},
		`{"version":1,"pages":[{"url":"https://example.test/"}]}`, "-config", cfg, "-config", cfg)
	env := decodeEnvelope(t, out)
	if code != exitUsage || env["code"] != codeInvalidFlags || len(fr.reqs) != 0 {
		t.Errorf("exit %d, %v, %d requests; want %d, %s, none", code, env["code"], len(fr.reqs), exitUsage, codeInvalidFlags)
	}
}
