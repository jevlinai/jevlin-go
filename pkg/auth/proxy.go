package auth

// The proxy rule for the AS clients: an environment proxy is used only when
// its address is a loopback literal.
//
// Both AS-facing transports used to set Proxy: nil, on the rule "no
// environment proxy may silently interpose on AS identity". That rule is
// about a proxy somebody else configured — a corporate gateway, a VPN agent,
// a debugging proxy on another host — which would see the AS's name in a
// CONNECT and, if it terminated TLS, everything after. A proxy on this
// machine's loopback is not that: a local process can already read
// credentials.json, and over HTTPS a CONNECT proxy sees only the host name.
// And it is what Codex's permission profile requires: a sandboxed command's
// only route out is the loopback HTTP proxy Codex starts and names in
// HTTPS_PROXY, so a flush or a claim resume that a sandboxed search spawns
// could not reach the AS at all while these transports ignored it.
//
// So: the environment's proxy for the request, held to 127.0.0.0/8 and ::1
// as literal addresses. A name, even localhost, is refused, because what it
// resolves to is not this client's to vouch for; any other address is
// ignored exactly as every environment proxy was before, and the request
// goes direct. Only the AS's clients follow this rule. The chain client in
// cmd/jevlin/wallet_tx.go keeps Proxy: nil, for a node endpoint the operator
// named; the search and the platform clients clone http.DefaultTransport and
// keep its ProxyFromEnvironment.

import (
	"net"
	"net/http"
	"net/url"
)

// environmentProxy is where the environment's proxy is read from. It is
// http.ProxyFromEnvironment in production — which reads the environment once
// per process — and a variable only so a test can hand the package's own
// transports a proxy without depending on when the environment was read.
var environmentProxy = http.ProxyFromEnvironment

// loopbackProxy is the Proxy function of every AS-facing transport.
func loopbackProxy(req *http.Request) (*url.URL, error) {
	u, err := environmentProxy(req)
	if err != nil || u == nil {
		// An environment proxy that does not parse was ignored before
		// this rule existed, as every one was; it still is.
		return nil, nil
	}
	if !isLoopbackLiteral(u.Hostname()) {
		return nil, nil
	}
	return u, nil
}

// isLoopbackLiteral: is host an IP address literal in 127.0.0.0/8 or ::1?
func isLoopbackLiteral(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
