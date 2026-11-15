package platform

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstalledCarriesOnlyCredentialAndRefusesForeignRedirect(t *testing.T) {
	var foreign atomic.Int32
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	other.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			foreign.Add(1)
		}
	}
	other.Start()
	defer other.Close()
	for _, redirect := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/v1/agents/installed" || r.Header.Get("Authorization") != "Bearer sr-test" {
				t.Errorf("unexpected installation request")
			}
			b, _ := io.ReadAll(r.Body)
			if len(b) > 0 {
				t.Errorf("acknowledgment carried a body")
			}
			if redirect {
				w.Header().Set("Location", other.URL)
				w.WriteHeader(http.StatusTemporaryRedirect)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := New(server.URL, server.URL).Installed(ctx, "sr-test")
		cancel()
		server.Close()
		if (err != nil) != redirect {
			t.Fatalf("redirect %v error %v", redirect, err)
		}
	}
	if foreign.Load() != 0 {
		t.Fatal("credential reached foreign origin")
	}
}
