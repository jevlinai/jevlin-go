package auth

// The refresh authorization is recorded with the AS that issued it and goes
// nowhere else: a configuration that names another AS after enrollment —
// changed by hand, or chosen with JEVLIN_CONFIG — reaches nothing with it.
// Held by connections dialed to that other AS, which must be none.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/egress"
)

// countingAS answers OAuth metadata, the token endpoint and revocation, and
// counts every connection dialed to it.
func countingAS(t *testing.T) (*httptest.Server, func() int64) {
	t.Helper()
	var conns atomic.Int64
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                        srv.URL,
			"authorization_endpoint":        srv.URL + "/oauth/authorize",
			"token_endpoint":                srv.URL + "/oauth/token",
			"device_authorization_endpoint": srv.URL + "/oauth/device_authorization",
			"revocation_endpoint":           srv.URL + "/oauth/revoke",
			"jwks_uri":                      srv.URL + "/oauth/jwks.json",
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"DPoP","expires_in":300,"refresh_token":"rt-next"}`))
	})
	mux.HandleFunc("POST /oauth/revoke", func(http.ResponseWriter, *http.Request) {})
	srv = httptest.NewUnstartedServer(mux)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns.Load
}

func clientFor(t *testing.T, asURL, stateDir string) *OAuthClient {
	t.Helper()
	store, err := OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDiscoverer(DiscoveryConfig{BaseURL: asURL, ChainID: "twilight-1", SlotID: 7})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewOAuthClient(context.Background(), d, store)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTheRefreshAuthorizationGoesOnlyToTheASThatIssuedIt(t *testing.T) {
	issuer, issuerConns := countingAS(t)
	other, otherConns := countingAS(t)
	state := filepath.Join(t.TempDir(), "state")

	// Issued by the first AS, as an enrollment there records it.
	if err := clientFor(t, issuer.URL, state).store.SaveRefreshToken(testOrigin(t, issuer.URL), "rt-issued"); err != nil {
		t.Fatal(err)
	}
	// The same state, a configuration naming the other AS.
	moved := clientFor(t, other.URL, state)
	if _, err := moved.Refresh(context.Background()); !egress.IsRefused(err) {
		t.Errorf("refresh against another AS: %v, want a refusal", err)
	}
	if err := moved.Revoke(context.Background()); !egress.IsRefused(err) {
		t.Errorf("revoke against another AS: %v, want a refusal", err)
	}
	if n := otherConns(); n != 0 {
		t.Errorf("%d connection(s) dialed to the AS that did not issue the authorization", n)
	}
	if rec, ok, err := moved.store.LoadRefreshToken(); err != nil || !ok || rec.Token != "rt-issued" {
		t.Errorf("a refused refresh disturbed the authorization: %+v %v %v", rec, ok, err)
	}

	// The issuing AS still takes it, and its successor is recorded with it.
	own := clientFor(t, issuer.URL, state)
	if _, err := own.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh at the issuing AS: %v", err)
	}
	if issuerConns() == 0 {
		t.Fatal("the issuing AS was never dialed, so the refusal above proves nothing")
	}
	rec, ok, err := own.store.LoadRefreshToken()
	if err != nil || !ok || rec.Token != "rt-next" || rec.AS != testOrigin(t, issuer.URL) {
		t.Errorf("the rotated authorization: %+v %v %v", rec, ok, err)
	}
}

// A refresh.token that records no AS — an earlier client's raw token, or a
// file something else wrote — is sent nowhere, and says what to do.
func TestARefreshTokenThatRecordsNoASIsSentNowhere(t *testing.T) {
	as, conns := countingAS(t)
	state := filepath.Join(t.TempDir(), "state")
	c := clientFor(t, as.URL, state)
	for _, raw := range []string{"rt-raw-token", `{"v":1,"token":"rt"}`, `{"v":1,"as":"","token":"rt"}`, `{"v":2,"as":"` + as.URL + `","token":"rt"}`} {
		if err := os.WriteFile(filepath.Join(state, refreshTokenFile), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Refresh(context.Background()); !errors.Is(err, ErrUnboundRefreshAuthorization) {
			t.Errorf("%q: refresh %v, want ErrUnboundRefreshAuthorization", raw, err)
		}
		if err := c.Revoke(context.Background()); !errors.Is(err, ErrUnboundRefreshAuthorization) {
			t.Errorf("%q: revoke %v, want ErrUnboundRefreshAuthorization", raw, err)
		}
	}
	if n := conns(); n != 0 {
		t.Errorf("%d connection(s) dialed with an authorization bound to no AS", n)
	}
}

// Every request the OAuth client sends carries a DPoP proof, and the
// enrollment assertion besides: all of it is held to the configured AS by the
// transport, so a metadata document naming another origin's token endpoint
// reaches nothing there.
func TestTheAssertionAndEveryProofGoOnlyToTheConfiguredAS(t *testing.T) {
	elsewhere, elsewhereConns := countingAS(t)
	var as *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        as.URL,
			"authorization_endpoint":        as.URL + "/oauth/authorize",
			"token_endpoint":                as.URL + "/oauth/token",
			"device_authorization_endpoint": as.URL + "/oauth/device_authorization",
			"revocation_endpoint":           as.URL + "/oauth/revoke",
			"jwks_uri":                      as.URL + "/oauth/jwks.json",
			"grant_types_supported":         []string{GrantTypeJWTBearer},
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		// Off-origin: the redirect a misbehaving AS could answer with.
		http.Redirect(w, r, elsewhere.URL+"/oauth/token", http.StatusTemporaryRedirect)
	})
	as = httptest.NewServer(mux)
	t.Cleanup(as.Close)

	c := clientFor(t, as.URL, filepath.Join(t.TempDir(), "state"))
	if _, err := c.RedeemEnrollmentAssertion(context.Background(), "eyJhbGciOi.assertion.sig"); err == nil {
		t.Fatal("an assertion redeemed through an off-origin redirect")
	}
	if n := elsewhereConns(); n != 0 {
		t.Errorf("%d connection(s) dialed off the configured AS", n)
	}
}

// refresh.token sits in a directory a sandboxed command can write, so a
// record is held to what this client writes: an issuer spelled exactly as an
// origin, and a token with no control or format character. Anything else is
// no authorization at all.
func TestARefreshRecordIsHeldToWhatThisClientWrites(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	s, err := OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, as := range []string{
		"https://x\u2028y.example", "https://x\u200by.example", "https://x\u202ey.example",
		"https://AS.example", "https://as.example/path", "https://as.example:443",
		"see https://as.example now", "", "https://user@as.example",
	} {
		raw, _ := json.Marshal(RefreshAuthorization{V: 1, AS: as, Token: "rt"})
		if err := os.WriteFile(filepath.Join(state, refreshTokenFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if rec, ok, err := s.LoadRefreshToken(); !errors.Is(err, ErrUnboundRefreshAuthorization) || ok {
			t.Errorf("as=%q loaded: %+v %v %v", as, rec, ok, err)
		}
	}
	raw, _ := json.Marshal(RefreshAuthorization{V: 1, AS: "https://as.example", Token: "rt\u2028x"})
	if err := os.WriteFile(filepath.Join(state, refreshTokenFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadRefreshToken(); !errors.Is(err, ErrUnboundRefreshAuthorization) || ok {
		t.Errorf("a token holding a line separator loaded: %v %v", ok, err)
	}
}

// The refusal for an authorization issued elsewhere names the configured AS,
// which the participant chose, and never what the record says.
func TestTheRefusalNamesTheConfiguredASNotTheRecord(t *testing.T) {
	other, _ := countingAS(t)
	state := filepath.Join(t.TempDir(), "state")
	const recorded = "https://recorded-issuer.example"
	if err := clientFor(t, other.URL, state).store.SaveRefreshToken(recorded, "rt"); err != nil {
		t.Fatal(err)
	}
	_, err := clientFor(t, other.URL, state).Refresh(context.Background())
	if !errors.Is(err, ErrAuthorizationElsewhere) || !egress.IsRefused(err) {
		t.Fatalf("refresh: %v", err)
	}
	if strings.Contains(err.Error(), "recorded-issuer") {
		t.Errorf("the refusal repeats the record's issuer: %v", err)
	}
	if !strings.Contains(err.Error(), testOrigin(t, other.URL)) {
		t.Errorf("the refusal does not name the configured AS: %v", err)
	}
}
