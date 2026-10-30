package main

// The egress matrix: every command that carries the router key, under every
// configuration state and environment that could change where it goes. Each
// cell is held to the exact set of origins a credential-bearing request
// reached, which is either the one origin the key may go to or none at all.
//
// What a request reached is observed twice over. Loopback routers record
// each request's Host and Authorization; every dial to anything else — the
// compiled default router, or a host an old file or variable names — is
// caught by a dial hook in front of the network fence and refused, so a cell
// can see that a request was aimed at api.jevlin.ai, or at the host an older
// file or variable names, without
// it leaving the machine.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/netdial"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

const (
	matrixEnvKey    = "sr-matrix-env-canary-0123456789"    // #nosec G101 -- a synthetic key only loopback stubs see
	matrixStoredKey = "sr-matrix-stored-canary-0123456789" // #nosec G101 -- a synthetic key only loopback stubs see
	matrixPasted    = "sr-matrix-pasted-canary-0123456789" // #nosec G101 -- a synthetic key only loopback stubs see
)

// egressRecorder notes every origin a credential-bearing request reached.
type egressRecorder struct {
	mu      sync.Mutex
	reached map[string]int
	keys    map[string]bool
}

func (r *egressRecorder) note(origin, authz string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reached == nil {
		r.reached = map[string]int{}
		r.keys = map[string]bool{}
	}
	r.reached[origin]++
	if authz != "" {
		r.keys[authz] = true
	}
}

