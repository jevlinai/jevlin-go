package platform

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/egress"
)

// The platform key goes only to the agents API it was issued by: a client
// configured for another one refuses before dialing, on every call that
// carries the key.
func TestAKeyGoesOnlyToTheAgentsAPIItWasIssuedBy(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	c := New(srv.URL, srv.URL)
	elsewhere := Key{Value: "sr-issued-elsewhere", Origin: "https://agents-v1.jevlin.ai"}
	unbound := Key{Value: "sr-bound-nowhere"}
	ctx := context.Background()
	for _, key := range []Key{elsewhere, unbound} {
		for name, call := range map[string]func() error{
			"Status":    func() error { _, err := c.Status(ctx, "agent-1", key); return err },
			"Me":        func() error { _, err := c.Me(ctx, key); return err },
			"ClaimCode": func() error { _, err := c.ClaimCode(ctx, "agent-1", key); return err },
			"Enroll":    func() error { _, err := c.Enroll(ctx, "agent-1", key, "slot"); return err },
		} {
			if err := call(); !egress.IsRefused(err) {
				t.Errorf("%s with a key bound to %q: %v, want a refusal", name, key.Origin, err)
			}
		}
	}
	if n := conns.Load(); n != 0 {
		t.Fatalf("%d connection(s) carried a key to an agents API that did not issue it", n)
	}
	// The control: its own key reaches it.
	if _, err := c.Me(ctx, keyOf(c, "sr-own")); egress.IsRefused(err) || conns.Load() == 0 {
		t.Fatalf("its own key was refused or never sent: %v", err)
	}
}
