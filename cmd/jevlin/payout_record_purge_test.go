package main

import (
	"path/filepath"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// payout.json moved out of the state directory to beside credentials.json,
// so a purge, which used to take it with state/, has to name it.
func TestPurgeRemovesThePayoutRecordBesideTheCredential(t *testing.T) {
	s := installed(t)
	record := filepath.Join(s.home, payoutRecordFile)
	writeFileT(t, record, `{"address":"twilight1kl0dn0rtwk46h9zcmazyyrruta290crh93rnlh"}`)
	code, out, errOut := s.uninstall(t, tty(walletFixtureAddress(t)), true, &revokeRecorder{}, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if lexists(record) {
		t.Fatalf("purge left the payout record %s", record)
	}
}

// A configured intake outside the installation puts the record beside it,
// outside too: named with the other configured paths a purge reports.
func TestConfiguredStatePathsNameThePayoutRecordBesideTheIntake(t *testing.T) {
	intake := filepath.Join(t.TempDir(), "elsewhere", "intake")
	cfg := &config.Config{Miner: config.Miner{IntakeDir: intake}}
	want := filepath.Join(filepath.Dir(intake), payoutRecordFile)
	for _, c := range configuredStatePaths(cfg) {
		if c.path == want {
			return
		}
	}
	t.Fatalf("configuredStatePaths does not name %s", want)
}

// The claim record moved out of agent.json to beside credentials.json, so a
// purge, which took the old copy with state/, has to name it too; with a
// configured intake outside the installation it is reported with the rest.
func TestPurgeRemovesTheClaimRecordBesideTheCredential(t *testing.T) {
	s := installed(t)
	record := filepath.Join(s.home, claimRecordFile)
	writeFileT(t, record, `{"agent_id":"agent-1","claim_url":"https://platform.example/claim/AB12"}`)
	code, out, errOut := s.uninstall(t, tty(walletFixtureAddress(t)), true, &revokeRecorder{}, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if lexists(record) {
		t.Fatalf("purge left the claim record %s", record)
	}
	intake := filepath.Join(t.TempDir(), "elsewhere", "intake")
	want := filepath.Join(filepath.Dir(intake), claimRecordFile)
	for _, c := range configuredStatePaths(&config.Config{Miner: config.Miner{IntakeDir: intake}}) {
		if c.path == want {
			return
		}
	}
	t.Fatalf("configuredStatePaths does not name %s", want)
}
