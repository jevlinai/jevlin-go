package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/wire"
)

// "joined this epoch" explains the region refusal of the epoch doctor is
// looking at. With one note per epoch, the slot's highest-numbered note is
// not necessarily that one: an operator pinned to an earlier epoch, or an
// AS whose target sits below a note left from a later one, must still see
// the current epoch's own verdict. The note is chosen once the epoch is
// known, through the real gather, not from hand-built facts.
func TestDoctorExplainsTheRefusalOfTheEpochItIsLookingAt(t *testing.T) {
	stateDir := doctorStateDir(t)
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRegionRefusal(7, 1042, auth.RegionUnverified); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRegionRefusal(7, 1043, auth.RegionIneligible); err != nil {
		t.Fatal(err)
	}
	pinned := uint64(1042)
	as := &stubAS{
		doc:    &wire.DiscoveryDocument{ChainID: "twilight-1", SlotID: "7"},
		status: &auth.EpochStatus{Joinable: true, Phase: "ACTIVE"},
	}
	f := gatherDoctorFactsFor(context.Background(), as, config.Mining{
		ASBaseURL: "https://as.example.com", StateDir: stateDir, SpoolDir: t.TempDir(),
		SlotID: 7, TargetEpoch: &pinned,
	}, config.Miner{}, realIntakeProbeOps())

	if !f.HasRegionRefusal || f.RegionRefusal.TargetEpoch != 1042 {
		t.Fatalf("doctor read the note %+v (have=%v), want epoch 1042's", f.RegionRefusal, f.HasRegionRefusal)
	}
	joined := verdictOf(assembleDoctor(f), "joined this epoch")
	if joined.Verdict != verdictNo || !strings.Contains(joined.Detail, "VPN") {
		t.Fatalf("joined this epoch = %s %q, want epoch 1042's hold explained", joined.Verdict, joined.Detail)
	}
}
