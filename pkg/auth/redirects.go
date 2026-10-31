package auth

// The redirect policy every credential-bearing http.Client in this module
// carries.
//
// THE DEFECT THIS EXISTS TO CLOSE. Every credential-bearing client used to
// build its own `&http.Client{Transport: …, Timeout: …}` and not one set
// CheckRedirect, so all of them inherited net/http's default of following
// up to ten redirects. 307 and 308 preserve the method AND the body, and Go
// strips only Authorization/Cookie on a cross-host redirect: a body is
// re-sent regardless. A router or platform answering 307 would therefore
// re-send whatever the request carried to whatever host it named.
//
// A policy stated once makes the next call site correct without its author
// having to know this comment exists. cmd/jevlin/boundary_test.go's
// TestEveryHTTPClientInTheModuleSetsARedirectPolicy enforces it
// structurally, so the guarantee does not depend on remembering to state it.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// maxCredentialRedirects bounds a redirect chain. Three is not a tuning
// parameter: it is "a couple of hops within one origin is normal, a chain
// is not".
const maxCredentialRedirects = 3

// SameOriginRedirects is the redirect policy every http.Client in this module
// carries: bounded, and same-origin only. It is deliberately NOT "refuse
// every redirect" — a service legitimately redirecting within its own origin
// must not break a search, and a policy that broke it would be reverted by
// the first person it inconvenienced.
//
// The origin compared against is the ORIGINAL request's (via[0]), not a
// configured one: the first request's origin is the identity the caller
// intended, and a redirect leaving it is the service handing the credential
// to someone else, whatever the reason.
//
// Exported because the clients that need it are not in this package.
// cmd/jevlin has its own that carry a credential — search.go's router
// client and credentials.go's login probe, both bearing the participant's
// sr- key, the turn end's and the limits read's — and pkg/platform's
// carries the platform-issued key. Each keeps its own timeout/transport
// reasoning (search.go: no overall timeout, since one silently caps how
// long a search may run) that a shared constructor would flatten. So what
// travels here is the policy, not the client.
func SameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= maxCredentialRedirects {
		return errors.New("auth: too many redirects on a credential-bearing request")
	}
	// via is never empty when CheckRedirect is called, but a nil deref here
	// would turn a security control into a panic, so it is checked rather
	// than assumed.
	if len(via) == 0 {
		return errors.New("auth: redirect with no originating request; refusing")
	}
	if !sameOrigin(req.URL, originOf(via[0].URL)) {
		return fmt.Errorf("auth: request redirected off-origin to %s://%s — "+
			"refusing to re-send a credential to a different host",
			req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// sameOrigin compares scheme and host, port included: net/http strips
// Authorization by HOST, ignoring the port, so 127.0.0.1:P1 → 127.0.0.1:P2
// would otherwise carry the header.
func sameOrigin(u, origin *url.URL) bool {
	return u.Scheme == origin.Scheme && u.Host == origin.Host
}

// originOf reduces a URL to the scheme+host pair sameOrigin compares, so the
// comparison cannot be accidentally widened by a path or query.
func originOf(u *url.URL) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}
