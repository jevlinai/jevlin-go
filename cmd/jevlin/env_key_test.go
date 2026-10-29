package main

// JEVLIN_API_KEY goes to the router this run is configured with, and nowhere
// else: never to a host an older config or variable would have named, and,
// when the router is not the default, with one line on stderr naming it on
// every call that carries the key (credentials.go, resolveAPIKey and
// noteNonDefaultOrigin). The matrix in egress_matrix_test.go crosses this with
// every configuration state; these are the cases read one at a time.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// In every form, the environment's key reaches the configured router, and
// each call says on stderr that it is not the default router.
func TestTheEnvironmentKeyGoesToTheConfiguredRouterAndSaysSo(t *testing.T) {
	limitsBody := `{"daily_micros":5000000,"weekly_micros":0,"today":{"spent_micros":0,"ceiling_micros":5000000,"remaining_micros":5000000}}`
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/limits":
			_, _ = w.Write([]byte(limitsBody))
		case "/v1/fetch":
			fetchAnswering(w, r)
		default:
			_, _ = w.Write([]byte(routerBody))
		}
	})
	env := map[string]string{"JEVLIN_HOME": root, apiKeyEnv: boundKey}
	notice := origin(t, own.srv.URL) + " is not the default router"

	type form struct {
		name string
		run  func() (int, string)
	}
	for _, f := range []form{
		{"search --stdin", func() (int, string) {
			code, _, errOut := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
			return code, errOut
		}},
		{"search", func() (int, string) {
			code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", cfg, "q")
			return code, errOut
		}},
		{"limits", func() (int, string) {
			code, _, errOut := runLimits(t, env, "-config", cfg)
			return code, errOut
		}},
		{"fetch --stdin", func() (int, string) {
			code, _, errOut := runFetchStdin(t, env, fetchOneURL, "-config", cfg)
			return code, errOut
		}},
	} {
		before := own.requests()
		code, errOut := f.run()
		if code != exitOK || own.requests() != before+1 {
			t.Errorf("%s: exit %d, %d request(s)\n%s", f.name, code, own.requests()-before, errOut)
		}
		if strings.Count(errOut, notice) != 1 {
			t.Errorf("%s: stderr does not name the router once:\n%s", f.name, errOut)
		}
		if strings.Contains(errOut, boundKey) {
			t.Errorf("%s: stderr repeats the key:\n%s", f.name, errOut)
		}
	}
	if req, _ := own.last(t); req.Header.Get("Authorization") != "Bearer "+boundKey {
		t.Errorf("the router was sent Authorization %q", req.Header.Get("Authorization"))
	}
}

// JEVLIN_ROUTER_URL chooses the router for one run, over the file, and the
// notice names the variable as what chose it.
func TestJEVLINROUTERURLChoosesTheRouterAndTheNoticeSaysSo(t *testing.T) {
	fileRouter, cfg, root := newFakeRouter(t, nil)
	chosen, chosenConns := countingServer(t, nil)
	env := map[string]string{"JEVLIN_HOME": root, apiKeyEnv: boundKey, config.EnvRouterURL: chosen.URL}
	code, _, errOut := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitOK || chosenConns() == 0 {
		t.Fatalf("exit %d, %d connection(s) to the chosen router\n%s", code, chosenConns(), errOut)
	}
	if n := fileRouter.requests(); n != 0 {
		t.Errorf("the file's router still got %d request(s)", n)
	}
	if !strings.Contains(errOut, origin(t, chosen.URL)) || !strings.Contains(errOut, config.EnvRouterURL) {
		t.Errorf("the notice does not name the router and what chose it:\n%s", errOut)
	}
}

// The default router draws no notice: nothing chose it but the build.
func TestTheDefaultRouterDrawsNoNotice(t *testing.T) {
	var stderr strings.Builder
	noteNonDefaultOrigin(&stderr, origin(t, config.DefaultRouterURL), "router", config.DefaultRouterURL, "the configuration")
	if stderr.Len() != 0 {
		t.Errorf("the default router drew a notice: %q", stderr.String())
	}
}

// The turn end sends the environment's key to the configured router too,
// and only when the configuration turns it on.
func TestTheTurnEndSendsTheEnvironmentKeyOnlyToTheConfiguredRouter(t *testing.T) {
	f := newTurnEndFixture(t)
	env := envOf(map[string]string{apiKeyEnv: boundKey})
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", f.queue()}, env); code != exitOK || f.ownConns() == 0 {
		t.Fatalf("the configured router: exit %d, %d connection(s)", code, f.ownConns())
	}
	if n := f.otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router this run was not configured with", n)
	}
}