func (r *egressRecorder) origins() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for o := range r.reached {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

func (r *egressRecorder) reset() {
	r.mu.Lock()
	r.reached, r.keys = nil, nil
	r.mu.Unlock()
}

// matrixRouter is a loopback router answering every endpoint a router key is
// sent to, recording each request that carries one. redirectTo, when set,
// answers the search with a 307 to another origin.
func matrixRouter(t *testing.T, rec *egressRecorder, redirectTo *string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			rec.note(testOrigin(srv.URL), a)
		}
		if redirectTo != nil && *redirectTo != "" && r.URL.Path == "/v1/search" {
			http.Redirect(w, r, *redirectTo+"/v1/search", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/search":
			_, _ = w.Write([]byte(routerBody))
		case "/v1/fetch":
			_, _ = w.Write([]byte(`{"request_id":"f1","pages":[]}`))
		case "/v1/limits":
			_, _ = w.Write([]byte(`{"caps":{},"spend":{}}`))
		case "/v1/turns":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hookDials notes every dial to a host that is not loopback, refusing it,
// for the life of the test. A dial to loopback goes to the fence as before.
func hookDials(t *testing.T, rec *egressRecorder) {
	t.Helper()
	prev := netdial.Hook
	netdial.Hook = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && host != "127.0.0.1" && host != "::1" && !strings.EqualFold(host, "localhost") {
			scheme := "https"
			if port == "80" {
				scheme = "http"
			}
			o := scheme + "://" + host
			if port != "443" && port != "80" {
				o = scheme + "://" + net.JoinHostPort(host, port)
			}
			rec.note(o, "")
			return nil, errors.New("egress matrix: a dial off this machine, noted and refused")
		}
		return prev(ctx, network, addr)
	}
	t.Cleanup(func() { netdial.Hook = prev })
}

// matrixConfig is one configuration state: how a command is pointed at it,
// what file (if any) it reads, and the router that file names.
type matrixConfig struct {
	name     string
	body     func(stateDir string, turnEnd bool) string // nil: no file at all
	selector string                                     // "flag", "env" (JEVLIN_CONFIG) or "" (none)
	router   string                                     // the router the file names; "" for the default
	refused  bool                                       // the file is not format 1
}

func matrixConfigs(a, b string) []matrixConfig {
	format1 := func(router string) func(string, bool) string {
		return func(stateDir string, turnEnd bool) string {
			s := "schema = 1\n"
			if router != "" {
				s += "[router]\nurl = \"" + router + "\"\n"
			}
			s += "[mining]\nstate_dir = " + fmt.Sprintf("%q", filepath.ToSlash(stateDir)) + "\n"
			if turnEnd {
				s += "turn_end = true\n"
			}
			return s
		}
	}
	return []matrixConfig{
		{name: "no file"},
		{name: "an empty file", body: func(string, bool) string { return "" }, selector: "flag", refused: true},
		{name: "minimal format 1", body: format1(""), selector: "flag"},
		{name: "a custom router", body: format1(a), selector: "flag", router: a},
		{name: "an older file naming another router", body: func(string, bool) string {
			return "[[provider]]\nname = \"search-router\"\nupstream = \"https://legacy-upstream.example/api\"\n[miner]\nenabled = true\nrouter_url = \"" + a + "\"\n"
		}, selector: "flag", refused: true},
		{name: "a file chosen by JEVLIN_CONFIG", body: format1(b), selector: "env", router: b},
	}
}

// matrixKey is where the router key comes from.
type matrixKey struct {
	name    string
	env     bool   // JEVLIN_API_KEY is set
	stored  string // the router credentials.json records; "-" for none stored
	unbound bool   // credentials.json records no router
}

// matrixEnv is the environment overlay.
type matrixEnv struct {
	name   string
	router string // JEVLIN_ROUTER_URL
	legacy bool   // TOKENDROP_UPSTREAM names a legacy upstream
}

// matrixCell is everything one run needs, and the origin its key may reach.
type matrixCell struct {
	args    []string
	env     map[string]string
	allowed string // the one origin a credential may reach; "" for none
}

func TestEveryRouterKeyGoesOnlyWhereItMay(t *testing.T) {
	rec := &egressRecorder{}
	hookDials(t, rec)
	var redirect string
	a := matrixRouter(t, rec, &redirect)
	b := matrixRouter(t, rec, &redirect)
	c := matrixRouter(t, rec, &redirect)
	elsewhere := matrixRouter(t, rec, nil)
	defaultOrigin := testOrigin(config.DefaultRouterURL)

	keys := []matrixKey{
		{name: "JEVLIN_API_KEY", env: true, stored: "-"},
		{name: "a stored key bound to router A", stored: a.URL},
		{name: "a stored key bound to no router", unbound: true},
	}
	envs := []matrixEnv{
		{name: "no endpoint variable"},
		{name: "JEVLIN_ROUTER_URL", router: c.URL},
		{name: "TOKENDROP_UPSTREAM", legacy: true},
	}
	type command struct {
		name string
		keys bool // whether the key dimension applies (login brings its own)
		run  func(t *testing.T, cell matrixCell) int
	}
	commands := []command{
		{"search --stdin", true, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return searchMain(fixedSearchOps(t.TempDir()).ops, append([]string{"--stdin"}, cell.args...),
				strings.NewReader(`{"version":1,"query":"q"}`), &out, &errOut, envOf(cell.env))
		}},
		{"search (terminal)", true, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return searchMain(fixedSearchOps(t.TempDir()).ops, append(cell.args, "q"), strings.NewReader(""), &out, &errOut, envOf(cell.env))
		}},
		{"fetch --stdin", true, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return fetchMain(time.Now, append([]string{"--stdin"}, cell.args...),
				strings.NewReader(`{"version":1,"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}]}`), &out, &errOut, envOf(cell.env))
		}},
		{"limits", true, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return cmdLimits(append([]string{"-json"}, cell.args...), &out, &errOut, envOf(cell.env))
		}},
		{"login (pasted)", false, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return cmdLogin(cell.args, strings.NewReader(matrixPasted+"\n"), &out, &errOut, envOf(cell.env))
		}},
		{"login -key-env", false, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			env := map[string]string{"MATRIX_KEY": matrixPasted}
			for k, v := range cell.env {
				env[k] = v
			}
			return cmdLogin(append([]string{"-key-env", "MATRIX_KEY"}, cell.args...), strings.NewReader(""), &out, &errOut, envOf(env))
		}},
		{"login -no-verify", false, func(t *testing.T, cell matrixCell) int {
			var out, errOut strings.Builder
			return cmdLogin(append([]string{"-no-verify"}, cell.args...), strings.NewReader(matrixPasted+"\n"), &out, &errOut, envOf(cell.env))
		}},
	}

	cells := 0
	for _, mc := range matrixConfigs(a.URL, b.URL) {
		for _, me := range envs {
			for _, cmd := range commands {
				keySet := keys
				if !cmd.keys {
					keySet = []matrixKey{{name: "a pasted key", stored: "-"}}
				}
				for _, mk := range keySet {
					name := strings.Join([]string{mc.name, me.name, cmd.name, mk.name}, " / ")
					t.Run(name, func(t *testing.T) {
						cell := prepareMatrixCell(t, mc, me, mk, false)
						effective := defaultOrigin
						switch {
						case me.router != "":
							effective = testOrigin(me.router)
						case mc.router != "":
							effective = testOrigin(mc.router)
						}
						switch {
						case mc.refused:
							cell.allowed = ""
						case strings.HasPrefix(cmd.name, "login"):
							cell.allowed = effective
							if cmd.name == "login -no-verify" {
								cell.allowed = ""
							}
						case mk.env:
							cell.allowed = effective
						case mk.unbound:
							cell.allowed = ""
						case testOrigin(mk.stored) == effective:
							cell.allowed = effective
						default:
							cell.allowed = ""
						}
						rec.reset()
						cmd.run(t, cell)
						assertReached(t, rec, cell.allowed)
					})
					cells++
				}
			}
		}
	}

	// A redirect, and the trace_unsupported retry, are held to the key's
	// origin the same way as the first request.
	t.Run("a redirect off the key's router", func(t *testing.T) {
		redirect = elsewhere.URL
		defer func() { redirect = "" }()
		cell := prepareMatrixCell(t, matrixConfigs(a.URL, b.URL)[3], envs[0], keys[1], false)
		rec.reset()
		var out, errOut strings.Builder
		searchMain(fixedSearchOps(t.TempDir()).ops, append([]string{"--stdin"}, cell.args...),
			strings.NewReader(`{"version":1,"query":"q"}`), &out, &errOut, envOf(cell.env))
		for _, o := range rec.origins() {
			if o == testOrigin(elsewhere.URL) {
				t.Errorf("a redirect carried the key to %s", o)
			}
		}
	})

	// The turn end: off unless the configuration turns it on, and then only to
	// the key's router.
	for _, mc := range matrixConfigs(a.URL, b.URL) {
		if mc.body == nil || mc.refused {
			continue
		}
		for _, on := range []bool{false, true} {
			for _, mk := range keys {
				name := fmt.Sprintf("turn end on=%v / %s / %s", on, mc.name, mk.name)
				t.Run(name, func(t *testing.T) {
					cell := prepareMatrixCell(t, mc, envs[0], mk, on)
					effective := defaultOrigin
					if mc.router != "" {
						effective = testOrigin(mc.router)
					}
					cell.allowed = ""
					if on && (mk.env || (!mk.unbound && testOrigin(mk.stored) == effective)) {
						cell.allowed = effective
					}
					cfg, _, err := loadConfig(cfgArg(cell.args), envOf(cell.env))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(cfg.Miner.SessionsDir, 0o700); err != nil {
						t.Fatal(err)
					}
					file := filepath.Join(cfg.Miner.SessionsDir, "t1"+turnEndSuffix)
					if err := os.WriteFile(file, []byte(`{"session_id":"s1","turn_id":"t1"}`), 0o600); err != nil {
						t.Fatal(err)
					}
					rec.reset()
					cmdTurnEnd(append(cell.args, "-file", file), envOf(cell.env))
					assertReached(t, rec, cell.allowed)
				})
				cells++
			}
		}
	}
	t.Logf("%d cells", cells)
}

