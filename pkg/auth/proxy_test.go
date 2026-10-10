package auth

// The AS clients' proxy rule (proxy.go), driven through the package's own
// transports: an environment proxy on this machine's loopback carries the
// request, as Codex's sandbox needs; one anywhere else is ignored, as every
// environment proxy was before, and nothing is dialed toward it.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withEnvironmentProxy makes the package read proxy as the environment's
// proxy for the rest of the test.
func withEnvironmentProxy(t *testing.T, proxy string) {
	t.Helper()
	var u *url.URL
	if proxy != "" {
		var err error
		if u, err = url.Parse(proxy); err != nil {
			t.Fatal(err)
		}
	}
	was := environmentProxy
	environmentProxy = func(*http.Request) (*url.URL, error) { return u, nil }
	t.Cleanup(func() { environmentProxy = was })
}

// connectProxy is a CONNECT-only proxy on loopback that counts the tunnels
// it opened and the targets they went to. A request that did not come
// through it is not counted, which is the observable: connections, not
// handler calls on the AS.
type connectProxy struct {
	*httptest.Server
	tunnels atomic.Int32
	mu      sync.Mutex
	targets []string
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	p := &connectProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		// The tunnel goes only to this machine: the AS under test is an
		// httptest server on loopback, and anything else is refused here
		// before the network fence would refuse it.
		if host, _, err := net.SplitHostPort(r.Host); err != nil || !isLoopbackLiteral(host) {
			http.Error(w, "loopback targets only", http.StatusForbidden)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, 5*time.Second) // #nosec G704 -- a loopback target, checked above
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = upstream.Close()
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		p.tunnels.Add(1)
		p.mu.Lock()
		p.targets = append(p.targets, r.Host)
		p.mu.Unlock()
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
	}))
	t.Cleanup(p.Close)
	return p
}

// tlsAS is an AS that serves its discovery document and counts requests.
func tlsAS(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/observations" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(fixtureDocument(t, "https://"+r.Host), &doc); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func discovererFor(t *testing.T, srv *httptest.Server) *Discoverer {
	t.Helper()
	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: srv.URL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	d.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	return d
}

func dpopPostTo(t *testing.T, srv *httptest.Server) error {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proofer, err := NewProofer(key)
	if err != nil {
		t.Fatal(err)
	}
	dt := newDPoPTransport(proofer)
	dt.base.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/observations", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newCredentialClient(dt).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// A loopback proxy carries both AS clients' requests, by CONNECT, to the
// AS's own address.
func TestTheASClientsUseALoopbackEnvironmentProxy(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(t *testing.T, srv *httptest.Server) error
	}{
		{"discovery", func(t *testing.T, srv *httptest.Server) error {
			_, err := discovererFor(t, srv).Document(context.Background())
			return err
		}},
		{"DPoP credential client", dpopPostTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := tlsAS(t)
			proxy := newConnectProxy(t)
			withEnvironmentProxy(t, proxy.URL)
			if err := tc.call(t, srv); err != nil {
				t.Fatalf("the request through the loopback proxy failed: %v", err)
			}
			if proxy.tunnels.Load() == 0 {
				t.Fatalf("no tunnel was opened through the loopback proxy; the AS saw %d request(s) directly", hits.Load())
			}
			want := strings.TrimPrefix(srv.URL, "https://")
			proxy.mu.Lock()
			defer proxy.mu.Unlock()
			for _, got := range proxy.targets {
				if got != want {
					t.Errorf("a tunnel went to %s, not the AS at %s", got, want)
				}
			}
		})
	}
}

// A proxy that is not on this machine's loopback is ignored and nothing is
// dialed toward it: the request goes direct. The proxy named here is a
// TEST-NET-1 address that the network fence would refuse, so a dial toward
// it fails the call rather than going unnoticed.
func TestTheASClientsIgnoreANonLoopbackEnvironmentProxy(t *testing.T) {
	for _, proxy := range []string{"http://192.0.2.1:3128", "http://localhost:3128", "http://[2001:db8::1]:3128", "http://proxy.example.invalid:3128"} {
		t.Run(proxy, func(t *testing.T) {
			withEnvironmentProxy(t, proxy)
			req := httptest.NewRequest(http.MethodGet, "https://as.example.invalid/x", nil)
			if u, err := loopbackProxy(req); u != nil || err != nil {
				t.Fatalf("loopbackProxy = %v, %v; want no proxy", u, err)
			}
			srv, hits := tlsAS(t)
			if _, err := discovererFor(t, srv).Document(context.Background()); err != nil {
				t.Fatalf("the direct request failed: %v", err)
			}
			if hits.Load() == 0 {
				t.Fatal("the AS saw no request")
			}
			if err := dpopPostTo(t, srv); err != nil {
				t.Fatalf("the direct DPoP request failed: %v", err)
			}
		})
	}
}

// The loopback set is 127.0.0.0/8 and ::1, as literals.
func TestLoopbackProxyAcceptsOnlyLoopbackLiterals(t *testing.T) {
	for proxy, want := range map[string]bool{
		"http://127.0.0.1:3128":   true,
		"http://127.9.8.7:1":      true,
		"http://[::1]:3128":       true,
		"http://localhost:3128":   false,
		"http://0.0.0.0:3128":     false,
		"http://[::]:3128":        false,
		"http://10.0.0.1:3128":    false,
		"http://192.168.1.1:3128": false,
		"":                        false,
	} {
		withEnvironmentProxy(t, proxy)
		u, err := loopbackProxy(httptest.NewRequest(http.MethodGet, "https://as.example.invalid/", nil))
		if err != nil || (u != nil) != want {
			t.Errorf("%q: proxy %v (err %v), want used=%v", proxy, u, err, want)
		}
	}
}

// Production reads the environment through net/http's own function, so the
// seam the tests use is the only thing they replace.
func TestTheEnvironmentProxyIsNetHTTPs(t *testing.T) {
	if reflect.ValueOf(environmentProxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("environmentProxy is not http.ProxyFromEnvironment")
	}
}

// An environment proxy that does not parse was ignored before this rule
// existed, as every environment proxy was, and still is: the request goes
// direct rather than failing.
func TestAnUnparsableEnvironmentProxyIsIgnored(t *testing.T) {
	was := environmentProxy
	environmentProxy = func(*http.Request) (*url.URL, error) { return nil, errors.New("invalid proxy address") }
	t.Cleanup(func() { environmentProxy = was })
	u, err := loopbackProxy(httptest.NewRequest(http.MethodGet, "https://as.example.invalid/", nil))
	if u != nil || err != nil {
		t.Fatalf("loopbackProxy = %v, %v; want no proxy and no error", u, err)
	}
	srv, hits := tlsAS(t)
	if _, err := discovererFor(t, srv).Document(context.Background()); err != nil || hits.Load() == 0 {
		t.Fatalf("the request did not go direct: %v (AS saw %d)", err, hits.Load())
	}
}
