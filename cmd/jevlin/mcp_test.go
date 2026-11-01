package main

// `jevlin mcp` driven the way a host drives it: scripted lines on stdin,
// every line of stdout read back as JSON-RPC, against a fake router on
// loopback. Every identifier is synthetic.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fictional-host","version":"0.0.1"}}}`

// runMCP feeds lines to the server and returns its exit code, each stdout
// line decoded, and stderr. It fails the test on a stdout line that is not
// a JSON-RPC 2.0 object.
func runMCP(t *testing.T, env map[string]string, lines []string, args ...string) (int, []map[string]any, string) {
	t.Helper()
	h := fixedSearchOps(t.TempDir())
	var out, errOut strings.Builder
	in := strings.Join(lines, "\n")
	if len(lines) > 0 {
		in += "\n"
	}
	now := func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }
	code := mcpMain(h.ops, now, args, strings.NewReader(in), &out, &errOut, installedAs(envOf(env), args))
	return code, decodeRPCLines(t, out.String()), errOut.String()
}

func decodeRPCLines(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	if stdout == "" {
		return nil
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout does not end in a newline: %q", stdout)
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout carries a line that is not JSON: %q", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("stdout carries a line that is not JSON-RPC 2.0: %q", line)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func rpcErrorCode(t *testing.T, m map[string]any) float64 {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("not an error response: %v", m)
	}
	return e["code"].(float64)
}

// toolEnvelope is a tools/call result's one text item, decoded as the
// machine envelope it carries, and its isError.
func toolEnvelope(t *testing.T, m map[string]any) (map[string]any, bool) {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("not a result: %v", m)
	}
	content := r["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("the result carries %d content items, want 1: %v", len(content), r)
	}
	item := content[0].(map[string]any)
	if item["type"] != "text" {
		t.Fatalf("the content item is %v, want text", item["type"])
	}
	env := decodeEnvelope(t, item["text"].(string)+"\n")
	if sc, has := r["structuredContent"]; has && !reflect.DeepEqual(sc, env) {
		t.Errorf("structuredContent %v is not the text's envelope %v", sc, env)
	}
	return env, r["isError"].(bool)
}

