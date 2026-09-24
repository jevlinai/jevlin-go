package main

// §4.5: walletNode's resolution order.

import (
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// TestWalletNodeResolutionOrder: flag beats env beats table; a
// configured chain id with no table row refuses with the message above
// and the stub node records zero requests.
func TestWalletNodeResolutionOrder(t *testing.T) {
	t.Run("flag wins over everything", func(t *testing.T) {
		got, err := walletNode("https://flag.example", envOf(map[string]string{walletNodeEnv: "https://env.example"}), config.DefaultChainID)
		if err != nil || got != "https://flag.example" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("env wins over the table", func(t *testing.T) {
		got, err := walletNode("", envOf(map[string]string{walletNodeEnv: "https://env.example"}), config.DefaultChainID)
		if err != nil || got != "https://env.example" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("table when neither flag nor env is set", func(t *testing.T) {
		got, err := walletNode("", noEnv, config.DefaultChainID)
		if err != nil || got != config.DefaultWalletNodes[config.DefaultChainID] {
			t.Fatalf("got %q, %v, want the table row for %q", got, err, config.DefaultChainID)
		}
	})
	t.Run("unknown chain id refuses with no request ever sent", func(t *testing.T) {
		node := newFakeNode(t, nodeConfig{chainID: "irrelevant"})
		_, err := walletNode("", noEnv, "some-chain-nobody-configured")
		if err == nil {
			t.Fatal("expected a refusal")
		}
		if !strings.Contains(err.Error(), "no default RPC node is known for chain") {
			t.Errorf("error should name the refusal: %v", err)
		}
		node.mu.Lock()
		bc, tq := node.broadcastCount, node.txQueries
		node.mu.Unlock()
		if bc != 0 || tq != 0 {
			t.Fatalf("no request should ever reach the node: broadcast=%d tx=%d", bc, tq)
		}
	})
}
