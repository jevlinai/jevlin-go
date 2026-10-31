package main

// The stored key is sent only to the router and the agents API it is bound
// to (credentials.go, "where the stored key may go").
//
// JEVLIN_ROUTER_URL, JEVLIN_PLATFORM_URL and JEVLIN_AGENTS_API_URL replace a
// config's URLs at runtime, so a getenv that returned one would decide an
// origin here before any config or recorded binding did. No getenv in this
// file returns one: envOf's maps name only JEVLIN_CONFIG and JEVLIN_HOME,
// noEnv names nothing, and installedAs only adds JEVLIN_CONFIG. Every origin
// a case counts connections to, or reads back from credentials.json, comes
// from the config file it wrote or the binding connect recorded.

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
	platformapi "github.com/jevlinai/jevlin-go/pkg/platform"
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
// of its own, with every from replaced by to: the same state and
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

func searchBlockOf(t *testing.T, cfgPath string) config.Search {
	t.Helper()
	cfg, _, err := loadConfig(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Search
}

func storeKey(t *testing.T, cfgPath string, c credentials) {
	t.Helper()
	c.APIKey = boundKey
	if err := writeCredentials(credentialsPath(searchBlockOf(t, cfgPath)), c); err != nil {
		t.Fatal(err)
	}
}

func readStoredKey(t *testing.T, cfgPath string) *credentials {
	t.Helper()
	c, err := readCredentials(credentialsPath(searchBlockOf(t, cfgPath)))
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
	router, _ := countingServer(t, nil)
	cfg, stateDir = connectConfig(t, platform.srv.URL, router.URL)
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
	if c.AgentsAPI != origin(t, platform.srv.URL) || c.Router != origin(t, cfgv.Search.RouterURL.String()) {
		t.Errorf("connect recorded router %q, agents API %q; want %s, %s", c.Router, c.AgentsAPI,
			origin(t, cfgv.Search.RouterURL.String()), origin(t, platform.srv.URL))
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
		if err := writeCredentials(credentialsPath(searchBlockOf(t, cfg)), credentials{APIKey: c.APIKey}); err != nil {
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

// A config naming no [router] url resolves to the compiled default router,
// and nothing stands in for it: that is where every search under that
// config carries the key, so that is the router connect records it as bound
// to. Until configuration format 1 a provider upstream stood in for an unset
// router_url and was never written down; there is no such stand-in now, and
// the getenv here names no JEVLIN_ROUTER_URL, so the only origin this
// connect can record is the compiled default.
func TestAConnectUnderAConfigNamingNoRouterRecordsTheCompiledDefault(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfg, _ := connectConfig(t, platform.srv.URL, "")
	if code, out, errOut := runConnect(t, cfg, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	if c := readStoredKey(t, cfg); c.Router != origin(t, config.DefaultRouterURL) || c.AgentsAPI != origin(t, platform.srv.URL) {
		t.Errorf("recorded router %q agents API %q; want %s, %s", c.Router, c.AgentsAPI,
			origin(t, config.DefaultRouterURL), origin(t, platform.srv.URL))
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
	a, aConns = countingServer(t, nil)
	b, bConns = countingServer(t, nil)
	own, _ = connectConfig(t, platform.srv.URL, a.URL)
	home = filepath.Dir(own)
	custom = sameStateOtherURL(t, own, a.URL, b.URL)
	if code, out, errOut := runConnect(t, own, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	c := readStoredKey(t, own)
	if err := writeCredentials(credentialsPath(searchBlockOf(t, own)), credentials{APIKey: c.APIKey}); err != nil {
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
	code, _, errOutS := runSearch(t, fixedSearchOps(t.TempDir()), map[string]string{"JEVLIN_HOME": home}, "-config", custom, "q")
	if code != exitOK || bConns() == 0 {
		t.Fatalf("search with custom.toml: exit %d, %d connection(s) to its router\n%s", code, bConns(), errOutS)
	}
}

// With nothing recorded and an installation whose own config names no
// router (here: no config at all under JEVLIN_HOME), the key belongs to the
// compiled default router, because that is the router every search of that
// installation carries it to. A search under a config naming another router
// is refused with the default named as the key's, as any other recorded
// router would be, and sends nothing. Before format 1 the default was a
// stand-in nobody chose and the refusal said the installation "names none";
// the getenv here names no JEVLIN_ROUTER_URL, so the bound origin is the
// compiled default itself and not an environment's.
func TestAKeyRecordingNoRouterUnderAConfigNamingNoneBelongsToTheCompiledDefault(t *testing.T) {
	own, cfg, root := newFakeRouter(t, nil)
	storeKey(t, cfg, credentials{})
	env := map[string]string{"JEVLIN_HOME": t.TempDir()}
	defaults, _, err := loadConfig("", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	bound := origin(t, defaults.Search.RouterURL.String())
	if bound != origin(t, config.DefaultRouterURL) {
		t.Fatalf("the installation's router is %s, want the compiled default %s", bound, config.DefaultRouterURL)
	}
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	assertOriginRefusal(t, code, out, bound, origin(t, own.srv.URL), root, cfg, credentialsPath(searchBlockOf(t, cfg)))
	if n := own.requests(); n != 0 {
		t.Fatalf("%d request(s) sent", n)
	}
}

// turnEndFixture is two configs over one state with the turn end on, each
// naming its own counting router, and a queue that puts one turn end where
// both configs' turn end reads it.
type turnEndFixture struct {
	ownCfg, otherCfg     string
	ownURL, otherURL     string
	ownConns, otherConns func() int64
	queue                func() string
}

func newTurnEndFixture(t *testing.T) turnEndFixture {
	t.Helper()
	ownSrv, ownConns := countingServer(t, nil)
	other, otherConns := countingServer(t, nil)
	fr, cfg, _ := newFakeRouter(t, nil)
	b, err := os.ReadFile(cfg) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(b), "[search]\n", "[search]\nturn_end = true\n", 1)
	write := func(router string) string {
		p := filepath.Join(t.TempDir(), "jevlin.toml")
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, fr.srv.URL, router)), 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
			t.Fatal(err)
		}
		return p
	}
	f := turnEndFixture{
		ownCfg: write(ownSrv.URL), otherCfg: write(other.URL),
		ownURL: ownSrv.URL, otherURL: other.URL,
		ownConns: ownConns, otherConns: otherConns,
	}
	sessions := searchBlockOf(t, f.ownCfg).SessionsDir
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	f.queue = func() string {
		p := filepath.Join(sessions, "t1"+turnEndSuffix)
		if err := os.WriteFile(p, []byte(`{"session_id":"s1","turn_id":"t1"}`), 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
			t.Fatal(err)
		}
		return p
	}
	return f
}

// The turn end refuses another origin silently, as it does every other key
// failure, and dials nothing.
func TestTheTurnEndSendsTheStoredKeyOnlyToItsRouter(t *testing.T) {
	f := newTurnEndFixture(t)
	storeKey(t, f.ownCfg, credentials{Router: origin(t, f.ownURL)})
	env := envOf(map[string]string{"JEVLIN_CONFIG": f.ownCfg})

	if code := cmdTurnEnd([]string{"-config", f.otherCfg, "-file", f.queue()}, env); code != exitOK {
		t.Errorf("exit %d, want the silent 0", code)
	}
	if n := f.otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}
	// The control: the same queued turn end reaches the key's own router.
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", f.queue()}, env); code != exitOK || f.ownConns() == 0 {
		t.Fatalf("the key's own router: exit %d, %d connection(s)", code, f.ownConns())
	}
}

// A registration journal's key is bound to the agents API that minted it:
// a connect under a config naming another publishes nothing and dials
// nothing.
func TestAJournalsKeyGoesOnlyToTheAgentsAPIThatMintedIt(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfg, _ := connectConfig(t, platform.srv.URL, "")
	fresh, err := platformapi.New(platform.srv.URL, platform.srv.URL).Register(t.Context(), "journal", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingRegistrationFromPlatform(fresh, mustLoadConfig(t, cfg))
	if pending.AgentsAPI != origin(t, platform.srv.URL) {
		t.Fatalf("the journal records agents API %q, want %s", pending.AgentsAPI, origin(t, platform.srv.URL))
	}
	if err := testJournal(t, cfg).Save(pending); err != nil {
		t.Fatal(err)
	}
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, platform.srv.URL, other.URL)
	// Even as the installation's own config, another agents API is refused.
	env := envOf(map[string]string{"JEVLIN_CONFIG": otherCfg})
	credPath := credentialsPath(searchBlockOf(t, cfg))

	var stdout, stderr bytes.Buffer
	if code := cmdConnect([]string{"-config", otherCfg}, &bytes.Buffer{}, &stdout, &stderr, env); code != exitUsage {
		t.Errorf("connect: exit %d, want %d\n%s", code, exitUsage, stderr.String())
	}
	stdout.Reset()
	code := cmdConnect([]string{"-config", otherCfg, "-json"}, &bytes.Buffer{}, &stdout, io.Discard, env)
	assertOriginRefusal(t, code, stdout.String(), origin(t, platform.srv.URL), origin(t, other.URL), otherCfg, cfg)
	if code := cmdConnect([]string{"-config", otherCfg, "-resume"}, &bytes.Buffer{}, io.Discard, io.Discard, env); code != exitOK {
		t.Errorf("resume: exit %d, want the quiet 0", code)
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached another agents API", n)
	}
	if _, err := os.Lstat(credPath); err == nil {
		t.Fatal("credentials.json was written")
	}

	// The minting agents API publishes it, bound to what the journal says.
	// A detached resume finishes it here, under a config that is not the
	// installation's: neither fills in an origin, so what is recorded can
	// only be the journal's own.
	elsewhere := envOf(map[string]string{"JEVLIN_HOME": t.TempDir()})
	if code := cmdConnect([]string{"-config", cfg, "-resume"}, &bytes.Buffer{}, io.Discard, io.Discard, elsewhere); code != exitOK {
		t.Fatalf("resume: exit %d", code)
	}
	if c := readStoredKey(t, cfg); c.AgentsAPI != pending.AgentsAPI || c.Router != pending.Router {
		t.Errorf("published with router %q agents API %q, want the journal's %q %q", c.Router, c.AgentsAPI, pending.Router, pending.AgentsAPI)
	}
}

// fetchAnswering is a router handler that answers every request with a fetch
// response.
func fetchAnswering(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fetchRouterAnswer))
}

const fetchOneURL = `{"version":1,"pages":[{"url":"https://example.test/"}]}`

func TestFetchSendsTheStoredKeyOnlyToItsRouter(t *testing.T) {
	own, cfg, root := newFakeRouter(t, fetchAnswering)
	other, otherConns := countingServer(t, http.HandlerFunc(fetchAnswering))
	otherCfg := sameStateOtherURL(t, cfg, own.srv.URL, other.URL)
	storeKey(t, cfg, credentials{Router: origin(t, own.srv.URL)})
	env := map[string]string{"JEVLIN_CONFIG": cfg}

	code, out, _ := runFetchStdin(t, env, fetchOneURL, "-config", otherCfg)
	assertOriginRefusal(t, code, out, origin(t, own.srv.URL), origin(t, other.URL),
		root, cfg, otherCfg, credentialsPath(searchBlockOf(t, cfg)))
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) reached a router the key does not belong to", n)
	}

	if code, out, _ := runFetchStdin(t, env, fetchOneURL, "-config", cfg); code != exitOK {
		t.Fatalf("the key's own router: exit %d\n%s", code, out)
	}
	if req, _ := own.last(t); req.Header.Get("Authorization") != "Bearer "+boundKey {
		t.Errorf("the key's own router was sent Authorization %q", req.Header.Get("Authorization"))
	}
}
