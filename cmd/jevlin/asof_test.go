package main

// as_of: a dated search and a dated read, on the command line, through
// --stdin and through the MCP tools. Each test is held to the bytes the
// router receives or to the answer it gives; the router is a loopback stub
// serving what the router's own handlers wrote (testdata/search/router and
// testdata/asof, each with a README saying how).

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// asOfEnv is a key and no trace, so a body is the same bytes on every run.
var asOfEnv = map[string]string{"JEVLIN_API_KEY": "sr-fictional", "JEVLIN_TRACE": "off"}

const asOfCutoff = "2026-03-01T13:00:00+01:00"

// routerDatedRead is POST /v1/fetch with as_of as the router's own fetch
// handler wrote it (testdata/asof/README.md), checked against its SHA-256 so
// it cannot be edited into a router that does not exist.
func routerDatedRead(t *testing.T) []byte {
	t.Helper()
	const sum = "a38cc354790bb22fb85aba1d11d8c9c7745ce556e58332f8c63bfdc2d2bffa3e"
	raw, err := os.ReadFile(filepath.Join("testdata", "asof", "fetch-dated.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(raw); hex.EncodeToString(got[:]) != sum {
		t.Fatal("testdata/asof/fetch-dated.json is not the answer recorded from the router; see its README")
	}
	return raw
}

// asOfBodyCase is one request through one path, and the body main sent for
// it before as_of existed. The bodies were taken from origin/main at
// 50250f3 with the same requests and the trace off.
type asOfBodyCase struct {
	name   string
	before string
	// send runs the request, dated when asOf is true, against a router
	// serving answer, and returns the body the router received.
	send func(t *testing.T, dated bool) string
}

func asOfBodyCases() []asOfBodyCase {
	searchAnswer := func(*testing.T) string { return routerBody }
	fetchAnswer := func(*testing.T) string { return `{"request_id":"r","pages":[]}` }
	via := func(answer func(*testing.T) string, run func(t *testing.T, cfg, root string, dated bool)) func(*testing.T, bool) string {
		return func(t *testing.T, dated bool) string {
			fr, cfg, root := newFakeRouter(t, okHandler(answer(t)))
			run(t, cfg, root, dated)
			_, body := fr.last(t)
			return string(body)
		}
	}
	withAsOf := func(request string, dated bool) string {
		if !dated {
			return request
		}
		return strings.TrimSuffix(request, "}") + `,"as_of":"` + asOfCutoff + `"}`
	}
	return []asOfBodyCase{
		{
			name:   "search --stdin",
			before: `{"cache":"refresh","domain_filter":["go.dev"],"max_results":5,"page_chars":500,"providers":["exa"],"query":"go release notes","tier":"balanced","view":"merged"}`,
			send: via(searchAnswer, func(t *testing.T, cfg, root string, dated bool) {
				runSearchStdin(t, fixedSearchOps(root), asOfEnv, withAsOf(
					`{"version":1,"query":"go release notes","tier":"balanced","domain_filter":["go.dev"],"max_results":5,"providers":["exa"],"cache":"refresh"}`, dated),
					"-config", cfg)
			}),
		},
		{
			name:   "search on the command line",
			before: `{"query":"go release notes","tier":"balanced"}`,
			send: via(searchAnswer, func(t *testing.T, cfg, root string, dated bool) {
				args := []string{"-config", cfg, "-tier", "balanced"}
				if dated {
					args = append(args, "-as-of", asOfCutoff)
				}
				runSearch(t, fixedSearchOps(root), asOfEnv, append(args, "go", "release", "notes")...)
			}),
		},
		{
			name:   "fetch --stdin",
			before: `{"max_chars":2000,"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}],"query":"retry"}`,
			send: via(fetchAnswer, func(t *testing.T, cfg, _ string, dated bool) {
				runFetchStdin(t, asOfEnv, withAsOf(
					`{"version":1,"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}],"max_chars":2000,"query":"retry"}`, dated),
					"-config", cfg)
			}),
		},
		{
			name:   "the MCP search tool",
			before: `{"page_chars":500,"query":"go release notes","tier":"balanced","view":"merged"}`,
			send: via(searchAnswer, func(t *testing.T, cfg, _ string, dated bool) {
				runMCP(t, asOfEnv, []string{mcpInitialize,
					toolCall(2, "search", withAsOf(`{"query":"go release notes","tier":"balanced"}`, dated))}, "-config", cfg)
			}),
		},
		{
			name:   "the MCP fetch tool",
			before: `{"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}]}`,
			send: via(fetchAnswer, func(t *testing.T, cfg, _ string, dated bool) {
				runMCP(t, asOfEnv, []string{mcpInitialize,
					toolCall(2, "fetch", withAsOf(`{"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}]}`, dated))}, "-config", cfg)
			}),
		},
	}
}

// A live request is the body it always was, byte for byte, on every path:
// as_of goes out only when the caller sent it.
func TestALiveRequestSendsTheBodyItAlwaysDid(t *testing.T) {
	for _, tc := range asOfBodyCases() {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.send(t, false); got != tc.before {
				t.Errorf("body\n got %s\nwant %s", got, tc.before)
			}
		})
	}
}

