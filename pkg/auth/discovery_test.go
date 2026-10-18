package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/wire"
)

// fixtureDocument loads the shared L3 discovery fixture and repoints its
// endpoint fields at the given test server origin.
func fixtureDocument(t *testing.T, origin string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/fixtures/wire/discovery_document.json")
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(string(raw), "https://as.example.com", origin))
}

func serveDiscovery(t *testing.T, hits *atomic.Int64) (*httptest.Server, DiscoveryConfig) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path != WellKnownPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtureDocument(t, srv.URL))
	}))
	t.Cleanup(srv.Close)
	return srv, DiscoveryConfig{BaseURL: srv.URL, ChainID: "twilight-1", SlotID: 7}
}

// AUTH-020: discovery + identity validation against local config.
func TestDiscoveryValidatesAndCaches(t *testing.T) {
	var hits atomic.Int64
	_, cfg := serveDiscovery(t, &hits)
	d, err := NewDiscoverer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := d.Document(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if doc.ChainID != "twilight-1" || doc.SlotID != "7" {
		t.Fatalf("unexpected identity: %s/%s", doc.ChainID, doc.SlotID)
	}
	if _, err := d.Document(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", hits.Load())
	}
}

// AUTH-020/021: chain or slot mismatch fails the mining plane closed.
func TestDiscoveryIdentityMismatchFailsClosed(t *testing.T) {
	_, cfg := serveDiscovery(t, nil)
	for name, mutate := range map[string]func(*DiscoveryConfig){
		"chain mismatch": func(c *DiscoveryConfig) { c.ChainID = "twilight-2" },
		"slot mismatch":  func(c *DiscoveryConfig) { c.SlotID = 8 },
	} {
		t.Run(name, func(t *testing.T) {
			c := cfg
			mutate(&c)
			d, err := NewDiscoverer(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Document(context.Background()); err == nil {
				t.Fatal("mismatched discovery accepted; §19 requires fail-closed")
			}
		})
	}
}

// AUTH-022: a redirect to a different origin is a different AS identity.
func TestDiscoveryRefusesOffOriginRedirect(t *testing.T) {
	target, _ := serveDiscovery(t, nil)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+WellKnownPath, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)

	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: redirecting.URL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Document(context.Background())
	if err == nil || !strings.Contains(err.Error(), "off-origin") {
		t.Fatalf("off-origin redirect not refused: %v", err)
	}
}

func TestDiscoveryFollowsSameOriginRedirect(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case WellKnownPath:
			http.Redirect(w, r, "/actual", http.StatusMovedPermanently)
		case "/actual":
			_, _ = w.Write(fixtureDocument(t, srv.URL))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: srv.URL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Document(context.Background()); err != nil {
		t.Fatalf("same-origin redirect must be followed: %v", err)
	}
}

// §18: https mandatory off-loopback, checked before any request.
func TestDiscoveryRequiresHTTPSOffLoopback(t *testing.T) {
	if _, err := NewDiscoverer(DiscoveryConfig{BaseURL: "http://as.example.com", ChainID: "twilight-1"}); err == nil {
		t.Fatal("plain http accepted for a public host")
	}
	if _, err := NewDiscoverer(DiscoveryConfig{BaseURL: "http://127.0.0.1:9", ChainID: "twilight-1"}); err != nil {
		t.Fatalf("loopback http refused: %v", err)
	}
	if _, err := NewDiscoverer(DiscoveryConfig{BaseURL: "http://localhost:9", ChainID: "twilight-1"}); err != nil {
		t.Fatalf("localhost http refused: %v", err)
	}
}

// CONTRACT-DISC-008 seed: expiry forces rediscovery; a failed refetch
// does not resurrect the stale cache.
func TestDiscoveryTTLExpiryRefetches(t *testing.T) {
	var hits atomic.Int64
	srv, cfg := serveDiscovery(t, &hits)
	cfg.TTL = time.Minute
	d, err := NewDiscoverer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	d.now = func() time.Time { return clock }

	if _, err := d.Document(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := d.Document(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected refetch after TTL, got %d fetches", hits.Load())
	}

	srv.Close() // AS gone: expired cache must not serve
	clock = clock.Add(2 * time.Minute)
	if _, err := d.Document(context.Background()); err == nil {
		t.Fatal("expired cache served after failed rediscovery; must fail closed")
	}
}

func TestDiscoveryRejectsMalformedDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.DiscoveryDocument{Version: "twilight-mining-as-v2"})
	}))
	t.Cleanup(srv.Close)
	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: srv.URL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Document(context.Background()); err == nil {
		t.Fatal("malformed document accepted")
	}
}

