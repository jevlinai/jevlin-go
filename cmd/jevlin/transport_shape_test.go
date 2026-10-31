package main

// D1c's second review: routing every http.Transport through one shared
// dial seam must not change what any of them actually dials with. This
// table checks every production Transport this package builds against
// exactly what it had at 40c8b050220c114bc22ea29c6ebdaa6ee1d0e3e7 (the base
// commit for this whole PR): the two that leaned on http.DefaultTransport
// implicitly (leaving Transport nil) still carry every field
// DefaultTransport tunes — ForceAttemptHTTP2, TLSHandshakeTimeout,
// IdleConnTimeout, MaxIdleConns, ExpectContinueTimeout, its own Proxy.

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/networkfence"
)

func TestProductionTransportsMatchTheirPreD1cShapeExactly(t *testing.T) {
	t.Run("loginProbeTransport (was http.DefaultTransport)", func(t *testing.T) {
		networkfence.AssertTransportFieldsMatch(t, loginProbeTransport, freshDefaultTransportClone(t))
		assertDialer(t, loginProbeDialer, 30*time.Second, 30*time.Second)
	})
	t.Run("searchDefaultTransport (was http.DefaultTransport)", func(t *testing.T) {
		networkfence.AssertTransportFieldsMatch(t, searchDefaultTransport, freshDefaultTransportClone(t))
		assertDialer(t, searchDialer, 30*time.Second, 30*time.Second)
	})

}

// freshDefaultTransportClone is the reference shape every clone-based
// production Transport in this package is compared against: a Clone() of
// the real http.DefaultTransport, made independently of any production
// code path, so a bug in cloneDefaultTransport itself cannot also hide
// from this comparison.
func freshDefaultTransportClone(t *testing.T) *http.Transport {
	t.Helper()
	dt, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is a %T, not *http.Transport", http.DefaultTransport)
	}
	return dt.Clone()
}

func assertDialer(t *testing.T, d *net.Dialer, wantTimeout, wantKeepAlive time.Duration) {
	t.Helper()
	if d.Timeout != wantTimeout {
		t.Errorf("dialer Timeout = %v, want %v", d.Timeout, wantTimeout)
	}
	if d.KeepAlive != wantKeepAlive {
		t.Errorf("dialer KeepAlive = %v, want %v", d.KeepAlive, wantKeepAlive)
	}
}
