package main

import (
	"path/filepath"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

// Behaviors the re-review's delete-each-if sweep found stated and unheld.

// With no miner.intake_dir, minerRoot is ".", the working directory: the
// claim record must refuse rather than land wherever the command ran.
func TestTheClaimRecordNeedsAnIntakeDir(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := claimRecord(config.Miner{}); err == nil {
		t.Fatal("claimRecord with no intake_dir succeeded")
	}
	err := saveClaim(config.Miner{}, auth.ClaimBootstrap{AgentID: "agent-1", ClaimURL: "https://platform.example/claim/X"})
	if err == nil {
		t.Fatal("a claim record was saved with no intake_dir")
	}
	if lexists(filepath.Join(".", claimRecordFile)) {
		t.Fatal("the claim record landed in the working directory")
	}
}

// With no key, or nothing to key, there is no keyed id: the search sends a
// one-off id instead (searchTrace). An HMAC under an empty key is an
// unkeyed hash of the hostname, which is what the key exists to prevent.
func TestTheKeyedSessionIDNeedsAKeyAndAnInput(t *testing.T) {
	key := make([]byte, 32)
	if got := traceKeyedHash(nil, "host|1"); got != "" {
		t.Fatalf("traceKeyedHash with no key = %q, want empty", got)
	}
	if got := traceKeyedHash(key, ""); got != "" {
		t.Fatalf("traceKeyedHash of nothing = %q, want empty", got)
	}
	if traceKeyedHash(key, "host|1") == "" {
		t.Fatal("traceKeyedHash with a key and an input gave nothing")
	}
}