func toolCall(id int, name, args string) string {
	b, _ := json.Marshal(id)
	return `{"jsonrpc":"2.0","id":` + string(b) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

// The whole handshake a host makes, then a search: the search reaches the
// router as the machine path would send it, labeled with the client's name,
// and comes back as the machine envelope.
func TestAnMCPHostSearchesThroughTheWholeHandshake(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, okHandler(routerBody))
	code, msgs, _ := runMCP(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, []string{
		mcpInitialize,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		toolCall(3, "search", `{"query":"how do ports work","recency":"week"}`),
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	}, "-config", cfg)
	if code != exitOK {
		t.Errorf("exit %d at end of input, want 0", code)
	}
	if len(msgs) != 4 {
		t.Fatalf("%d responses, want 4 (the notification is not answered): %v", len(msgs), msgs)
	}
	for i, want := range []float64{1, 2, 3, 4} {
		if msgs[i]["id"] != want {
			t.Errorf("response %d has id %v, want %v: answered out of order", i, msgs[i]["id"], want)
		}
	}

	init := msgs[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion %v, want the client's 2025-06-18", init["protocolVersion"])
	}
	info := init["serverInfo"].(map[string]any)
	if info["name"] != "jevlin" || info["version"] != buildVersion() {
		t.Errorf("serverInfo %v, want jevlin at %s", info, buildVersion())
	}
	if caps := init["capabilities"].(map[string]any); len(caps) != 1 || caps["tools"] == nil {
		t.Errorf("capabilities %v, want tools only", caps)
	}

	var names []string
	for _, tool := range msgs[1]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if !reflect.DeepEqual(names, []string{"search", "fetch"}) {
		t.Errorf("tools %v, want search and fetch", names)
	}

	env, isError := toolEnvelope(t, msgs[2])
	if isError || env["ok"] != true || env["command"] != "search" {
		t.Errorf("the search came back isError=%v, envelope %v", isError, env)
	}
	if _, has := msgs[2]["result"].(map[string]any)["structuredContent"]; !has {
		t.Error("no structuredContent under 2025-06-18")
	}
	req, body := fr.last(t)
	if req.URL.Path != "/v1/search" {
		t.Errorf("the router was asked %s", req.URL.Path)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["query"] != "how do ports work" || sent["recency"] != "week" || sent["view"] != "merged" {
		t.Errorf("the router received %s", body)
	}
	if h := sent["trace"].(map[string]any)["harness"]; h != "mcp:fictional-host" {
		t.Errorf("the trace's harness is %v, want mcp:fictional-host", h)
	}
	if msgs[3]["result"] == nil {
		t.Errorf("ping got %v", msgs[3])
	}
}

// The version a client asks for is answered with itself when this server
// speaks it, and with the newest this server speaks when it does not.
// structuredContent travels only from the revision that defined it.
func TestTheServerNegotiatesTheProtocolVersion(t *testing.T) {
	for _, tc := range []struct{ asked, want string }{
		{"2025-11-25", "2025-11-25"},
		{"2025-06-18", "2025-06-18"},
		{"2025-03-26", "2025-03-26"},
		{"2024-11-05", "2024-11-05"},
		{"1999-01-01", mcpProtocolVersions[0]},
		{"2099-12-31", mcpProtocolVersions[0]},
	} {
		t.Run(tc.asked, func(t *testing.T) {
			_, msgs, _ := runMCP(t, nil, []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + tc.asked + `","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`,
				toolCall(2, "search", `{}`),
			})
			if got := msgs[0]["result"].(map[string]any)["protocolVersion"]; got != tc.want {
				t.Errorf("asked %s, answered %v, want %s", tc.asked, got, tc.want)
			}
			_, has := msgs[1]["result"].(map[string]any)["structuredContent"]
			if want := tc.want >= mcpStructuredContentSince; has != want {
				t.Errorf("structuredContent present %v under %s, want %v", has, tc.want, want)
			}
		})
	}
	_, msgs, _ := runMCP(t, nil, []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`})
	if rpcErrorCode(t, msgs[0]) != rpcInvalidParams {
		t.Errorf("an initialize with no protocolVersion got %v", msgs[0])
	}
}

// Each malformed message gets JSON-RPC's own answer, and the server goes on
// to the next line.
func TestTheServerAnswersMalformedMessagesAndGoesOn(t *testing.T) {
	oversized := `{"jsonrpc":"2.0","id":9,"method":"ping","params":{"pad":"` + strings.Repeat("a", machineStdinMax) + `"}}`
	for _, tc := range []struct {
		name, line string
		code       float64
		id         any
	}{
		{"unknown method", `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`, rpcMethodNotFound, 7.0},
		{"malformed JSON", `{"jsonrpc":"2.0","id":7,"method":`, rpcParseError, nil},
		{"oversized line", oversized, rpcInvalidRequest, nil},
		{"batch", `[{"jsonrpc":"2.0","id":7,"method":"ping"}]`, rpcInvalidRequest, nil},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, rpcInvalidRequest, nil},
		{"wrong jsonrpc", `{"jsonrpc":"1.0","id":7,"method":"ping"}`, rpcInvalidRequest, 7.0},
		{"no method", `{"jsonrpc":"2.0","id":7}`, rpcInvalidRequest, 7.0},
		{"method not a string", `{"jsonrpc":"2.0","id":7,"method":1}`, rpcInvalidRequest, 7.0},
		{"method null", `{"jsonrpc":"2.0","id":7,"method":null}`, rpcInvalidRequest, 7.0},
		{"jsonrpc not a string", `{"jsonrpc":2,"id":7,"method":"ping"}`, rpcInvalidRequest, 7.0},
		{"no jsonrpc", `{"id":7,"method":"ping"}`, rpcInvalidRequest, 7.0},
		{"a bare string", `"hello"`, rpcInvalidRequest, nil},
		{"a bare number", `42`, rpcInvalidRequest, nil},
		{"a bare null", `null`, rpcInvalidRequest, nil},
		{"id a boolean", `{"jsonrpc":"2.0","id":true,"method":"ping"}`, rpcInvalidRequest, nil},
		{"unknown tool", toolCall(7, "browse", `{}`), rpcInvalidParams, 7.0},
		{"arguments not an object", toolCall(7, "search", `["a"]`), rpcInvalidParams, 7.0},
		{"tools/call without a name", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}`, rpcInvalidParams, 7.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, msgs, _ := runMCP(t, nil, []string{tc.line, `{"jsonrpc":"2.0","id":"next","method":"ping"}`})
			if code != exitOK {
				t.Errorf("exit %d", code)
			}
			if len(msgs) != 2 {
				t.Fatalf("%d responses, want the error and the next ping's: %v", len(msgs), msgs)
			}
			if got := rpcErrorCode(t, msgs[0]); got != tc.code {
				t.Errorf("error %v, want %v: %v", got, tc.code, msgs[0])
			}
			if msgs[0]["id"] != tc.id {
				t.Errorf("id %v, want %v", msgs[0]["id"], tc.id)
			}
			if msgs[1]["id"] != "next" || msgs[1]["result"] == nil {
				t.Errorf("the line after it was not served: %v", msgs[1])
			}
		})
	}
	// A notification is never answered, an unknown one or a cancellation
	// included.
	_, msgs, _ := runMCP(t, nil, []string{
		`{"jsonrpc":"2.0","method":"` + mcpCancelled + `","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/fictional"}`,
	})
	if len(msgs) != 0 {
		t.Errorf("notifications were answered: %v", msgs)
	}
}

