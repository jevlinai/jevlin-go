package main

// Two switches turn the trace off: [search] trace = false in the config and
// JEVLIN_TRACE=off in the environment. traceOff (search.go) is the one place
// that reads both, and every test here drives the same assertions under each
// switch, after a control with both on, so a fixture that never traced cannot
// pass for one that was stopped.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// traceSwitch is one way to turn the trace off: the environment it adds and
// whether the config says trace = false.
type traceSwitch struct {
	name      string
	env       map[string]string
	configOff bool
}

// traceOffInConfig is the config's switch alone.
var traceOffInConfig = traceSwitch{name: "[search] trace = false", configOff: true}

// traceSwitches are the two switches, each alone.
var traceSwitches = []traceSwitch{
	{name: "JEVLIN_TRACE=off", env: map[string]string{"JEVLIN_TRACE": "off"}},
	traceOffInConfig,
}

// envTraceSpellings is the environment's switch in each given spelling.
func envTraceSpellings(values ...string) []traceSwitch {
	out := make([]traceSwitch, 0, len(values))
	for _, v := range values {
		out = append(out, traceSwitch{name: "JEVLIN_TRACE=" + strings.TrimSpace(v), env: map[string]string{"JEVLIN_TRACE": v}})
	}
	return out
}

// withTraceOffInConfig is a format-1 config's bytes with trace = false under
// its [search] table.
func withTraceOffInConfig(t *testing.T, body string) string {
	t.Helper()
	out := strings.Replace(body, "[search]\n", "[search]\ntrace = false\n", 1)
	if out == body {
		t.Fatalf("the fixture has no [search] table:\n%s", body)
	}
	return out
}

func setTraceOffInConfig(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- this test's own config under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(withTraceOffInConfig(t, string(b))), 0o600); err != nil { // #nosec G703 -- the test's own config under t.TempDir
		t.Fatal(err)
	}
}

// newCountingRouter counts the turn ends it receives: requests, not
// connections, since the sender's transport may keep one alive.
func newCountingRouter(t *testing.T, turns *atomic.Int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/turns" {
			turns.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The decision itself: either switch off is off, and both are named.
func TestTraceOffReadsTheConfigAndTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		configOff bool
		env       string
		want      []string
	}{
		{false, "", nil},
		{false, "on", nil},
		{true, "", []string{traceOffByConfig}},
		{false, " OFF ", []string{traceOffByEnvironment}},
		{true, "0", []string{traceOffByConfig, traceOffByEnvironment}},
	} {
		getenv := envOf(map[string]string{"JEVLIN_TRACE": tc.env})
		if got := traceOffBy(tc.configOff, getenv); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("config off %v, JEVLIN_TRACE=%q: off by %v, want %v", tc.configOff, tc.env, got, tc.want)
		}
		if got := traceOff(tc.configOff, getenv); got != (len(tc.want) > 0) {
			t.Errorf("config off %v, JEVLIN_TRACE=%q: traceOff = %v", tc.configOff, tc.env, got)
		}
	}
}

// A search under either switch sends no trace and no session id, in the body
// or the header, and drops a bridge it was handed — what opencode's plugin
// and Pi's extension still build under trace = false, since they read only
// the variable. The control sends all three from the same bridge.
func TestEitherSwitchSendsTheSearchNoTraceAndNoSessionID(t *testing.T) {
	bridge, err := encodeTraceBridge(&traceEnvelope{
		V: traceVersion, Harness: "opencode", SessionID: traceHash("s"), TurnID: traceHash("s|t"),
		History: []traceHistory{{Role: "assistant", Text: "Looking that up now."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, sw traceSwitch) (sent []byte, header string) {
		t.Helper()
		fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(routerBody)) })
		if sw.configOff {
			setTraceOffInConfig(t, cfg)
		}
		env := map[string]string{"JEVLIN_API_KEY": "k", bridgeEnv: bridge, "JEVLIN_HARNESS": "opencode"}
		for k, v := range sw.env {
			env[k] = v
		}
		if code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", cfg, "q"); code != exitOK {
			t.Fatalf("search exited %d: %s", code, errOut)
		}
		req, body := fr.last(t)
		return body, req.Header.Get("X-Session-Id")
	}

	sent, header := run(t, traceSwitch{name: "control"})
	if !bytes.Contains(sent, []byte(`"trace"`)) || !bytes.Contains(sent, []byte(`"session_id"`)) || header != traceHash("s") ||
		!bytes.Contains(sent, []byte("Looking that up now.")) {
		t.Fatalf("the control did not send the bridge's trace and session id: header %q body %s", header, sent)
	}
	for _, sw := range traceSwitches {
		t.Run(sw.name, func(t *testing.T) {
			sent, header := run(t, sw)
			for _, never := range []string{`"trace"`, `"session_id"`, traceHash("s"), "Looking that up now."} {
				if bytes.Contains(sent, []byte(never)) {
					t.Errorf("the search sent %s: %s", never, sent)
				}
			}
			if header != "" {
				t.Errorf("the search sent X-Session-Id %q", header)
			}
		})
	}
}

