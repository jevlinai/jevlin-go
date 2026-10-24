package main

// With --stdin, the environment's key goes to the installation's own router
// (credentials.go, envKeyRefusedFor).

import (
	"net/http"
	"strings"
	"testing"
)

// With --stdin, the environment's key goes to the installation's own
// router: an explicit config that is another file naming another router is
// refused; the installation's config, or another file naming its router, is
// served.
func TestTheEnvironmentKeyInMachineModeGoesToTheInstallationsRouter(t *testing.T) {
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	env := map[string]string{"JEVLIN_HOME": root, apiKeyEnv: boundKey}

	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", otherCfg)
	assertOriginRefusal(t, code, out, origin(t, own.srv.URL), origin(t, other.URL), otherCfg, root)
	if !strings.Contains(out, apiKeyEnv) {
		t.Errorf("the refusal does not say whose key it is:\n%s", out)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached another router", n)
	}

	for name, path := range map[string]string{
		"the installation's config":           cfg,
		"another file naming the same router": sameStateOtherURL(t, cfg, own.srv.URL, own.srv.URL),
	} {
		before := own.requests()
		code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", path)
		if code != exitOK || own.requests() != before+1 {
			t.Errorf("%s: exit %d, %d request(s)\n%s", name, code, own.requests()-before, out)
		}
	}
}

// The terminal form and limits hold the environment's key to the same
// router; the message says how to choose another one.
func TestTheEnvironmentKeyGoesToTheInstallationsRouterInEveryForm(t *testing.T) {
	limitsBody := `{"daily_micros":5000000,"weekly_micros":0,"today":{"spent_micros":0,"ceiling_micros":5000000,"remaining_micros":5000000}}`
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/limits" {
			_, _ = w.Write([]byte(limitsBody))
			return
		}
		_, _ = w.Write([]byte(routerBody))
	})
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	env := map[string]string{"JEVLIN_HOME": root, apiKeyEnv: boundKey}

	code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", otherCfg, "q")
	if code != exitUsage || !strings.Contains(errOut, origin(t, other.URL)) || !strings.Contains(errOut, "JEVLIN_CONFIG") {
		t.Errorf("terminal search: exit %d\n%s", code, errOut)
	}
	code, out, _ := runLimits(t, env, "-json", "-config", otherCfg)
	if code != exitUsage || !strings.Contains(out, `"code":"`+keyOriginCode+`"`) || !strings.Contains(out, `"action":"fix_input"`) {
		t.Errorf("limits: exit %d\n%s", code, out)
	}
	if code, _, errOut := runLimits(t, env, "-config", otherCfg); code != exitUsage {
		t.Errorf("limits, terminal: exit %d\n%s", code, errOut)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached another router", n)
	}

	before := own.requests()
	if code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", cfg, "q"); code != exitOK {
		t.Errorf("terminal search, the installation's config: exit %d\n%s", code, errOut)
	}
	if code, _, errOut := runLimits(t, env, "-config", cfg); code != exitOK {
		t.Errorf("limits, the installation's config: exit %d\n%s", code, errOut)
	}
	if got := own.requests() - before; got != 2 {
		t.Errorf("the installation's router got %d request(s), want 2", got)
	}

	// A router chosen on purpose is served once its config is the
	// installation's.
	chosen := map[string]string{"JEVLIN_HOME": root, "JEVLIN_CONFIG": otherCfg, apiKeyEnv: boundKey}
	if code, _, errOut := runSearch(t, fixedSearchOps(root), chosen, "-config", otherCfg, "q"); code != exitOK || otherConns() == 0 {
		t.Errorf("JEVLIN_CONFIG naming the other config: exit %d, %d connection(s)\n%s", code, otherConns(), errOut)
	}
}

// The turn end holds the environment's key to the installation's router as
// it does the stored one: silently, exit 0, nothing dialed.
func TestTheTurnEndSendsTheEnvironmentKeyOnlyToTheInstallationsRouter(t *testing.T) {
	f := newTurnEndFixture(t)
	if _, err := readCredentials(credentialsPath(minerOf(t, f.ownCfg))); err == nil {
		t.Fatal("this test is about the environment's key, and a key is stored")
	}
	env := envOf(map[string]string{"JEVLIN_CONFIG": f.ownCfg, apiKeyEnv: boundKey})

	if code := cmdTurnEnd([]string{"-config", f.otherCfg, "-file", f.queue()}, env); code != exitOK {
		t.Errorf("exit %d, want the silent 0", code)
	}
	if n := f.otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached another router", n)
	}
	// The control: the installation's own config reaches its router.
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", f.queue()}, env); code != exitOK || f.ownConns() == 0 {
		t.Fatalf("the installation's router: exit %d, %d connection(s)", code, f.ownConns())
	}
}

// With no router in the installation's config, the environment key's
// refusal names none as the installation's: it says the config names none,
// and how to choose one.
func TestTheEnvironmentKeysRefusalNamesNoRouterTheInstallationNeverChose(t *testing.T) {
	own, cfg, root := newFakeRouter(t, nil)
	env := map[string]string{"JEVLIN_HOME": t.TempDir(), apiKeyEnv: boundKey}
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitUsage || !strings.Contains(out, `"code":"`+keyOriginCode+`"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	defaults, _, err := loadConfig("", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	standIn := origin(t, defaults.Miner.RouterURL.String())
	for _, gone := range []string{standIn, strings.TrimPrefix(standIn, "https://"), boundKey, root} {
		if strings.Contains(out, gone) {
			t.Errorf("the refusal says %q:\n%s", gone, out)
		}
	}
	for _, said := range []string{apiKeyEnv, "names none", "JEVLIN_CONFIG", origin(t, own.srv.URL)} {
		if !strings.Contains(out, said) {
			t.Errorf("the refusal does not say %q:\n%s", said, out)
		}
	}
	if n := own.requests(); n != 0 {
		t.Fatalf("%d request(s) sent", n)
	}
}