// A tool call the machine path refuses is a tool result carrying the
// machine path's own envelope, isError set, and the router never hears of
// it.
func TestAToolCallOutsideTheContractNeverReachesTheRouter(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, okHandler(routerBody))
	for _, tc := range []struct{ tool, args, code string }{
		{"search", `{"query":"x","recency":"decade"}`, codeInvalidRecency},
		{"search", `{"query":"x","trace":{"harness":"forged"}}`, codeUnknownField},
		{"search", `{}`, codeMissingQuery},
		{"search", `{"query":"  "}`, codeEmptyQuery},
		{"search", `{"query":"x","page_chars":1}`, codeInvalidPageChars},
		{"search", `{"version":2,"query":"x"}`, codeUnsupportedVersion},
		{"search", `{"Version":2,"query":"x"}`, codeUnsupportedVersion},
		{"search", `{"VERSION":"1","query":"x"}`, codeUnsupportedVersion},
		{"fetch", `{"pages":[{"source_id":"s_0123"}]}`, codeInvalidSourceID},
		{"fetch", `{}`, codeMissingPages},
	} {
		t.Run(tc.tool+"/"+tc.code, func(t *testing.T) {
			_, msgs, _ := runMCP(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"},
				[]string{mcpInitialize, toolCall(2, tc.tool, tc.args)}, "-config", cfg)
			if _, isRPCError := msgs[1]["error"]; isRPCError {
				t.Fatalf("a refused argument is a JSON-RPC error, not a tool result: %v", msgs[1])
			}
			env, isError := toolEnvelope(t, msgs[1])
			if !isError || env["ok"] != false || env["code"] != tc.code || env["action"] != actionFixInput {
				t.Errorf("isError=%v envelope %v, want %s / fix_input", isError, env, tc.code)
			}
		})
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.reqs) != 0 {
		t.Errorf("the router was called %d times for requests the contract refuses", len(fr.reqs))
	}
}

// Before setup the server still starts, says what it is and lists its
// tools; a tool call answers with the machine path's guidance and the
// server goes on.
func TestAToolCallBeforeSetupAnswersWithTheGuidance(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"JEVLIN_HOME": home, "HOME": home}
	code, msgs, _ := runMCP(t, env, []string{
		mcpInitialize,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		toolCall(3, "search", `{"query":"how do ports work"}`),
		toolCall(4, "fetch", `{"pages":[{"url":"https://a.test/"}]}`),
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
	})
	if code != exitOK || len(msgs) != 5 {
		t.Fatalf("exit %d with %d responses, want 0 and 5: %v", code, len(msgs), msgs)
	}
	if msgs[0]["result"] == nil || msgs[1]["result"] == nil {
		t.Errorf("initialize or tools/list was not answered before setup: %v", msgs[:2])
	}
	for _, m := range msgs[2:4] {
		env, isError := toolEnvelope(t, m)
		msg, _ := env["error"].(map[string]any)["message"].(string)
		if !isError || env["action"] != actionConnect || !strings.Contains(msg, "run jevlin connect") {
			t.Errorf("isError=%v envelope %v, want the connect guidance", isError, env)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("a tool call before setup left %d entries in the home", len(entries))
	}
}