// No turn end is queued under either switch: for Claude Code's Stop, and for
// a JavaScript host's `hook turn`, whose plugin assembles the turn itself and
// reads only the variable.
func TestEitherSwitchQueuesNoTurnEnd(t *testing.T) {
	turn := traceHash("sess|p1")
	hostTurn := map[string]any{"session": "sess", "turn": "p1", "status": "completed", "user_text": "what you asked", "final_text": "the answer"}
	for _, host := range []struct {
		name string
		run  func(t *testing.T, h *turnEndHarness)
	}{
		{"Claude Code's Stop", func(t *testing.T, h *turnEndHarness) { runHook(t, h.ops, h.hc, "stop", claudeStop(nil)) }},
		{"opencode's hook turn", func(t *testing.T, h *turnEndHarness) { runHook(t, h.ops, h.hc, "turn opencode", hostTurn) }},
	} {
		t.Run(host.name, func(t *testing.T) {
			control := newTurnEndHarness(nil, true)
			control.served(turn)
			host.run(t, control)
			if len(control.queued) != 1 {
				t.Fatalf("the control queued %d turn end(s), want 1", len(control.queued))
			}
			for _, sw := range traceSwitches {
				h := newTurnEndHarness(sw.env, true)
				h.hc.traceOffInConfig = sw.configOff
				h.served(turn)
				host.run(t, h)
				if len(h.queued) != 0 {
					t.Errorf("%s: queued %v", sw.name, h.queued)
				}
				for path := range h.fs.files {
					if strings.HasSuffix(path, turnEndSuffix) {
						t.Errorf("%s: a record was written: %s", sw.name, path)
					}
				}
			}
		})
	}
}

// The config's switch reaches the hooks through the real entry, which reads
// the config from disk: the Stop hook asks for no sender, and Hermes' hook
// writes no bridge. The variable, through the same entry, does the same.
func TestTheConfigSwitchReachesTheHooksThroughTheRealEntry(t *testing.T) {
	turn := traceHash("sess|p1")
	hermes := mustJSON(t, map[string]any{
		"hook_event_name": "pre_tool_call", "tool_name": "terminal",
		"tool_input": map[string]any{"command": `jevlin search -format model "q"`, "timeout": 30},
		"session_id": "sess-1", "cwd": "/w", "extra": map[string]any{"tool_call_id": "c1", "turn_id": "t1"},
	})
	runHermes := func(t *testing.T, cfg string, env map[string]string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := cmdHook([]string{"-config", cfg, "hermes", "pre_tool_call"}, bytes.NewReader(hermes), &out, &errOut, envOf(env)); code != exitOK {
			t.Fatalf("hook exited %d: %s", code, errOut.String())
		}
		return out.String()
	}

	control := newTurnEndInstallation(t, true)
	control.served(t, turn)
	asks := watchSpawns(t)
	stopHook(t, control, claudeStop(nil))
	if len(*asks) != 1 || !strings.Contains(runHermes(t, control.cfg, nil), bridgeEnv) {
		t.Fatalf("the control did not trace: %d sender(s)", len(*asks))
	}

	for _, sw := range traceSwitches {
		t.Run(sw.name, func(t *testing.T) {
			in := newTurnEndInstallation(t, true)
			in.served(t, turn) // before the switch: a turn searched while the trace was on
			if sw.configOff {
				setTraceOffInConfig(t, in.cfg)
			}
			asks := watchSpawns(t)
			b := mustJSON(t, claudeStop(nil))
			var out, errOut bytes.Buffer
			if code := cmdHook([]string{"-config", in.cfg, "stop"}, bytes.NewReader(b), &out, &errOut, envOf(sw.env)); code != exitOK {
				t.Fatalf("hook stop exited %d: %s", code, errOut.String())
			}
			if len(*asks) != 0 || len(in.queuedTurnEnds(t)) != 0 {
				t.Errorf("the Stop hook queued a turn end: %+v", *asks)
			}
			if got := runHermes(t, in.cfg, sw.env); got != "" {
				t.Errorf("Hermes' hook answered %q", got)
			}
		})
	}
}