// A dated request is the live body with "as_of" and nothing else added, and
// the cutoff goes out as the caller wrote it: normalizing it is the router's.
func TestADatedRequestAddsOnlyAsOf(t *testing.T) {
	for _, tc := range asOfBodyCases() {
		t.Run(tc.name, func(t *testing.T) {
			want := `{"as_of":"` + asOfCutoff + `",` + strings.TrimPrefix(tc.before, "{")
			if got := tc.send(t, true); got != want {
				t.Errorf("body\n got %s\nwant %s", got, want)
			}
		})
	}
}

// What this client refuses before sending: an as_of that is not a
// non-blank string, an as_of beside recency, and -as-of where it cannot
// apply. Each is exit 2 with fix_input, and nothing reaches the router.
func TestAnAsOfThatCannotBeSentIsRefusedHere(t *testing.T) {
	for _, tc := range []struct {
		name, command, stdin, code string
	}{
		{"empty", "search", `{"version":1,"query":"q","as_of":""}`, codeAsOfInvalid},
		{"blank", "search", `{"version":1,"query":"q","as_of":"   "}`, codeAsOfInvalid},
		{"null", "search", `{"version":1,"query":"q","as_of":null}`, codeAsOfInvalid},
		{"a number", "search", `{"version":1,"query":"q","as_of":1767225600}`, codeAsOfInvalid},
		{"an object", "search", `{"version":1,"query":"q","as_of":{}}`, codeAsOfInvalid},
		{"with recency", "search", `{"version":1,"query":"q","recency":"week","as_of":"2026-03-01T12:00:00Z"}`, codeAsOfWithRecency},
		{"misspelled", "search", `{"version":1,"query":"q","As_Of":"2026-03-01T12:00:00Z"}`, codeUnknownField},
		{"named twice", "search", `{"version":1,"query":"q","as_of":"2026-03-01T12:00:00Z","as_of":"2026-03-02T12:00:00Z"}`, codeAsOfInvalid},
		{"named twice, once escaped", "search", `{"version":1,"query":"q","as\u005fof":"2026-03-01T12:00:00Z","as_of":"2026-03-02T12:00:00Z"}`, codeAsOfInvalid},
		{"fetch, named twice", "fetch", `{"version":1,"pages":[{"url":"https://a.test/"}],"as_of":"2026-03-01T12:00:00Z","as_of":"2026-03-02T12:00:00Z"}`, codeAsOfInvalid},
		{"fetch, empty", "fetch", `{"version":1,"pages":[{"url":"https://a.test/"}],"as_of":""}`, codeAsOfInvalid},
		{"fetch, null", "fetch", `{"version":1,"pages":[{"url":"https://a.test/"}],"as_of":null}`, codeAsOfInvalid},
		{"fetch, a number", "fetch", `{"version":1,"pages":[{"url":"https://a.test/"}],"as_of":1}`, codeAsOfInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, okHandler(routerBody))
			var code int
			var out string
			if tc.command == "search" {
				code, out, _ = runSearchStdin(t, fixedSearchOps(root), asOfEnv, tc.stdin, "-config", cfg)
			} else {
				code, out, _ = runFetchStdin(t, asOfEnv, tc.stdin, "-config", cfg)
			}
			env := decodeEnvelope(t, out)
			if code != exitUsage || env["code"] != tc.code || env["action"] != actionFixInput || env["retryable"] != false {
				t.Errorf("exit %d envelope %v, want exit 2, %s, fix_input", code, env, tc.code)
			}
			if len(fr.reqs) != 0 {
				t.Errorf("%d requests reached the router", len(fr.reqs))
			}
		})
	}
	t.Run("named twice through MCP", func(t *testing.T) {
		fr, cfg, _ := newFakeRouter(t, okHandler(routerBody))
		_, msgs, _ := runMCP(t, asOfEnv, []string{mcpInitialize,
			toolCall(2, "search", `{"query":"q","as_of":"2026-03-01T12:00:00Z","as_of":"2026-03-02T12:00:00Z"}`),
			toolCall(3, "fetch", `{"pages":[{"url":"https://a.test/"}],"as_of":"2026-03-01T12:00:00Z","as_of":"2026-03-02T12:00:00Z"}`)},
			"-config", cfg)
		for _, m := range msgs[1:] {
			if env, isError := toolEnvelope(t, m); !isError || env["code"] != codeAsOfInvalid || env["exit_code"] != float64(exitUsage) {
				t.Errorf("isError %v envelope %v", isError, env)
			}
		}
		if len(fr.reqs) != 0 {
			t.Errorf("%d requests reached the router", len(fr.reqs))
		}
	})
	t.Run("an empty -as-of", func(t *testing.T) {
		fr, cfg, root := newFakeRouter(t, okHandler(routerBody))
		code, _, errOut := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "-as-of", "", "q")
		if code != exitUsage || !strings.Contains(errOut, "-as-of needs an RFC 3339 time") || len(fr.reqs) != 0 {
			t.Errorf("exit %d, %d requests, stderr %q", code, len(fr.reqs), errOut)
		}
	})
	t.Run("-as-of beside --stdin", func(t *testing.T) {
		fr, cfg, root := newFakeRouter(t, okHandler(routerBody))
		code, out, _ := runSearchStdin(t, fixedSearchOps(root), asOfEnv, `{"version":1,"query":"q"}`,
			"-config", cfg, "-as-of", "2026-03-01T12:00:00Z")
		if env := decodeEnvelope(t, out); code != exitUsage || env["code"] != codeInvalidFlags || len(fr.reqs) != 0 {
			t.Errorf("exit %d envelope %v, %d requests", code, env, len(fr.reqs))
		}
	})
}

