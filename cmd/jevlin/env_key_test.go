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
