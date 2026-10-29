package promote

// Which source profile a promoted observation carries.
//
// The AS resolves provider_event_id through the lookup registered for the
// profile named beside it. Naming the wrong one sends a request id to be
// resolved in another provider's vocabulary: it resolves to nothing, the
// observation is rejected and never revisited. So the search router's profile
// is the only one this client sends, and an observation that names none is
// refused rather than given one.

import (
	"errors"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/observe"
	"github.com/jevlinai/jevlin-go/pkg/wire"
)

func completed(profile observe.Profile, id string) *observe.Observation {
	return &observe.Observation{
		Provider:     "search-router",
		Profile:      profile,
		GenerationID: id,
		StatusCode:   200,
		StartedAt:    time.Now().Add(-time.Second),
		FinishedAt:   time.Now(),
		Outcome:      observe.Complete(observe.TerminationDone),
	}
}

func TestASearchObservationIsPromotedUnderTheSearchProfile(t *testing.T) {
	rec, err := Build(completed(observe.ProfileSearchRouter, "id-1"), "crid-1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rec.SourceProfile != wire.SourceProfileSearchRouterV1 {
		t.Fatalf("source_profile = %q, want %q", rec.SourceProfile, wire.SourceProfileSearchRouterV1)
	}
	if rec.SourceData != nil {
		t.Errorf("the search profile's source-data allowlist is empty, and the record carries %v", rec.SourceData)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("the record does not validate: %v", err)
	}
}

// An observation built without a profile is refused: no record goes out under
// a profile nobody named, and OPENROUTER_V1 in particular is never sent.
func TestAnObservationWithNoProfileIsNotPromoted(t *testing.T) {
	for _, p := range []observe.Profile{observe.ProfileUnset, observe.Profile(7)} {
		rec, err := Build(completed(p, "id-1"), "crid-1")
		if !errors.Is(err, ErrNoSourceProfile) || rec != nil {
			t.Errorf("profile %d: Build = %+v, %v; want ErrNoSourceProfile and no record", p, rec, err)
		}
	}
}
