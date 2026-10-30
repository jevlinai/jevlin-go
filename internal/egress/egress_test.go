package egress

// The guard every request carrying a secret goes through. A request is held to
// its origin by connections dialed, not by handler calls: a refused request
// must not reach the network at all.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/networkfence"
)

func TestMain(m *testing.M) {
	os.Exit(networkfence.Guard(m))
}

// counting is a loopback server that counts the connections dialed to it.
func counting(t *testing.T, h http.Handler) (*httptest.Server, func() int64) {
	t.Helper()
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

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOriginIsSchemeHostAndPortInLowerCase(t *testing.T) {
	for raw, want := range map[string]string{
		"https://API.Jevlin.ai/v1/search": "https://api.jevlin.ai",
		"https://api.jevlin.ai:443/x":     "https://api.jevlin.ai",
		"http://127.0.0.1:80/":            "http://127.0.0.1",
		"http://127.0.0.1:8080":           "http://127.0.0.1:8080",
		"http://[::1]:9/":                 "http://[::1]:9",
	} {
		if got, err := Origin(raw); err != nil || got != want {
			t.Errorf("Origin(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "ftp://h.example", "https://u:p@h.example", "/relative", "https://"} {
		if got, err := Origin(raw); err == nil {
			t.Errorf("Origin(%q) = %q, want an error", raw, got)
		}
	}
}

func TestCheckRefusesEveryOtherOrigin(t *testing.T) {
	const bound = "https://api.jevlin.ai"
	for _, ok := range []string{"https://api.jevlin.ai/v1/search", "https://API.jevlin.ai:443/v1/fetch"} {
		if err := Check("k", bound, mustURL(t, ok)); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, other := range []string{
		"https://elsewhere.example/v1/search",
		"http://api.jevlin.ai/v1/search",
		"https://api.jevlin.ai:8443/v1/search",
		"https://api.jevlin.ai.evil.example/v1/search",
		"https://user:pw@api.jevlin.ai/v1/search",
	} {
		err := Check("k", bound, mustURL(t, other))
		if !IsRefused(err) {
			t.Errorf("%s: %v, want a refusal", other, err)
		}
	}
	for _, unbound := range []string{"", "not an origin"} {
		if err := Check("k", unbound, mustURL(t, "https://api.jevlin.ai/v1/search")); !IsRefused(err) {
			t.Errorf("bound %q: %v, want a refusal of every destination", unbound, err)
		}
	}
}

// A refusal names the secret and the two origins, and nothing of the request.
func TestARefusalNamesOnlyTheOrigins(t *testing.T) {
	err := Check("the stored router key", "https://api.jevlin.ai", mustURL(t, "https://elsewhere.example/v1/search?q=private"))
	msg := err.Error()
	for _, want := range []string{"the stored router key", "https://api.jevlin.ai", "https://elsewhere.example"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not name %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "private") || strings.Contains(msg, "/v1/search") {
		t.Errorf("the refusal repeats the request: %s", msg)
	}
}

// Do refuses before dialing, and every redirect hop is held to the bound
// origin whatever the client's own redirect policy would allow.
func TestDoSendsOnlyToTheBoundOriginWhateverTheClientFollows(t *testing.T) {
	other, otherConns := counting(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the other origin was reached: %s %s (Authorization %q)", r.Method, r.URL, r.Header.Get("Authorization"))
	}))
	bound, boundConns := counting(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	// A client that follows every redirect, which is the policy this guard
	// must not depend on.
	client := &http.Client{}

	send := func(target string) error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := Do(client, req, "k", bound.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := send(bound.URL + "/ok"); err != nil {
		t.Fatalf("the bound origin: %v", err)
	}
	if err := send(other.URL + "/direct"); !IsRefused(err) {
		t.Errorf("a direct request to another origin: %v", err)
	}
	if err := send(bound.URL + "/hop"); !IsRefused(err) {
		t.Errorf("a redirect to another origin: %v", err)
	}
	if n := otherConns(); n != 0 {
		t.Errorf("%d connection(s) dialed to the other origin", n)
	}
	if n := boundConns(); n == 0 {
		t.Error("the bound origin was never dialed, so nothing above was exercised")
	}
	if client.Transport != nil {
		t.Error("Do modified the caller's client")
	}
}

func TestTransportChecksEveryRequestItCarries(t *testing.T) {
	other, otherConns := counting(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	bound, _ := counting(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := &http.Client{Transport: &Transport{Secret: "k", Bound: bound.URL}}
	for target, refused := range map[string]bool{bound.URL + "/a": false, other.URL + "/b": true} {
		resp, err := client.Get(target)
		if err == nil {
			_ = resp.Body.Close()
		}
		if IsRefused(err) != refused {
			t.Errorf("%s: %v, want refused=%v", target, err, refused)
		}
	}
	if n := otherConns(); n != 0 {
		t.Errorf("%d connection(s) dialed to the other origin", n)
	}
}

// A host that is not plain ASCII has no origin: Go's lowercasing folds "İ"
// onto "i" while net/http dials the IDNA form, another name, so a lookalike
// compared after folding would pass as the recorded origin. Written in
// Unicode or percent-encoded into it, it is refused, and no connection is
// made for it.
func TestALookalikeHostIsRefusedNotFolded(t *testing.T) {
	const bound = "https://api.jevlin.ai"
	for _, raw := range []string{
		"https://apİ.jevlİn.ai/v1/search",
		"https://ap%C4%B0.jevlin.ai/v1/search",
		"https://API.JEVLİN.AI/v1/search",
		"https://api.jevlin.ai\u200b/v1/search",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			continue // a URL that does not parse cannot be dialed either
		}
		if err := Check("k", bound, u); !IsRefused(err) {
			t.Errorf("%q: Check = %v, want a refusal", raw, err)
		}
	}
	if _, err := Origin("https://apİ.jevlin.ai"); err == nil {
		t.Error("a Unicode host has an origin")
	}
	if err := Check("k", "https://apİ.jevlin.ai", mustURL(t, "https://api.jevlin.ai/x")); !IsRefused(err) {
		t.Errorf("a Unicode bound origin passed: %v", err)
	}
}
