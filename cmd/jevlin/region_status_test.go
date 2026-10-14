package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// A participant who enrolled by hand — `jevlin enroll`, then `jevlin join`
// — has no platform registration, and status used to return before the
// region line for them. The AS's refusal is theirs to read too (PR #85
// review): the line prints whether or not connect was ever run.
func TestStatusPrintsTheRegionLineWithoutAPlatformRegistration(t *testing.T) {
	note := auth.RegionRefusal{SlotID: 7, TargetEpoch: 1042, Code: auth.RegionIneligible, At: "2026-10-14T00:00:00Z"}
	for name, f := range map[string]agentIdentityFacts{
		"manual enrollment": {HasRegistration: false, Region: note, HasRegion: true},
		"platform agent":    {HasRegistration: true, Registration: auth.AgentRegistration{Status: "claimed"}, Region: note, HasRegion: true},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			renderAgentIdentity(f, &out, &errOut)
			if !strings.Contains(out.String(), "region: "+auth.RegionRefusalText(auth.RegionIneligible)) || !strings.Contains(out.String(), "epoch 1042") {
				t.Fatalf("no region line:\nstdout: %s\nstderr: %s", out.String(), errOut.String())
			}
		})
	}
	var out, errOut bytes.Buffer
	renderAgentIdentity(agentIdentityFacts{HasRegistration: false}, &out, &errOut)
	if strings.Contains(out.String(), "region:") {
		t.Fatal("a region line with no note")
	}
}
