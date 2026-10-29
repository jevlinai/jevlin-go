// Package promote turns a served search's observation into a
// ProviderObservationV1 record (contract §54 promotion rule, §48–§57
// envelope). It is pure data mapping: no I/O, no network, no clock
// beyond what the observation already carries.
//
// The privacy line is absolute here — an Observation contains only
// response-side metadata, and this package copies a fixed set of those
// fields. There is no path by which prompt or completion content could
// enter a record.
package promote

import (
	"errors"
	"strconv"

	"github.com/jevlinai/jevlin-go/pkg/observe"
	"github.com/jevlinai/jevlin-go/pkg/wire"
)

// ErrNotPromotable means the observation is not structurally eligible
// (§54). It is an ordinary outcome, not a failure: most observations of
// a healthy proxy are promotable, and the rest are simply dropped.
var ErrNotPromotable = errors.New("promote: observation is not structurally eligible")

// Eligible reports the §54 rule: Outcome == COMPLETE and a non-empty
// provider_event_id (the router's request id). Whether it qualifies
// economically is the AS's call.
func Eligible(obs *observe.Observation) bool {
	if obs == nil || obs.GenerationID == "" {
		return false
	}
	_, complete := obs.Outcome.Termination()
	return complete
}

// Build maps an eligible observation into the wire record. clientRecordID
// must already be minted and stable across retries (§49).
func Build(obs *observe.Observation, clientRecordID string) (*wire.ProviderObservationV1, error) {
	if !Eligible(obs) {
		return nil, ErrNotPromotable
	}
	if clientRecordID == "" {
		return nil, errors.New("promote: client_record_id is required")
	}
	termination, _ := obs.Outcome.Termination()
	profile, err := sourceProfileFor(obs.Profile)
	if err != nil {
		return nil, err
	}

	rec := &wire.ProviderObservationV1{
		Version:         wire.ObservationVersion,
		ClientRecordID:  clientRecordID,
		SourceProfile:   profile,
		ProviderEventID: obs.GenerationID,
		ResolvedModel:   obs.ResolvedModel,
		Streaming:       obs.Streaming,
		StatusCode:      obs.StatusCode,
		FinishReason:    obs.FinishReason,
		Outcome: wire.OutcomeV1{
			Type:        wire.OutcomeComplete,
			Termination: string(termination),
		},
		SawDone:          obs.SawDone,
		SawUsage:         obs.SawUsage,
		SawFinishReason:  obs.SawFinishReason,
		ParseDegraded:    obs.ParseDegraded,
		MetadataConflict: obs.MetadataConflict,
	}
	if !obs.StartedAt.IsZero() {
		rec.StartedAt = obs.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if !obs.FinishedAt.IsZero() {
		rec.FinishedAt = obs.FinishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	// §52: provider-reported values only, as decimal strings. Absence of
	// a usage field is not an error, and nothing is estimated locally.
	if obs.Usage != nil {
		rec.Usage = &wire.UsageV1{
			PromptTokens:     strconv.FormatUint(obs.Usage.PromptTokens, 10),
			CompletionTokens: strconv.FormatUint(obs.Usage.CompletionTokens, 10),
			TotalTokens:      strconv.FormatUint(obs.Usage.TotalTokens, 10),
		}
	}
	// §56: SEARCH_ROUTER_V1's source-data allowlist is empty, so no
	// source_data object is sent; the AS would reject one.
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	return rec, nil
}

// ErrNoSourceProfile is an observation that names no source profile.
var ErrNoSourceProfile = errors.New("promote: the observation names no source profile")

// sourceProfileFor names the wire source profile for an observation. The
// profile is decided by the route the client called (cmd/jevlin's
// intakeRecord.observation names observe.ProfileSearchRouter, because search
// only ever calls /v1/search), never inferred from a response body: guessing
// it wrong sends an identifier to be resolved in another provider's
// vocabulary, which rejects honest work. The search router's profile is the
// only one this client produces; any other is refused rather than sent.
func sourceProfileFor(p observe.Profile) (string, error) {
	if p == observe.ProfileSearchRouter {
		return wire.SourceProfileSearchRouterV1, nil
	}
	return "", ErrNoSourceProfile
}
