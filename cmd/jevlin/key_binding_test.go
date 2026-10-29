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

	"github.com/jevlinai/jevlin-go/internal/egress"
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
	o, err := egress.Origin(raw)
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

// A credentials.json that records no router — not written by this client's
// login or connect, which always record one — is sent to no router at all,
// the installation's own included, and the refusal says to store the key
// again with login.
func TestAStoredKeyThatRecordsNoRouterGoesNowhere(t *testing.T) {
	own, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(routerBody)) })
	storeKey(t, cfg, credentials{})
	env := map[string]string{"JEVLIN_HOME": root}

	code, out, _ := runSearchStdin(t, fixedSearchOps(root), env, `{"version":1,"query":"q"}`, "-config", cfg)
	envl := decodeEnvelope(t, out)
	if code != exitClientErr || envField(t, envl, "code") != unboundKeyCode || envField(t, envl, "action") != actionLogin {
		t.Errorf("search: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "jevlin login") || strings.Contains(out, boundKey) {
		t.Errorf("the refusal does not say to log in, or repeats the key:\n%s", out)
	}
	if code, _, errOut := runSearch(t, fixedSearchOps(root), env, "-config", cfg, "q"); code != exitClientErr || !strings.Contains(errOut, "jevlin login") {
		t.Errorf("the terminal form: exit %d\n%s", code, errOut)
	}
	if code, out, _ := runLimits(t, env, "-json", "-config", cfg); code != exitClientErr || !strings.Contains(out, `"code":"`+unboundKeyCode+`"`) {
		t.Errorf("limits: exit %d\n%s", code, out)
	}
	if code, out, _ := runFetchStdin(t, env, `{"version":1,"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}]}`, "-config", cfg); code != exitClientErr || !strings.Contains(out, `"code":"`+unboundKeyCode+`"`) {
		t.Errorf("fetch: exit %d\n%s", code, out)
	}
	if n := own.requests(); n != 0 {
		t.Fatalf("a key bound to no router was sent %d time(s)", n)
	}
	// Nothing infers a binding for it either.
	if c := readStoredKey(t, cfg); c.Router != "" || c.AgentsAPI != "" {
		t.Errorf("a command wrote a binding nobody recorded: %+v", c)
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
	_, _ = io.WriteString(f, "\n[router]\nurl = \""+router.URL+"\"\n")
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

// A key that records no agents API is sent to none: connect, its resume and
// mining enable refuse before any request, and say to run connect.
func TestAKeyThatRecordsNoAgentsAPIIsNotSentToThePlatform(t *testing.T) {
	platform, cfg, _ := keyedConnect(t)
	c := readStoredKey(t, cfg)
	c.AgentsAPI = ""
	if err := writeCredentials(credentialsPath(minerOf(t, cfg)), *c); err != nil {
		t.Fatal(err)
	}
	other, otherConns := countingServer(t, nil)
	otherCfg := sameStateOtherURL(t, cfg, platform.srv.URL, other.URL)
	env := installedAs(noEnv, []string{"-config", otherCfg})

	for _, args := range [][]string{{"-config", otherCfg, "-resume"}, {"-config", otherCfg}} {
		var stdout, stderr bytes.Buffer
		cmdConnect(args, &bytes.Buffer{}, &stdout, &stderr, env)
		if !strings.Contains(stderr.String()+stdout.String(), "jevlin connect") {
			t.Errorf("connect %v did not say what to do:\n%s%s", args, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := cmdMining([]string{"enable", "-config", otherCfg}, &bytes.Buffer{}, &stdout, &stderr, env); code == exitOK {
		t.Errorf("mining enable: exit 0\n%s", stdout.String())
	}
	if n := otherConns(); n != 0 {
		t.Fatalf("%d connection(s) carried a key that records no agents API", n)
	}
}

// With no router in the configuration, the router connect records is the
// compiled default: the one the key was issued for.
func TestConnectRecordsTheDefaultRouterWhenTheConfigNamesNone(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfg, _ := connectConfig(t, platform.srv.URL, "")
	if code, out, errOut := runConnect(t, cfg, nil); code != exitOK {
		t.Fatalf("connect: exit %d\n%s\n%s", code, out, errOut)
	}
	if c := readStoredKey(t, cfg); c.Router != origin(t, config.DefaultRouterURL) || c.AgentsAPI != origin(t, platform.srv.URL) {
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

// ── what login records ──────────────────────────────────────────────────

// login records the router it verified the key against. Storing the same key
// again keeps the agents API connect recorded with it; another key, which was
// not issued there as far as this client knows, records none.
func TestLoginRecordsTheRouterAndKeepsTheSameKeysAgentsAPI(t *testing.T) {
	platform, cfg, _ := keyedConnect(t)
	before := readStoredKey(t, cfg)
	router := minerOf(t, cfg).RouterURL.String()
	run := func(key string) *credentials {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := cmdLogin([]string{"-config", cfg, "-no-verify"}, strings.NewReader(key+"\n"), &out, &errOut, noEnv); code != exitOK {
			t.Fatalf("login: exit %d\n%s", code, errOut.String())
		}
		return readStoredKey(t, cfg)
	}
	if got := run(before.APIKey); got.Router != origin(t, router) || got.AgentsAPI != origin(t, platform.srv.URL) {
		t.Errorf("the same key again: %+v", got)
	}
	if got := run("sr-another-key-0123456789"); got.Router != origin(t, router) || got.AgentsAPI != "" {
		t.Errorf("another key: %+v", got)
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
	body := strings.Replace(string(b), "[mining]\n", "[mining]\nturn_end = true\n", 1)
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
	sessions := minerOf(t, f.ownCfg).SessionsDir
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
	credPath := credentialsPath(minerOf(t, cfg))

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
		root, cfg, otherCfg, credentialsPath(minerOf(t, cfg)))
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
