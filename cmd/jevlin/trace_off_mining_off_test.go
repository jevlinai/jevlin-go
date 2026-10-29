package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

func traceWithHistory() string { return traceWithHistoryAs("claude-code") }

// traceWithHistoryAs is a bridge envelope with assistant text — a
// trajectory — under the given harness, for the tests that need a search
// to mine.
func traceWithHistoryAs(harness string) string {
	bridge, _ := encodeTraceBridge(&traceEnvelope{V: traceVersion, Harness: harness, SessionID: "abc", TurnID: "t", CallID: "c",
		History: []traceHistory{{Role: "assistant", Text: "the error mentions ECONNRESET; check undici's retry defaults"}}})
	return bridge
}

func traceIDsOnly() string {
	bridge, _ := encodeTraceBridge(&traceEnvelope{V: traceVersion, Harness: "codex", SessionID: "abc", TurnID: "t", CallID: "c"})
	return bridge
}

// The mining declaration rides only beside a trajectory with text, and only
// when this installation is mining. Never "off": absence is the undeclared
// state.
func TestMiningHeaderRidesOnlyBesideATrajectory(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		mining   bool
		wantHdr  bool
		wantTraj bool
	}{
		{"trajectory and mining on", map[string]string{bridgeEnv: traceWithHistory()}, true, true, true},
		{"trajectory, mining off", map[string]string{bridgeEnv: traceWithHistory()}, false, false, true},
		{"ids alone are not a trajectory", map[string]string{bridgeEnv: traceIDsOnly()}, true, false, false},
		{"trace off", map[string]string{"JEVLIN_TRACE": "off"}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
			if !tc.mining {
				store, err := auth.OpenStore(filepath.Join(root, "state"))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SaveMiningEnabled(false); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{"JEVLIN_API_KEY": "k"}
			for k, v := range tc.env {
				env[k] = v
			}
			h := fixedSearchOps(root)
			code, out, errOut := runSearchStdin(t, h, env, `{"version":1,"query":"undici retry defaults"}`, "-config", cfg)
			if code != exitOK {
				t.Fatalf("exit %d: %s\n%s", code, errOut, out)
			}
			req, _ := fr.last(t)
			if got := req.Header.Get("X-Jevlin-Mining") == "on"; got != tc.wantHdr {
				t.Errorf("X-Jevlin-Mining sent %v, want %v (value %q)", got, tc.wantHdr, req.Header.Get("X-Jevlin-Mining"))
			}
			if v := req.Header.Get("X-Jevlin-Mining"); v != "" && v != "on" {
				t.Errorf("the header is only ever %q, got %q", "on", v)
			}
		})
	}
}

// The machine envelope says whether a trajectory rode, and a search with
// none spools nothing and says why — distinctly from a capture failure.
func TestNoTrajectoryMeansNothingSpooledAndTheEnvelopeSaysSo(t *testing.T) {
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	_ = fr
	h := fixedSearchOps(root)
	code, out, errOut := runSearchStdin(t, h, map[string]string{"JEVLIN_API_KEY": "k", bridgeEnv: traceIDsOnly()},
		`{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, errOut, out)
	}
	var env struct {
		Mining struct {
			State      string `json:"state"`
			Recorded   bool   `json:"recorded"`
			Trajectory bool   `json:"trajectory"`
			Detail     string `json:"detail"`
			Health     []any  `json:"health"`
		} `json:"mining"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	if env.Mining.State != string(auth.MiningEnabled) || env.Mining.Trajectory || env.Mining.Recorded {
		t.Errorf("mining on, no trajectory: state %q trajectory %v recorded %v", env.Mining.State, env.Mining.Trajectory, env.Mining.Recorded)
	}
	if !strings.Contains(env.Mining.Detail, "no trajectory") || len(env.Mining.Health) != 0 {
		t.Errorf("the reason must be the trajectory, not a capture failure: detail %q health %v", env.Mining.Detail, env.Mining.Health)
	}
	if errOut != "" && strings.Contains(errOut, "JEVLIN") {
		t.Errorf("the machine form explains nothing to its reader: %q", errOut)
	}
	if recs, _ := filepath.Glob(filepath.Join(root, "spool", "intake", "*")); len(recs) != 0 {
		t.Errorf("intake was spooled for a search with no trajectory: %v", recs)
	}

	with := fixedSearchOps(root)
	_, out, _ = runSearchStdin(t, with, map[string]string{"JEVLIN_API_KEY": "k", bridgeEnv: traceWithHistory()},
		`{"version":1,"query":"q"}`, "-config", cfg)
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Mining.Trajectory || !env.Mining.Recorded {
		t.Errorf("with a trajectory: trajectory %v recorded %v", env.Mining.Trajectory, env.Mining.Recorded)
	}
}

// The router's price band is shown as it arrives.
func TestBillingIsRenderedAsItArrives(t *testing.T) {
	r := &routerResponse{Usage: &routerUsage{CostMicros: 4000, Billing: "credit", BillingReason: "no_trajectory"}}
	line := renderDecisionLine(*r)
	if !strings.Contains(line, "billing: credit (no_trajectory)") {
		t.Errorf("decision line: %q", line)
	}
	r.Usage.Billing, r.Usage.BillingReason = "free", ""
	if line := renderDecisionLine(*r); !strings.Contains(line, "billing: free") || strings.Contains(line, "(") {
		t.Errorf("decision line: %q", line)
	}
}

// `jevlin mining enable` from a trace-off shell refuses: there is nothing
// to enable for.
func TestMiningEnableRefusesFromATraceOffShell(t *testing.T) {
	_, cfg, _ := newFakeRouter(t, nil)
	var out, errOut strings.Builder
	code := cmdMining([]string{"enable", "-config", cfg}, strings.NewReader(""), &out, &errOut,
		func(k string) string { return map[string]string{"JEVLIN_TRACE": "off"}[k] })
	if code != exitUsage || !strings.Contains(errOut.String(), "JEVLIN_TRACE is off") {
		t.Errorf("exit %d err %q", code, errOut.String())
	}
}

func TestTraceOffSpellings(t *testing.T) {
	for v, want := range map[string]bool{"off": true, "OFF": true, " 0 ": true, "false": true, "": false, "on": false, "1": false} {
		if got := traceOff(func(string) string { return v }); got != want {
			t.Errorf("JEVLIN_TRACE=%q: %v, want %v", v, got, want)
		}
	}
}