// A dated search's answer says what it is: result.as_of is the cutoff the
// router echoed, filters_applied.as_of how many results its check dropped,
// and a person reading either human form is told both.
func TestADatedSearchSaysWhichCutoffItIsAsOf(t *testing.T) {
	answer := string(readSearchTestdata(t, filepath.Join("router", "dated-search-merged.json")))
	const said = "as of 2026-03-01T12:00:00Z: the web as it was saved then; 2 results dropped for a publication date after it"

	t.Run("--stdin", func(t *testing.T) {
		_, cfg, root := newFakeRouter(t, okHandler(answer))
		code, out, _ := runSearchStdin(t, fixedSearchOps(root), asOfEnv,
			`{"version":1,"query":"q","as_of":"`+asOfCutoff+`"}`, "-config", cfg)
		result := resultOf(t, decodeEnvelope(t, out))
		fa, _ := result["filters_applied"].(map[string]any)
		asOf, _ := fa["as_of"].(map[string]any)
		if code != exitOK || result["as_of"] != "2026-03-01T12:00:00Z" ||
			asOf["cutoff"] != "2026-03-01T12:00:00Z" || asOf["dropped"] != float64(2) {
			t.Errorf("exit %d, as_of %v, filters_applied %v", code, result["as_of"], fa)
		}
		if merged, _ := result["merged"].([]any); len(merged) != 3 {
			t.Errorf("%d merged pages, want the router's 3", len(merged))
		}
	})
	t.Run("-format model", func(t *testing.T) {
		_, cfg, root := newFakeRouter(t, okHandler(answer))
		code, out, _ := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "-format", "model", "-as-of", asOfCutoff, "q")
		if code != exitOK || !strings.Contains(out, said+"\n") {
			t.Errorf("exit %d, output:\n%s", code, out)
		}
	})
	t.Run("-format json", func(t *testing.T) {
		_, cfg, root := newFakeRouter(t, okHandler(answer))
		code, out, errOut := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "-as-of", asOfCutoff, "q")
		if code != exitOK || out != answer {
			t.Errorf("exit %d: stdout is not the router's bytes", code)
		}
		if !strings.Contains(errOut, "jevlin: "+said) {
			t.Errorf("stderr %q does not say the cutoff", errOut)
		}
	})
	t.Run("a live search says nothing of it", func(t *testing.T) {
		_, cfg, root := newFakeRouter(t, okHandler(routerBody))
		_, model, _ := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "-format", "model", "q")
		_, _, errOut := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "q")
		if strings.Contains(model, "as of") || errOut != "" {
			t.Errorf("a live search mentions a cutoff: %q / %q", model, errOut)
		}
	})
}

