package main

// The stored key is sent only to the router and the agents API it is bound
// to (credentials.go, "where the stored key may go").

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

const boundKey = "sr-bound-key-0123456789" // #nosec G101 -- a synthetic key only loopback stubs see

// countingServer is a loopback server that counts the connections dialed to
// it, which is what a request that must not be made is held to: not the
// handler calls, which a refused connection would never reach either.
func countingServer(t *testing.T, h http.Handler) (*httptest.Server, func() int64) {
	t.Helper()
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	}
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns.Load
}

// sameStateOtherURL writes a copy of the config at cfgPath, in a directory
// of its own, with every from replaced by to: the same state, intake and
// credentials, another server.
func sameStateOtherURL(t *testing.T, cfgPath, from, to string) string {
	t.Helper()
	b, err := os.ReadFile(cfgPath) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		t.Fatalf("%s does not name %s", cfgPath, from)
	}
	out := filepath.Join(t.TempDir(), "other.toml")
	if err := os.WriteFile(out, []byte(strings.ReplaceAll(string(b), from, to)), 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
		t.Fatal(err)
	}
	return out
}

// requests is how many requests the fake router has received.
func (f *fakeRouter) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func minerOf(t *testing.T, cfgPath string) config.Miner {
	t.Helper()
	cfg, _, err := loadConfig(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Miner
}

func storeKey(t *testing.T, cfgPath string, c credentials) {
	t.Helper()
	c.APIKey = boundKey
	if err := writeCredentials(credentialsPath(minerOf(t, cfgPath)), c); err != nil {
		t.Fatal(err)
	}
}

func readStoredKey(t *testing.T, cfgPath string) *credentials {
	t.Helper()
	c, err := readCredentials(credentialsPath(minerOf(t, cfgPath)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func origin(t *testing.T, raw string) string {
	t.Helper()
	o, err := urlOrigin(raw)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// assertOriginRefusal holds a machine refusal to its contract: fix_input,
// both origins named, and neither the key nor a path in the message.
func assertOriginRefusal(t *testing.T, code int, stdout, bound, requested string, paths ...string) {
	t.Helper()
	if code != exitUsage {
		t.Fatalf("exit %d, want %d\n%s", code, exitUsage, stdout)
	}
	env := decodeEnvelope(t, stdout)
	if got := envField(t, env, "code"); got != keyOriginCode {
		t.Errorf("code %q, want %q", got, keyOriginCode)
	}
	if got := envField(t, env, "action"); got != actionFixInput {
		t.Errorf("action %q, want %q", got, actionFixInput)
	}
	if !strings.Contains(stdout, bound) || !strings.Contains(stdout, requested) {
		t.Errorf("the refusal does not name both %s and %s:\n%s", bound, requested, stdout)
	}
	for _, leak := range append([]string{boundKey, "sr-stubkey"}, paths...) {
		if strings.Contains(stdout, leak) || strings.Contains(stdout, filepath.ToSlash(leak)) {
			t.Errorf("the refusal repeats %q:\n%s", leak, stdout)
		}
	}
}

func TestTheStoredKeyGoesOnlyToTheRouterItRecords(t *testing.T) {
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	storeKey(t, cfg, credentials{Router: origin(t, own.srv.URL)})
	// The installation's own config would be no help to the other one:
	// what is recorded decides.
	env := map[string]string{"JEVLIN_CONFIG": otherCfg}

	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", otherCfg)
	assertOriginRefusal(t, code, out, origin(t, own.srv.URL), origin(t, other.URL), otherCfg, root)
	code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", otherCfg, "q")
	if code != exitUsage || !strings.Contains(errOut, origin(t, own.srv.URL)) {
		t.Errorf("the terminal form was not refused: exit %d\n%s", code, errOut)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}
	if n := own.requests(); n != 0 {
		t.Fatalf("a refused search still sent %d request(s)", n)
	}

	code, out, _ = runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("the key's own router: exit %d\n%s", code, out)
	}
	req, _ := own.last(t)
	if req.Header.Get("Authorization") != "Bearer "+boundKey {
		t.Errorf("the key's own router got Authorization %q", req.Header.Get("Authorization"))
	}
}

// A port is part of the origin: the same host on another port is another
// server.
func TestAnOriginIsSchemeHostAndPort(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"https://Router.Example/v1/x", "https://router.example:443"},
		{"http://127.0.0.1:80/", "http://127.0.0.1"},
		{"https://[::1]:8443/a", "https://[::1]:8443"},
	} {
		if origin(t, tc.a) != origin(t, tc.b) {
			t.Errorf("%s and %s are one origin: %s, %s", tc.a, tc.b, origin(t, tc.a), origin(t, tc.b))
		}
	}
	for _, tc := range []struct{ a, b string }{
		{"https://router.example", "http://router.example"},
		{"https://router.example", "https://router.example:8443"},
		{"https://router.example", "https://router.example.evil"},
	} {
		if origin(t, tc.a) == origin(t, tc.b) {
			t.Errorf("%s and %s are two origins", tc.a, tc.b)
		}
	}
}

// A credentials.json an older client wrote records no router. Its key
// belongs to the router of the installation's own config, whichever config
// the command names.
func TestAKeyRecordingNoRouterBelongsToTheInstallationsConfig(t *testing.T) {
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	storeKey(t, cfg, credentials{})
	// newFakeRouter's config is <root>/jevlin.toml: the installation's own
	// config is found by JEVLIN_HOME, as it is on a participant's machine.
	env := map[string]string{"JEVLIN_HOME": root}

	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", otherCfg)
	assertOriginRefusal(t, code, out, origin(t, own.srv.URL), origin(t, other.URL), otherCfg, root)
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}

	code, out, _ = runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitOK {
		t.Fatalf("the installation's own config: exit %d\n%s", code, out)
	}
	// A search reads the binding; only connect writes it.
	if c := readStoredKey(t, cfg); c.Router != "" || c.AgentsAPI != "" {
		t.Errorf("a search wrote the binding: %+v", c)
	}
}

func TestLimitsSendsTheStoredKeyOnlyToItsRouter(t *testing.T) {
	own, cfg, _ := newFakeRouter(t, nil)
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	storeKey(t, cfg, credentials{Router: origin(t, own.srv.URL)})
	code, out, _ := runLimits(t, map[string]string{"JEVLIN_CONFIG": cfg}, "-json", "-config", otherCfg)
	if code != exitUsage || !strings.Contains(out, `"code":"`+keyOriginCode+`"`) || !strings.Contains(out, `"action":"fix_input"`) {
		t.Errorf("limits was not refused: exit %d\n%s", code, out)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}
}

// ── the agents API ──────────────────────────────────────────────────────

// keyedConnect is an installation connect has registered against platform,
// with a router of its own named in its config.
func keyedConnect(t *testing.T) (platform *stubPlatform, cfg, stateDir string) {
	t.Helper()
	withShortConnectTimings(t)
	platform = newStubPlatform(t)
	cfg, stateDir = connectConfig(t, platform.srv.URL, "")
	router, _ := countingServer(t, nil)
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "\n[miner]\nrouter_url = \""+router.URL+"\"\n")
	_ = f.Close()
	if code, out, errOut := runConnect(t, cfg, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	return platform, cfg, stateDir
}

func TestConnectRecordsWhereItsKeyMayGo(t *testing.T) {
	platform, cfg, _ := keyedConnect(t)
	c := readStoredKey(t, cfg)
	cfgv, _, err := loadConfig(cfg, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentsAPI != origin(t, platform.srv.URL) || c.Router != origin(t, cfgv.Miner.RouterURL.String()) {
		t.Errorf("connect recorded router %q, agents API %q; want %s, %s", c.Router, c.AgentsAPI,
			origin(t, cfgv.Miner.RouterURL.String()), origin(t, platform.srv.URL))
	}
}

// A key an older connect stored records neither origin. A foreground
// connect writes down the ones the installation's config names; a detached
// resume writes nothing.
func TestAForegroundConnectRecordsAnOlderKeysOrigins(t *testing.T) {
	_, cfg, _ := keyedConnect(t)
	c := readStoredKey(t, cfg)
	wantRouter, wantAPI := c.Router, c.AgentsAPI
	strip := func() {
		if err := writeCredentials(credentialsPath(minerOf(t, cfg)), credentials{APIKey: c.APIKey}); err != nil {
			t.Fatal(err)
		}
	}

	strip()
	if code, _, errOut := runConnect(t, cfg, nil, "-resume"); code != exitOK {
		t.Fatalf("resume: exit %d\n%s", code, errOut)
	}
	if got := readStoredKey(t, cfg); got.Router != "" || got.AgentsAPI != "" {
		t.Errorf("a resume recorded origins: %+v", got)
	}

	if code, out, errOut := runConnect(t, cfg, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	if got := readStoredKey(t, cfg); got.Router != wantRouter || got.AgentsAPI != wantAPI || got.APIKey != c.APIKey {
		t.Errorf("after a foreground connect: router %q agents API %q; want %q %q, same key", got.Router, got.AgentsAPI, wantRouter, wantAPI)
	}
}

// The provider upstream that stands in for an unset router_url is where a
// search goes, not a router anybody named, and is never written down.
func TestAStandInRouterIsNeverRecorded(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfg, _ := connectConfig(t, platform.srv.URL, "")
	if code, out, errOut := runConnect(t, cfg, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	if c := readStoredKey(t, cfg); c.Router != "" || c.AgentsAPI != origin(t, platform.srv.URL) {
		t.Errorf("recorded router %q agents API %q", c.Router, c.AgentsAPI)
	}
}

func TestTheStoredKeyGoesOnlyToTheAgentsAPIItRecords(t *testing.T) {
	platform, cfg, _ := keyedConnect(t)
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, platform.srv.URL, other.URL)
	env := installedAs(noEnv, []string{"-config", cfg})
	bound, requested := origin(t, platform.srv.URL), origin(t, other.URL)

	// The detached resume a search starts.
	var stdout, stderr bytes.Buffer
	if code := cmdConnect([]string{"-config", otherCfg, "-resume"}, &bytes.Buffer{}, &stdout, &stderr, env); code != exitOK {
		t.Errorf("resume: exit %d, want the quiet %d\n%s", code, exitOK, stderr.String())
	}
	if !strings.Contains(stderr.String(), bound) || !strings.Contains(stderr.String(), requested) {
		t.Errorf("the resume did not say why it stopped:\n%s", stderr.String())
	}

	// A foreground connect, in both forms.
	stdout.Reset()
	stderr.Reset()
	if code := cmdConnect([]string{"-config", otherCfg}, &bytes.Buffer{}, &stdout, &stderr, env); code != exitUsage {
		t.Errorf("connect: exit %d, want %d\n%s", code, exitUsage, stderr.String())
	}
	stdout.Reset()
	code := cmdConnect([]string{"-config", otherCfg, "-json"}, &bytes.Buffer{}, &stdout, io.Discard, env)
	assertOriginRefusal(t, code, stdout.String(), bound, requested, otherCfg, cfg)

	// mining enable.
	stdout.Reset()
	stderr.Reset()
	if code := cmdMining([]string{"enable", "-config", otherCfg}, &bytes.Buffer{}, &stdout, &stderr, env); code == exitOK {
		t.Errorf("mining enable: exit 0\n%s", stdout.String())
	}

	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached an agents API the key does not belong to", n)
	}
	// And the key's own agents API still answers.
	if code, _, errOut := runConnect(t, cfg, nil, "-resume"); code != exitOK {
		t.Errorf("resume against the key's own agents API: exit %d\n%s", code, errOut)
	}
}

// ── what is recorded, and by which run ──────────────────────────────────

// twoConfigInstallation is an installation registered under its own config
// (<home>/jevlin.toml, router A), and a second config beside it naming router
// B over the same state, so the same credentials.json. The key is left as an
// older client wrote it: no origins.
func twoConfigInstallation(t *testing.T) (home, own, custom string, a, b *httptest.Server, aConns, bConns func() int64) {
	t.Helper()
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	own, _ = connectConfig(t, platform.srv.URL, "")
	home = filepath.Dir(own)
	a, aConns = countingServer(t, nil)
	b, bConns = countingServer(t, nil)
	f, err := os.OpenFile(own, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "\n[miner]\nrouter_url = \""+a.URL+"\"\n")
	_ = f.Close()
	custom = sameStateOtherURL(t, own, a.URL, b.URL)
	if code, out, errOut := runConnect(t, own, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	c := readStoredKey(t, own)
	if err := writeCredentials(credentialsPath(minerOf(t, own)), credentials{APIKey: c.APIKey}); err != nil {
		t.Fatal(err)
	}
	return home, own, custom, a, b, aConns, bConns
}

// A connect run under a config that is not the installation's own writes no
// origin: what it would write is the installation config's, and once written
// it would never change. A connect under the config the participant names as
// the installation's records that config's.
func TestOnlyTheInstallationsOwnConfigRecordsAnOrigin(t *testing.T) {
	home, _, custom, _, b, _, bConns := twoConfigInstallation(t)
	installation := envOf(map[string]string{"JEVLIN_HOME": home})

	var out, errOut bytes.Buffer
	if code := cmdConnect([]string{"-config", custom}, &bytes.Buffer{}, &out, &errOut, installation); code != exitOK {
		t.Fatalf("connect -config custom: exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if c := readStoredKey(t, custom); c.Router != "" || c.AgentsAPI != "" {
		t.Fatalf("a connect under another config recorded router %q, agents API %q", c.Router, c.AgentsAPI)
	}

	out.Reset()
	errOut.Reset()
	chosen := envOf(map[string]string{"JEVLIN_HOME": home, "JEVLIN_CONFIG": custom})
	if code := cmdConnect(nil, &bytes.Buffer{}, &out, &errOut, chosen); code != exitOK {
		t.Fatalf("JEVLIN_CONFIG=custom connect: exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if c := readStoredKey(t, custom); c.Router != origin(t, b.URL) {
		t.Fatalf("recorded router %q, want %s", c.Router, origin(t, b.URL))
	}

	// What is recorded wins: the installation's default config still names
	// router A, and a search with custom.toml is served by B.
	code, _, errOutS := runSearch(t, fixedSearchOps(t.TempDir()), map[string]string{"JEVLIN_HOME": home}, "-config", custom, "-no-flush", "q")
	if code != exitOK || bConns() == 0 {
		t.Fatalf("search with custom.toml: exit %d, %d connection(s) to its router\n%s", code, bConns(), errOutS)
	}
}

// With nothing recorded and no router in the installation's config, the
// origin the key falls to is a built-in default nobody chose, and the
// refusal does not present it as the key's.
func TestARefusalNamesNoRouterTheInstallationNeverChose(t *testing.T) {
	own, cfg, root := newFakeRouter(t, nil)
	storeKey(t, cfg, credentials{})
	env := map[string]string{"JEVLIN_HOME": t.TempDir()}
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	if code != exitUsage || !strings.Contains(out, `"code":"`+keyOriginCode+`"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	defaults, _, err := loadConfig("", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	standIn := origin(t, defaults.Miner.RouterURL.String())
	for _, gone := range []string{standIn, strings.TrimPrefix(standIn, "https://"), "belongs to", boundKey, root} {
		if strings.Contains(out, gone) {
			t.Errorf("the refusal says %q:\n%s", gone, out)
		}
	}
	for _, said := range []string{"names none", "JEVLIN_CONFIG", origin(t, own.srv.URL)} {
		if !strings.Contains(out, said) {
			t.Errorf("the refusal does not say %q:\n%s", said, out)
		}
	}
	if n := own.requests(); n != 0 {
		t.Fatalf("%d request(s) sent", n)
	}
}

// The turn end refuses another origin silently, as it does every other key
// failure, and dials nothing.
func TestTheTurnEndSendsTheStoredKeyOnlyToItsRouter(t *testing.T) {
	ownSrv, ownConns := countingServer(t, nil)
	other, otherConns := countingServer(t, nil)
	fr, cfg, _ := newFakeRouter(t, nil)
	b, err := os.ReadFile(cfg) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(b), "[miner]\n", "[miner]\nturn_end = true\n", 1)
	write := func(router string) string {
		p := filepath.Join(t.TempDir(), "jevlin.toml")
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, fr.srv.URL, router)), 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
			t.Fatal(err)
		}
		return p
	}
	ownCfg, otherCfg := write(ownSrv.URL), write(other.URL)
	storeKey(t, ownCfg, credentials{Router: origin(t, ownSrv.URL)})
	sessions := minerOf(t, ownCfg).SessionsDir
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	queue := func() string {
		p := filepath.Join(sessions, "t1"+turnEndSuffix)
		if err := os.WriteFile(p, []byte(`{"session_id":"s1","turn_id":"t1"}`), 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
			t.Fatal(err)
		}
		return p
	}
	env := envOf(map[string]string{"JEVLIN_CONFIG": ownCfg})

	if code := cmdTurnEnd([]string{"-config", otherCfg, "-file", queue()}, env); code != exitOK {
		t.Errorf("exit %d, want the silent 0", code)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}
	// The control: the same queued turn end reaches the key's own router.
	if code := cmdTurnEnd([]string{"-config", ownCfg, "-file", queue()}, env); code != exitOK || ownConns() == 0 {
		t.Fatalf("the key's own router: exit %d, %d connection(s)", code, ownConns())
	}
}
