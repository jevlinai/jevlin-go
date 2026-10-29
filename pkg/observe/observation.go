// Package observe is the Observation type: the metadata this client keeps
// about one served search, and nothing of its content. It imports no mining
// package; promotion (pkg/mining/promote) turns an Observation into the wire
// record the AS receives.
package observe

import "time"

// Profile names the provider vocabulary an observation's identifier belongs
// to. It is decided by the route the client called, never inferred from a
// response body. The zero value names none, and promotion refuses it: an
// observation built without a profile is a mistake, not a default.
type Profile uint8

const (
	// ProfileUnset is the zero value: no profile was named.
	ProfileUnset Profile = iota
	// ProfileSearchRouter is the search router's /v1/search answer, whose
	// request id the AS resolves under SEARCH_ROUTER_V1.
	ProfileSearchRouter
)

// Termination says how the proxy FINISHED observing (Complete): these are
// facts about watching, never reward-eligibility claims.
type Termination string

const (
	TerminationDone               Termination = "done"
	TerminationEOFWithoutDone     Termination = "eof_without_done"
	TerminationClientDisconnect   Termination = "client_disconnect"
	TerminationUpstreamErrorEvent Termination = "upstream_error_event"
)

// AbandonReason says why the proxy GAVE UP watching (Abandoned). An
// abandoned observation is never promotable — that much is not D-19, because
// the proxy is stating it did not finish watching.
type AbandonReason string

const (
	AbandonObserverOverrun  AbandonReason = "observer_overrun"
	AbandonBodyTooLarge     AbandonReason = "body_too_large"
	AbandonEncodedBody      AbandonReason = "encoded_body"
	AbandonMaxDepthExceeded AbandonReason = "max_depth_exceeded"
	// AbandonMalformedBody covers §6.3's top-level framing violations on the
	// non-streaming path: trailing content, a second top-level value, or an
	// incomplete value at EOF. Abandoned rather than tolerated.
	AbandonMalformedBody AbandonReason = "malformed_body"
)

// Outcome is exactly one of two shapes, never blended: "the proxy watched
// this finish" and "the proxy gave up watching" are different facts, and
// only the first can ever be promoted (D-19). A flattened enum would leak a
// false equivalence into metric labels and test names.
type Outcome struct {
	complete    bool
	termination Termination
	reason      AbandonReason
}

func Complete(t Termination) Outcome    { return Outcome{complete: true, termination: t} }
func Abandoned(r AbandonReason) Outcome { return Outcome{reason: r} }

func (o Outcome) IsComplete() bool { return o.complete }

// Termination is meaningful only for Complete outcomes.
func (o Outcome) Termination() (Termination, bool) {
	if !o.complete {
		return "", false
	}
	return o.termination, true
}

// Reason is meaningful only for Abandoned outcomes.
func (o Outcome) Reason() (AbandonReason, bool) {
	if o.complete {
		return "", false
	}
	return o.reason, true
}

func (o Outcome) String() string {
	if o.complete {
		return "complete:" + string(o.termination)
	}
	return "abandoned:" + string(o.reason)
}

// Usage is the provider-reported token accounting. Never locally estimated.
type Usage struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
}

// Observation is everything the proxy retains about one exchange: metadata
// only, provider-facing only. It carries no scope, draw, or payout field and
// never will — pairing with mining context is the wiring layer's job (§4.2).
type Observation struct {
	Provider string
	// Profile is the response shape this exchange was observed under, which
	// is decided by the ROUTE the agent called — /v1/search is a search
	// router request by definition, not by inspection. It is carried through
	// to promotion because the wire record's source_profile must say which
	// provider vocabulary produced the identifier in ProviderEventID, and
	// inferring that later from a payload shape would be guessing at
	// identity from evidence that does not establish it.
	Profile       Profile
	GenerationID  string
	ResolvedModel string
	Usage         *Usage // nil when the provider reported none
	StatusCode    int
	FinishReason  string
	ErrorType     string
	ErrorCode     string
	StartedAt     time.Time // request dispatched upstream
	FinishedAt    time.Time // observation terminal
	Streaming     bool
	Outcome       Outcome

	SawDone          bool
	SawUsage         bool
	SawFinishReason  bool
	ParseDegraded    bool // ≥1 event skipped (oversize or malformed)
	MetadataConflict bool // a later chunk disagreed with the first on id/model
}