// A dated read echoes its cutoff, and each page that has no saved copy to
// give says why in its own reason. Neither reason is a failure of the call.
func TestADatedReadSaysEachPagesReason(t *testing.T) {
	env, code, _ := fetchServing(t, http.StatusOK, string(routerDatedRead(t)),
		`{"version":1,"pages":[{"url":"https://docs.example.com/api/rate-limits"},{"url":"https://news.example.com/story"},{"url":"https://blog.example.com/post"}],"as_of":"`+asOfCutoff+`"}`)
	result, _ := env["result"].(map[string]any)
	if code != exitOK || env["ok"] != true || result["as_of"] != "2026-03-01T12:00:00Z" {
		t.Fatalf("exit %d envelope %v", code, env)
	}
	pages, _ := result["pages"].([]any)
	want := []struct{ status, reason string }{{"ok", ""}, {"failed", "blocked"}, {"failed", "not_saved_at_as_of"}}
	if len(pages) != len(want) {
		t.Fatalf("%d pages, want %d", len(pages), len(want))
	}
	for i, w := range want {
		p := pages[i].(map[string]any)
		reason, _ := p["reason"].(string)
		if p["status"] != w.status || reason != w.reason {
			t.Errorf("page %d: status %v reason %q, want %s %q", i, p["status"], reason, w.status, w.reason)
		}
	}
}