// The detached sender holds the switch as well: a turn end queued while the
// trace was on is not sent once either switch is off, and its file is still
// removed. The control sends the same file.
func TestTheSenderSendsNothingUnderEitherSwitch(t *testing.T) {
	f := newTurnEndFixture(t)
	var turns atomic.Int64
	router := newCountingRouter(t, &turns)
	b, err := os.ReadFile(f.ownCfg) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	on := strings.ReplaceAll(string(b), f.ownURL, router)
	write := func(body string) {
		if err := os.WriteFile(f.ownCfg, []byte(body), 0o600); err != nil { // #nosec G703 -- the fixture's own config under t.TempDir
			t.Fatal(err)
		}
	}
	base := map[string]string{apiKeyEnv: boundKey, "JEVLIN_CONFIG": f.ownCfg}
	write(on)
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", f.queue()}, envOf(base)); code != exitOK || turns.Load() != 1 {
		t.Fatalf("control: exit %d, %d turn end(s) sent", code, turns.Load())
	}
	for _, sw := range traceSwitches {
		t.Run(sw.name, func(t *testing.T) {
			write(on)
			if sw.configOff {
				write(withTraceOffInConfig(t, on))
			}
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for k, v := range sw.env {
				env[k] = v
			}
			before := turns.Load()
			queued := f.queue()
			if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", queued}, envOf(env)); code != exitOK {
				t.Fatalf("exit %d", code)
			}
			if n := turns.Load() - before; n != 0 {
				t.Errorf("a turn end was sent: %d request(s)", n)
			}
			if lexists(queued) {
				t.Errorf("the queued file was left: %s", queued)
			}
		})
	}
}