// stdout is the protocol and nothing else, with the log on and through
// every kind of answer; the log names methods and outcomes, never what a
// request carried.
func TestStdoutCarriesOnlyJSONRPCWithTheLogOn(t *testing.T) {
	_, cfg, _ := newFakeRouter(t, okHandler(routerBody))
	const secretQuery = "fictional-query-words-that-must-not-be-logged"
	in := []string{
		mcpInitialize,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		toolCall(3, "search", `{"query":"`+secretQuery+`"}`),
		toolCall(4, "search", `{"query":"`+secretQuery+`","recency":"decade"}`),
		toolCall(5, "fetch", `{"pages":[{"url":"https://a.test/"}],"query":"`+secretQuery+`"}`),
		`not json`,
		`{"jsonrpc":"2.0","id":6,"method":"` + secretQuery + `"}`,
		`{"jsonrpc":"2.0","id":7,"method":"bad\u001b[2Jmethod"}`,
	}
	h := fixedSearchOps(t.TempDir())
	var out, errOut strings.Builder
	args := []string{"-log", "-config", cfg}
	code := mcpMain(h.ops, time.Now, args, strings.NewReader(strings.Join(in, "\n")+"\n"), &out, &errOut,
		installedAs(envOf(map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}), args))
	if code != exitOK {
		t.Errorf("exit %d", code)
	}
	if got := len(decodeRPCLines(t, out.String())); got != 8 {
		t.Errorf("%d stdout lines, want one per request (8)", got)
	}
	logged := errOut.String()
	if !strings.Contains(logged, "tools/call search") {
		t.Errorf("the log is on and says nothing of the search:\n%s", logged)
	}
	for _, leak := range []string{secretQuery, "sr-fictional-key", "\x1b"} {
		if strings.Contains(logged, leak) {
			t.Errorf("the log repeats %q:\n%s", leak, logged)
		}
	}
}

// The schemas are the machine contract: the property names are exactly the
// request's json tags, so neither can gain a field the other lacks.
func TestTheToolSchemasAreTheMachineContract(t *testing.T) {
	tags := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
		}
		slices.Sort(out)
		return out
	}
	props := func(schema map[string]any) []string {
		var out []string
		for k := range schema["properties"].(map[string]any) {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	tools := map[string]map[string]any{}
	for _, tool := range mcpTools() {
		tools[tool.Name] = tool.InputSchema
		if tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s's schema admits fields the machine path refuses", tool.Name)
		}
	}
	if got, want := props(tools["search"]), tags(wireSearchRequest{}); !reflect.DeepEqual(got, want) {
		t.Errorf("search's schema has %v, the request %v", got, want)
	}
	if got, want := props(tools["fetch"]), tags(wireFetchRequest{}); !reflect.DeepEqual(got, want) {
		t.Errorf("fetch's schema has %v, the request %v", got, want)
	}
	page := tools["fetch"]["properties"].(map[string]any)["pages"].(map[string]any)["items"].(map[string]any)
	if got, want := props(page), tags(wireFetchPage{}); !reflect.DeepEqual(got, want) {
		t.Errorf("fetch's page schema has %v, the request %v", got, want)
	}
	// And every schema is JSON a client can read.
	if _, err := json.Marshal(mcpTools()); err != nil {
		t.Fatal(err)
	}
}