// Every refusal the router gives a dated search or read keeps the router's
// code and message, under the exit status and action of its HTTP class; the
// 429 says how long to wait, and nothing sends it again.
func TestEveryAsOfRefusalKeepsItsCodeAndClass(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		action    string
		retryable bool
	}{
		{400, "as_of_not_enabled", actionFixInput, false},
		{400, "as_of_invalid", actionFixInput, false},
		{400, "as_of_out_of_range", actionFixInput, false},
		{400, "as_of_with_recency", actionFixInput, false},
		{400, "as_of_conflict", actionFixInput, false},
		{422, "as_of_unsupported_for_selection", actionFixInput, false},
		{403, "as_of_requires_platform_key", actionCheckAccess, false},
		{429, "as_of_rate_limited", actionRetry, true},
	} {
		message := "as_of: a fictional refusal for " + tc.code
		handler := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if tc.status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "2")
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":"` + message + `","code":"` + tc.code + `"}`))
		}
		check := func(t *testing.T, fr *fakeRouter, code int, env map[string]string, msg string, retryAfter any) {
			t.Helper()
			if code != exitClientErr || env["status"] != "client_error" || env["code"] != tc.code ||
				env["action"] != tc.action || env["retryable"] != boolString(tc.retryable) || msg != message {
				t.Errorf("exit %d envelope %v message %q", code, env, msg)
			}
			if tc.status == http.StatusTooManyRequests && retryAfter != float64(2000) {
				t.Errorf("retry_after_ms %v, want 2000", retryAfter)
			}
			if tc.status != http.StatusTooManyRequests && retryAfter != nil {
				t.Errorf("retry_after_ms %v on a %d", retryAfter, tc.status)
			}
			if len(fr.reqs) != 1 {
				t.Errorf("%d requests, want exactly 1: a refusal is never sent again", len(fr.reqs))
			}
		}
		t.Run("search "+tc.code, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, handler)
			code, out, _ := runSearchStdin(t, fixedSearchOps(root), asOfEnv,
				`{"version":1,"query":"q","as_of":"2026-03-01T12:00:00Z"}`, "-config", cfg)
			env := decodeEnvelope(t, out)
			msg, _ := env["error"].(map[string]any)["message"].(string)
			check(t, fr, code, headerStrings(env), msg, env["retry_after_ms"])
		})
		t.Run("fetch "+tc.code, func(t *testing.T) {
			env, code, fr := fetchServingWith(t, handler,
				`{"version":1,"pages":[{"url":"https://a.test/"}],"as_of":"2026-03-01T12:00:00Z"}`)
			msg, _ := env["error"].(map[string]any)["message"].(string)
			check(t, fr, code, headerStrings(env), msg, env["retry_after_ms"])
		})
	}
	t.Run("the human 429 names the wait", func(t *testing.T) {
		fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"as_of: a fictional refusal","code":"as_of_rate_limited"}`))
		})
		code, _, errOut := runSearch(t, fixedSearchOps(root), asOfEnv, "-config", cfg, "-as-of", "2026-03-01T12:00:00Z", "q")
		if code != exitClientErr || !strings.Contains(errOut, "HTTP 429 — retry after 2 seconds") || len(fr.reqs) != 1 {
			t.Errorf("exit %d, %d requests, stderr %q", code, len(fr.reqs), errOut)
		}
	})
	t.Run("the MCP tool's text carries the wait", func(t *testing.T) {
		fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"as_of: a fictional refusal","code":"as_of_rate_limited"}`))
		})
		_, msgs, _ := runMCP(t, asOfEnv, []string{mcpInitialize,
			toolCall(2, "search", `{"query":"q","as_of":"2026-03-01T12:00:00Z"}`)}, "-config", cfg)
		env, isError := toolEnvelope(t, msgs[1])
		if !isError || env["code"] != "as_of_rate_limited" || env["action"] != actionRetry || env["retry_after_ms"] != float64(2000) {
			t.Errorf("isError %v envelope %v", isError, env)
		}
		text := msgs[1]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, `"retry_after_ms":2000`) {
			t.Errorf("the tool's text does not say how long to wait: %s", text)
		}
		if len(fr.reqs) != 1 {
			t.Errorf("%d requests, want exactly 1", len(fr.reqs))
		}
	})
}

// tools/list offers as_of on both tools, as an optional string.
func TestTheToolsListOffersAsOfOnSearchAndFetch(t *testing.T) {
	_, msgs, _ := runMCP(t, asOfEnv, []string{mcpInitialize, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`})
	tools, _ := msgs[1]["result"].(map[string]any)["tools"].([]any)
	seen := 0
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if name != "search" && name != "fetch" {
			continue
		}
		seen++
		schema := tool["inputSchema"].(map[string]any)
		prop, _ := schema["properties"].(map[string]any)["as_of"].(map[string]any)
		desc, _ := prop["description"].(string)
		if prop["type"] != "string" || !strings.Contains(desc, "RFC 3339") {
			t.Errorf("%s's as_of is %v", name, prop)
		}
		for _, r := range schema["required"].([]any) {
			if r == "as_of" {
				t.Errorf("%s requires as_of", name)
			}
		}
	}
	if seen != 2 {
		t.Errorf("found %d of search and fetch in tools/list", seen)
	}
}

// fetchServingWith is fetchServing with the stub's handler given whole, for
// a refusal that carries a header.
func fetchServingWith(t *testing.T, handler http.HandlerFunc, stdin string) (map[string]any, int, *fakeRouter) {
	t.Helper()
	fr, cfg, _ := newFakeRouter(t, handler)
	code, out, _ := runFetchStdin(t, asOfEnv, stdin, "-config", cfg)
	return decodeEnvelope(t, out), code, fr
}

// headerStrings is the envelope's header with each value as text, so one
// comparison reads strings and booleans alike.
func headerStrings(env map[string]any) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"status", "code", "action"} {
		out[k], _ = env[k].(string)
	}
	if b, ok := env["retryable"].(bool); ok {
		out["retryable"] = boolString(b)
	}
	return out
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