// status shows both switches, and why each is off, in text and in JSON.
func TestStatusShowsTheTraceAndTheTurnEnd(t *testing.T) {
	_, cfg, _ := newFakeRouter(t, nil)
	b, err := os.ReadFile(cfg) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	withTurnEnd := strings.Replace(string(b), "[search]\n", "[search]\nturn_end = true\n", 1)
	for _, tc := range []struct {
		name, body string
		env        map[string]string
		trace      string
		turnEnd    string
		jsonTrace  statusSwitchJSON
		jsonTurn   statusSwitchJSON
	}{
		{"both on", withTurnEnd, nil, "on", "on",
			statusSwitchJSON{On: true}, statusSwitchJSON{On: true}},
		{"no turn_end line", string(b), nil, "on", "off (turn_end is not on in the config)",
			statusSwitchJSON{On: true}, statusSwitchJSON{OffBy: []string{"config"}}},
		{"trace = false", withTraceOffInConfig(t, withTurnEnd), nil, "off (trace = false in the config)", "off (the trace is off)",
			statusSwitchJSON{OffBy: []string{"config"}}, statusSwitchJSON{OffBy: []string{"trace"}}},
		{"JEVLIN_TRACE=off", withTurnEnd, map[string]string{"JEVLIN_TRACE": "off"}, "off (JEVLIN_TRACE in this environment)", "off (the trace is off)",
			statusSwitchJSON{OffBy: []string{"environment"}}, statusSwitchJSON{OffBy: []string{"trace"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(cfg, []byte(tc.body), 0o600); err != nil { // #nosec G703 -- the test's own config under t.TempDir
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			statusMain([]string{"-config", cfg}, &stdout, &stderr, envOf(tc.env))
			for _, want := range []string{"trace:   " + tc.trace + "\n", "turn end: " + tc.turnEnd + "\n"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("status does not say %q:\n%s", want, stdout.String())
				}
			}
			stdout.Reset()
			statusMain([]string{"-config", cfg, "-json"}, &stdout, &stderr, envOf(tc.env))
			var env struct {
				Data struct {
					Trace   statusSwitchJSON `json:"trace"`
					TurnEnd statusSwitchJSON `json:"turn_end"`
				} `json:"data"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
				t.Fatalf("status -json: %v\n%s", err, stdout.String())
			}
			if !reflect.DeepEqual(env.Data.Trace, tc.jsonTrace) || !reflect.DeepEqual(env.Data.TurnEnd, tc.jsonTurn) {
				t.Errorf("status -json trace %+v turn_end %+v, want %+v %+v", env.Data.Trace, env.Data.TurnEnd, tc.jsonTrace, tc.jsonTurn)
			}
		})
	}
}

// sharingNoticeFor is the notice setup prints for a config at cfgPath whose
// trace and turn end are both on.
func sharingNoticeFor(cfgPath string) string {
	return "\nSharing with the Jevlin router\n" +
		"Each search sends a trace: hashed session ids and the assistant's text that\n" +
		"led to the search, from agents that provide it.\n" +
		"When a turn that searched ends, jevlin also sends what you asked, the\n" +
		"assistant's text and its final answer, and the name, duration and outcome of\n" +
		"each tool it ran, never a tool's input or output.\n" +
		"Anything shaped like a key or token, e-mail addresses, and your account name\n" +
		"in paths are removed before anything leaves this machine.\n" +
		"The router keeps it for 30 days.\n" +
		"To stop sending whole turns: set turn_end = false under [search] in " + cfgPath + ".\n" +
		"To stop sending both: set trace = false under [search] in " + cfgPath + ", or JEVLIN_TRACE=off.\n"
}

// Setup says what is sent, and both ways to stop it, without asking: a fresh
// installation's notice is the same with no terminal and with -yes. For a
// config it leaves as it was, the notice says only what is true of that
// config: no turn end where turn_end is not on, and nothing sent where the
// trace is off by either switch.
func TestSetupSaysWhatIsSent(t *testing.T) {
	for name, args := range map[string][]string{"no terminal": nil, "-yes": {"-yes"}} {
		t.Run("fresh, "+name, func(t *testing.T) {
			s := newSetupSandbox(t)
			s.platform.claim("credits")
			code, out, errOut := s.run(nil, false, args...)
			if code != exitOK {
				t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			if !strings.Contains(out, sharingNoticeFor(s.cfgPath())) {
				t.Errorf("setup did not print the notice:\n%s", out)
			}
		})
	}

	existing := func(t *testing.T, body func(fresh string) string, env map[string]string) string {
		t.Helper()
		s := newSetupSandbox(t)
		s.platform.claim("credits")
		for k, v := range env {
			s.env[k] = v
		}
		if err := os.MkdirAll(s.home, 0o700); err != nil {
			t.Fatal(err)
		}
		fresh, err := renderFreshConfig(resolveSetupValues(s.home, s.getenv), false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.cfgPath(), []byte(body(string(fresh))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := s.run(nil, false, "-yes")
		if code != exitOK {
			t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		section := out[strings.Index(out, "Sharing with the Jevlin router"):]
		return section[:strings.Index(section, "\nConnecting\n")] + "\n"
	}
	t.Run("a config without the line", func(t *testing.T) {
		got := existing(t, func(fresh string) string { return fresh }, nil)
		if strings.Contains(got, "When a turn that searched ends") || !strings.Contains(got, "Whole turns are not sent: turn_end is not on under [search] in ") ||
			!strings.Contains(got, "To stop sending the trace: set trace = false under [search] in ") || !strings.Contains(got, "Each search sends a trace") {
			t.Errorf("the notice for a config without turn_end:\n%s", got)
		}
	})
	for _, sw := range traceSwitches {
		t.Run(sw.name, func(t *testing.T) {
			body := func(fresh string) string {
				fresh = strings.Replace(fresh, "[search]\n", "[search]\nturn_end = true\n", 1)
				if sw.configOff {
					return withTraceOffInConfig(t, fresh)
				}
				return fresh
			}
			got := existing(t, body, sw.env)
			want := map[bool]string{true: "trace = false in the config", false: "JEVLIN_TRACE in this environment"}[sw.configOff]
			if !strings.Contains(got, "The trace is off ("+want+"), so searches send no trace and no session id,\nand no turn is sent when it ends.\n") ||
				strings.Contains(got, "Each search sends a trace") || strings.Contains(got, "When a turn that searched ends") {
				t.Errorf("the notice with the trace off:\n%s", got)
			}
		})
	}
}