// The tools' text is the workflow: it names no tier and no price, and tells
// the agent the user's tier applies when it leaves tier out.
func TestTheToolTextNamesNoTierAndNoPrice(t *testing.T) {
	b, err := json.Marshal(mcpTools())
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && k == "description" {
					texts = append(texts, s)
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	walk(decoded)
	forbidden := regexp.MustCompile(`(?i)\b(fast|economy|standard|balanced|max|cheapest|cheaper|costs?)\b|\$\s?[0-9]`)
	for _, s := range texts {
		if m := forbidden.FindString(s); m != "" {
			t.Errorf("a description names %q: %s", m, s)
		}
	}
	if all := strings.Join(texts, " "); !strings.Contains(all, "the user's own tier applies") {
		t.Error("no description says the user's own tier applies when tier is left out")
	}
}

// End of input ends the server with 0, whether or not the last line ended
// in a newline; the last message is still answered.
func TestTheServerExitsCleanlyAtEndOfInput(t *testing.T) {
	h := fixedSearchOps(t.TempDir())
	for _, in := range []string{"", "\n\n", `{"jsonrpc":"2.0","id":1,"method":"ping"}`} {
		var out, errOut strings.Builder
		code := mcpMain(h.ops, time.Now, nil, strings.NewReader(in), &out, &errOut, envOf(nil))
		if code != exitOK {
			t.Errorf("input %q: exit %d, want 0", in, code)
		}
		if want := strings.Contains(in, "ping"); (out.Len() > 0) != want {
			t.Errorf("input %q: stdout %q", in, out.String())
		}
	}
	var out strings.Builder
	if code := mcpMain(h.ops, time.Now, []string{"extra"}, strings.NewReader(""), &out, io.Discard, envOf(nil)); code != exitUsage || out.Len() != 0 {
		t.Errorf("an argument: exit %d, stdout %q; want 2 and nothing", code, out.String())
	}
}

// The client's own name rides as the harness only when it is a token, and
// never as a name a hook writes.
func TestTheClientNameIsAHarnessOnlyAsAToken(t *testing.T) {
	for name, want := range map[string]string{
		"claude-code":                 "mcp:claude-code",
		"cursor-vscode":               "mcp:cursor-vscode",
		"":                            "mcp",
		"two words":                   "mcp",
		"bad\x1bname":                 "mcp",
		strings.Repeat("a", 61):       "mcp",
		strings.Repeat("a", 60):       "mcp:" + strings.Repeat("a", 60),
		"../../etc/passwd":            "mcp",
		"fictional_host.v2-preview_1": "mcp:fictional_host.v2-preview_1",
	} {
		b, _ := json.Marshal(name)
		s := &mcpServer{out: io.Discard, logw: io.Discard, getenv: envOf(nil)}
		if _, rerr := s.initialize(json.RawMessage(`{"protocolVersion":"2025-06-18","clientInfo":{"name":` + string(b) + `}}`)); rerr != nil {
			t.Fatal(rerr)
		}
		if s.harness != want || len(s.harness) > 64 {
			t.Errorf("client name %q: harness %q, want %q", name, s.harness, want)
		}
		if got := s.env("JEVLIN_HARNESS"); got != want {
			t.Errorf("client name %q: JEVLIN_HARNESS reads %q", name, got)
		}
	}
	// The host's own declaration wins.
	s := &mcpServer{getenv: envOf(map[string]string{"JEVLIN_HARNESS": "cursor"}), harness: "mcp:x"}
	if got := s.env("JEVLIN_HARNESS"); got != "cursor" {
		t.Errorf("a declared harness was replaced by %q", got)
	}
}

// okHandler answers every request with 200 and body.
func okHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// The server's -config is the config every tool call reads, and without
// one the environment's config is. Driven with the environment exactly as
// given (no installedAs, which would name -config's file in JEVLIN_CONFIG
// and hide a call that dropped the flag): the installation's own config and
// the one named elsewhere share the fake router and differ in their tier,
// which the router receives.
func TestEveryToolCallReadsTheConfigTheServerWasGiven(t *testing.T) {
	fr, own, root := newFakeRouter(t, okHandler(routerBody))
	raw, err := os.ReadFile(own) // #nosec G304 -- the config this test's fake router wrote
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other.toml")
	if err := os.WriteFile(other, append(raw, []byte("tier = \"balanced\"\n")...), 0o600); err != nil { // #nosec G703 -- a path in this test's own temp dir
		t.Fatal(err)
	}
	base := map[string]string{"JEVLIN_HOME": root, "HOME": root, "JEVLIN_API_KEY": "sr-fictional-key"}
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"named by -config", base, []string{"-config", other}},
		{"named by JEVLIN_CONFIG", map[string]string{"JEVLIN_HOME": root, "HOME": root, "JEVLIN_API_KEY": "sr-fictional-key", "JEVLIN_CONFIG": other}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixedSearchOps(t.TempDir())
			var out, errOut strings.Builder
			in := mcpInitialize + "\n" + toolCall(2, "search", `{"query":"which config"}`) + "\n"
			mcpMain(h.ops, time.Now, tc.args, strings.NewReader(in), &out, &errOut, envOf(tc.env))
			msgs := decodeRPCLines(t, out.String())
			if env, isError := toolEnvelope(t, msgs[1]); isError {
				t.Fatalf("the search failed: %v", env)
			}
			_, body := fr.last(t)
			var sent map[string]any
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			if sent["tier"] != "balanced" {
				t.Errorf("the router received tier %v: the call read another config than %s", sent["tier"], other)
			}
		})
	}
}
