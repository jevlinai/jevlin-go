package main

import (
	"path/filepath"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// The registration journal moved beside credentials.json, and it can hold a
// platform key (the publication's key and, for a replacement, the previous
// one): a purge removes it like the credential, so no key survives one.
func TestPurgeRemovesTheRegistrationJournalBesideTheCredential(t *testing.T) {
	s := installed(t)
	journal := filepath.Join(s.home, registrationJournalFile)
	writeFileT(t, journal, `{"v":1}`)
	code, out, errOut := s.uninstall(t, tty(walletFixtureAddress(t)), true, &revokeRecorder{}, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if lexists(journal) {
		t.Fatalf("purge left the registration journal %s, which can hold a platform key", journal)
	}
}

// A configured intake outside the installation puts the journal beside it,
// outside too: it is named with the other configured paths, which a purge
// reports and leaves.
func TestConfiguredStatePathsNameTheJournalBesideTheIntake(t *testing.T) {
	intake := filepath.Join(t.TempDir(), "elsewhere", "intake")
	cfg := &config.Config{Miner: config.Miner{IntakeDir: intake}}
	want := filepath.Join(filepath.Dir(intake), registrationJournalFile)
	for _, c := range configuredStatePaths(cfg) {
		if c.path == want {
			return
		}
	}
	t.Fatalf("configuredStatePaths does not name %s", want)
}