// Review finding: userinfo in the AS URL becomes a Basic auth header on
// the wire and the username survives in *url.Error strings.
func TestDiscoveryRejectsUserinfo(t *testing.T) {
	for _, bad := range []string{"https://token@as.example.com", "https://user:pw@as.example.com"} {
		_, err := NewDiscoverer(DiscoveryConfig{BaseURL: bad, ChainID: "twilight-1", SlotID: 7})
		if err == nil || !strings.Contains(err.Error(), "userinfo") {
			t.Fatalf("userinfo URL %q not rejected: %v", bad, err)
		}
	}
}

// endpointOriginFields is every endpoint-bearing service-document field the
// client dials or names as a token resource.
var endpointOriginFields = []string{
	"authorization_server",
	"join_epoch_endpoint_template",
	"epoch_status_endpoint_template",
	"candidate_list_endpoint_template",
	"current_target_endpoint_template",
	"participation_resource",
	"observations_endpoint",
	"observations_batch_endpoint",
	"observation_status_endpoint_template",
	"activity_status_endpoint_template",
	"provider_verification_endpoint_template",
}

// serveTLSDiscovery serves the fixture over HTTPS with one field rewritten,
// and returns a Discoverer that trusts the test certificate.
func serveTLSDiscovery(t *testing.T, mutate func(origin string, doc map[string]any)) *Discoverer {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WellKnownPath {
			http.NotFound(w, r)
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(fixtureDocument(t, srv.URL), &doc); err != nil {
			t.Error(err)
			return
		}
		mutate(srv.URL, doc)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: srv.URL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	d.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	return d
}

// Scan finding: a service document naming a foreign or downgraded host would
// steer DPoP tokens, capabilities and the provider verification key there,
// and SameOriginRedirects anchors on that first host.
func TestDiscoveryRefusesOffOriginEndpoints(t *testing.T) {
	variants := map[string]func(origin, path string) string{
		"foreign host":   func(_, path string) string { return "https://evil.example" + path },
		"http downgrade": func(origin, path string) string { return strings.Replace(origin, "https://", "http://", 1) + path },
		"relative":       func(_, path string) string { return path },
		"userinfo": func(origin, path string) string {
			return strings.Replace(origin, "https://", "https://evil@", 1) + path
		},
		"other port": func(origin, path string) string { return origin + "1" + path },
	}
	for _, field := range endpointOriginFields {
		for name, rewrite := range variants {
			t.Run(field+"/"+name, func(t *testing.T) {
				d := serveTLSDiscovery(t, func(origin string, doc map[string]any) {
					doc[field] = rewrite(origin, "/v1/mining/slots/{slot_id}/x")
				})
				_, err := d.Document(context.Background())
				if err == nil || !strings.Contains(err.Error(), field) {
					t.Fatalf("off-origin %s accepted: %v", field, err)
				}
			})
		}
	}
}

func TestDiscoveryAcceptsSameOriginEndpointsAndOffOriginEnrollmentTemplate(t *testing.T) {
	d := serveTLSDiscovery(t, func(string, map[string]any) {})
	doc, err := d.Document(context.Background())
	if err != nil {
		t.Fatalf("same-origin fixture refused: %v", err)
	}
	if !strings.HasPrefix(doc.EnrollmentAuthorizationTemplate, "https://openrouter.ai/") {
		t.Fatalf("fixture enrollment template changed: %q", doc.EnrollmentAuthorizationTemplate)
	}
}