func cfgArg(args []string) string {
	for i, a := range args {
		if a == "-config" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// prepareMatrixCell writes the configuration and the credentials one cell
// runs under, and returns its arguments and environment.
func prepareMatrixCell(t *testing.T, mc matrixConfig, me matrixEnv, mk matrixKey, turnEnd bool) matrixCell {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	state := filepath.Join(t.TempDir(), "state")
	env := map[string]string{"JEVLIN_HOME": home}
	var args []string
	if mc.body != nil {
		path := filepath.Join(t.TempDir(), "jevlin.toml")
		if err := os.WriteFile(path, []byte(mc.body(state, turnEnd)), 0o600); err != nil {
			t.Fatal(err)
		}
		switch mc.selector {
		case "flag":
			args = []string{"-config", path}
		case "env":
			env["JEVLIN_CONFIG"] = path
		}
	}
	if me.router != "" {
		env[config.EnvRouterURL] = me.router
	}
	if me.legacy {
		env[strings.Join([]string{"TOKENDROP", "UPSTREAM"}, "_")] = "https://legacy-upstream.example/api"
	}
	if mk.env {
		env[apiKeyEnv] = matrixEnvKey
	}
	// Where this configuration keeps credentials.json; a refused file has
	// none, so the key is written where a state_dir of its own would put it.
	credPath := filepath.Join(filepath.Dir(state), credentialsFile)
	if cfg, _, err := loadConfig(cfgArg(args), envOf(env)); err == nil {
		credPath = credentialsPath(cfg.Miner)
	}
	if mk.stored != "-" || mk.unbound {
		c := credentials{APIKey: matrixStoredKey}
		if !mk.unbound {
			c.Router = testOrigin(mk.stored)
		}
		if err := writeCredentials(credPath, c); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Remove(credPath) })
	return matrixCell{args: args, env: env}
}

// assertReached holds the origins a credential-bearing request reached to
// exactly allowed, or to none.
func assertReached(t *testing.T, rec *egressRecorder, allowed string) {
	t.Helper()
	got := rec.origins()
	switch {
	case allowed == "" && len(got) != 0:
		t.Errorf("a credential-bearing request reached %v; none may", got)
	case allowed != "" && (len(got) != 1 || got[0] != allowed):
		t.Errorf("credential-bearing requests reached %v; exactly %s may", got, allowed)
	}
	for _, o := range got {
		if strings.Contains(o, "legacy-upstream") {
			t.Errorf("a request was aimed at %s", o)
		}
	}
}

// A router, agents API or AS named with a lookalike host never loads, so no
// key is sent toward it and no command gets as far as dialing.
func TestALookalikeEndpointIsRefusedBeforeAnyKeyIsSent(t *testing.T) {
	rec := &egressRecorder{}
	hookDials(t, rec)
	home := t.TempDir()
	for name, env := range map[string]map[string]string{
		"JEVLIN_ROUTER_URL":     {config.EnvRouterURL: "https://apİ.jevlin.ai"},
		"JEVLIN_AGENTS_API_URL": {config.EnvAgentsAPIURL: "https://agents-v1.jevlİn.ai"},
		"JEVLIN_AS_URL":         {config.EnvASURL: "https://rewards-testnet.jevlİn.ai"},
	} {
		env["JEVLIN_HOME"] = home
		env[apiKeyEnv] = matrixEnvKey
		var out, errOut strings.Builder
		code := searchMain(fixedSearchOps(t.TempDir()).ops, []string{"--stdin"}, strings.NewReader(`{"version":1,"query":"q"}`), &out, &errOut, envOf(env))
		if code == exitOK || !strings.Contains(out.String(), "plain ASCII") {
			t.Errorf("%s: exit %d\n%s", name, code, out.String())
		}
		var lout, lerr strings.Builder
		if code := cmdLimits([]string{"-json"}, &lout, &lerr, envOf(env)); code == exitOK {
			t.Errorf("%s: limits exit 0", name)
		}
	}
	if got := rec.origins(); len(got) != 0 {
		t.Errorf("a lookalike configuration reached %v", got)
	}
}
