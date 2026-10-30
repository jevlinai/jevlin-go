package auth

// A configured AS one lookalike letter away from the AS that issued the
// refresh authorization is another AS. Go's lowercasing folds "İ" onto "i",
// so an origin compared after folding would match, while net/http dials the
// IDNA form, another name; the guard refuses a host that is not plain ASCII
// instead, and nothing reaches the transport below it.

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/egress"
)

type recordingRT struct {
	host, body string
}

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.host = req.URL.Host
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		r.body = string(b)
	}
	return nil, errors.New("recorded, not sent")
}

func TestTheRefreshAuthorizationDoesNotFollowALookalikeAS(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	const issuer = "https://rewards-testnet.jevlin.ai"
	const lookalike = "https://rewards-testnet.jevlİn.ai"
	if err := clientFor(t, issuer, state).store.SaveRefreshToken(issuer, "rt-lookalike-canary"); err != nil {
		t.Fatal(err)
	}
	c := clientFor(t, lookalike, state)
	if tok, ok, err := c.storedAuthorization(); !egress.IsRefused(err) || ok || tok != "" {
		t.Fatalf("storedAuthorization under %s = %q, %v, %v; want a refusal", lookalike, tok, ok, err)
	}
	below := &recordingRT{}
	c.transport.(*egress.Transport).Base.(*dpopTransport).base = below
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"rt-lookalike-canary"}}
	req, err := http.NewRequest(http.MethodPost, lookalike+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.transport.RoundTrip(req); !egress.IsRefused(err) {
		t.Errorf("the guarded transport sent toward %s: %v", lookalike, err)
	}
	if below.host != "" || strings.Contains(below.body, "rt-lookalike-canary") {
		t.Errorf("a request reached the transport below the guard: host %q", below.host)
	}
}
