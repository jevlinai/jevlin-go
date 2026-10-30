// Package egress is the one place a request carrying a secret is checked
// against where that secret may go.
//
// Every secret this client holds was issued by one origin and is recorded
// with it: the sr- router key with the router it was stored for, the agents
// API key with the agents API that minted it, the AS's refresh authorization
// with the AS that issued it. A request carrying one of them is sent only
// when its destination is that origin — the first request, every retry and
// every redirect hop alike — and is refused otherwise, before a byte leaves
// the machine. The destination a command computed from its configuration is
// not evidence of where the secret belongs: configuration can change after
// the secret was issued, and the secret's record is what does not.
//
// Check is the rule. Do and Transport are the two ways a request reaches it:
// Do for a request this client builds and sends itself, Transport for a
// client a library drives (x/oauth2's token traffic, the DPoP transport).
// Both put the check on the transport, below every redirect a client follows
// and every retry a caller makes, so neither can be skipped by a code path
// that reaches the network another way through the same client.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// RefusedError is a request that would carry Secret somewhere other than the
// origin it is bound to. It names the two origins and nothing else.
type RefusedError struct {
	Secret    string
	Bound     string
	Requested string
}

func (e *RefusedError) Error() string {
	if e.Bound == "" {
		return fmt.Sprintf("%s is bound to no origin, so it is not sent to %s", e.Secret, e.Requested)
	}
	return fmt.Sprintf("%s belongs to %s and is not sent to %s", e.Secret, e.Bound, e.Requested)
}

// Origin is scheme://host[:port] in lower case, with the scheme's default
// port left out, so https://h and https://h:443 are one origin. Only http and
// https URLs with a host and no userinfo have one.
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not an absolute http(s) URL")
	}
	return originOf(u)
}

func originOf(u *url.URL) (string, error) {
	if u == nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errors.New("not an absolute http(s) URL without userinfo")
	}
	// Only an ASCII host has one spelling. Go's Unicode lowercasing folds
	// some letters onto ASCII ones ("İ" becomes "i") while net/http dials
	// the host's IDNA form, a different name: compared after folding, a
	// lookalike would pass as the recorded origin and be dialed as another.
	// A host written in Unicode, or percent-encoded into it, has no origin
	// here, so it is refused whatever it is compared with.
	if !asciiHost(u.Hostname()) {
		return "", errors.New("a host that is not plain ASCII")
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		return scheme + "://[" + host + "]", nil
	}
	return scheme + "://" + host, nil
}

// asciiHost reports whether every byte of host is printable ASCII.
func asciiHost(host string) bool {
	for i := 0; i < len(host); i++ {
		if host[i] < 0x21 || host[i] > 0x7e {
			return false
		}
	}
	return true
}

// Check refuses dest unless its origin is bound's. bound is the origin the
// secret was recorded with; an empty or unparseable bound refuses every
// destination, so a secret recorded with no origin goes nowhere.
func Check(secret, bound string, dest *url.URL) error {
	requested, err := originOf(dest)
	if err != nil {
		return &RefusedError{Secret: secret, Bound: bound, Requested: "a destination that is " + err.Error()}
	}
	want, err := Origin(bound)
	if bound == "" || err != nil {
		return &RefusedError{Secret: secret, Requested: requested}
	}
	if requested != want {
		return &RefusedError{Secret: secret, Bound: want, Requested: requested}
	}
	return nil
}

// Transport sends a request through Base only when Check passes for it. A
// client whose transport this is cannot send the secret elsewhere, whatever
// it is asked to send, redirected to or retried against.
type Transport struct {
	Base   http.RoundTripper
	Secret string
	Bound  string
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := Check(t.Secret, t.Bound, req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// Do sends req with client, its transport wrapped in a Transport bound to
// bound, so req and every redirect hop the client follows are checked. The
// client is not modified.
func Do(client *http.Client, req *http.Request, secret, bound string) (*http.Response, error) {
	if err := Check(secret, bound, req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	guarded := *client
	guarded.Transport = &Transport{Base: client.Transport, Secret: secret, Bound: bound}
	return guarded.Do(req) // #nosec G704 -- this is the guard: req's origin was just checked against bound, and every hop is checked again by the transport
}

// IsRefused reports whether err is, or wraps, a refusal.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}
