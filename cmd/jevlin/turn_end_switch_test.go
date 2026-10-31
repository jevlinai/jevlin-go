package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// The sender holds the switch itself: a queued turn end is not sent when
// the installation's config does not turn the turn end on, whoever queued
// it, and the file is still removed. With the switch on the same file is
// sent, so the fixture is one the sender would otherwise send.
func TestAQueuedTurnEndIsNotSentWithTheSwitchOff(t *testing.T) {
	f := newTurnEndFixture(t)
	// Requests, not connections: the sender's transport keeps a connection
	// alive, so a second turn end can arrive on the first one's.
	var turns atomic.Int64
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/turns" {
			turns.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(router.Close)
	b, err := os.ReadFile(f.ownCfg) // #nosec G304 -- this test's own config
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.ReplaceAll(string(b), f.ownURL, router.URL))
	if err := os.WriteFile(f.ownCfg, b, 0o600); err != nil { // #nosec G703 -- the fixture's own config under t.TempDir
		t.Fatal(err)
	}
	env := envOf(map[string]string{apiKeyEnv: boundKey, "JEVLIN_CONFIG": f.ownCfg})
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", f.queue()}, env); code != exitOK || turns.Load() != 1 {
		t.Fatalf("with the switch on: exit %d, %d turn end(s) sent", code, turns.Load())
	}
	off := strings.Replace(string(b), "turn_end = true", "turn_end = false", 1)
	if off == string(b) {
		t.Fatal("the fixture's config does not turn the turn end on")
	}
	if err := os.WriteFile(f.ownCfg, []byte(off), 0o600); err != nil { // #nosec G703 -- the fixture's own config under t.TempDir
		t.Fatal(err)
	}
	queued := f.queue()
	if code := cmdTurnEnd([]string{"-config", f.ownCfg, "-file", queued}, env); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if n := turns.Load() - 1; n != 0 {
		t.Errorf("a turn end was sent with the switch off: %d request(s)", n)
	}
	if lexists(queued) {
		t.Errorf("the queued file was left: %s", queued)
	}
}
