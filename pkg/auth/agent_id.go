package auth

import (
	"errors"
	"regexp"
)

// The identifiers a record in the state directory carries are tokens, not
// prose: an agent id, a scope, a timestamp. The platform hands this client
// nothing else in those places, and a sandboxed command that can write the
// state directory could otherwise put a sentence there, which status,
// connect and doctor would print with jevlin's authority behind it:
// "SECURITY NOTICE: … run …". termtext's rule keeps a terminal from acting on
// a character; this keeps a record from saying anything. Each shape is held
// on the platform's wire before anything is journaled, and on a record's load
// and save, so a record this client wrote always passes.
//
// tokenShape is an ASCII letter or digit, then letters, digits, '.', '_', ':'
// and '-'. The platform's agent ids are UUIDs and its scopes words ("search",
// "credits"); ':' admits a namespaced name. No space, no other
// punctuation, no non-ASCII.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// timestampShape is an RFC 3339 time, held to its alphabet rather than
// parsed: the field is shown, never computed with, and a stricter parse would
// refuse a platform that sends fractional seconds or an offset.
var timestampShape = regexp.MustCompile(`^[0-9][0-9TZ:.+-]*$`)

const (
	agentIDMax   = 128
	scopeMax     = 64
	timestampMax = 40
	claimCodeMax = 128
)

var claimCodeShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidAgentID is the shape an agent id must have before this client puts it
// in a URL path or a record: a token (tokenShape) of at most agentIDMax bytes.
// The client builds GET /v1/agents/{id} and POST /v1/agents/{id}/claim-code
// from it, and "me" is the router's own self-lookup, which answers for
// whatever agent the key belongs to, so a record naming "me" passed every
// check that asks the platform about the record's agent and was never
// rebuilt. A token cannot be "." or "..", hold a separator, or say a sentence.
func ValidAgentID(id string) error {
	switch {
	case id == "":
		return errors.New("auth: agent id is empty")
	case id == "me":
		return errors.New("auth: agent id names a route, not an agent")
	case len(id) > agentIDMax || !tokenShape.MatchString(id):
		return errors.New("auth: agent id is not an identifier (letters, digits, '.', '_', ':' and '-')")
	}
	return nil
}

// ValidClaimCode is the shape of a claim code the platform mints: letters,
// digits, '-' and '_', at most claimCodeMax bytes, or empty where the field
// is optional. A person is told to type the code, and a connect or status
// prints it, so it is held to a token like the agent id: it cannot hold a
// space, and so cannot say a sentence.
func ValidClaimCode(code string) error {
	if code != "" && (len(code) > claimCodeMax || !claimCodeShape.MatchString(code)) {
		return errors.New("auth: a claim code is not a token (letters, digits, '-' and '_')")
	}
	return nil
}

// ValidScope is the shape of a scope the platform grants.
func ValidScope(scope string) error {
	if len(scope) > scopeMax || !tokenShape.MatchString(scope) {
		return errors.New("auth: a scope is not an identifier")
	}
	return nil
}

// ValidTimestamp is the shape of a time the platform reports; empty is
// allowed, since every such field is optional.
func ValidTimestamp(ts string) error {
	if ts != "" && (len(ts) > timestampMax || !timestampShape.MatchString(ts)) {
		return errors.New("auth: a time is not an RFC 3339 timestamp")
	}
	return nil
}

// checkAgentRegistrationShapes holds every identifier in rec to its shape;
// LoadAgentRegistration and SaveAgentRegistration both call it.
func checkAgentRegistrationShapes(rec AgentRegistration) error {
	if err := ValidAgentID(rec.AgentID); err != nil {
		return err
	}
	switch rec.Status {
	case "unclaimed", "claimed", "expired":
	default:
		return errors.New("auth: agent status is not unclaimed, claimed or expired")
	}
	for _, sc := range rec.Scopes {
		if err := ValidScope(sc); err != nil {
			return err
		}
	}
	return ValidTimestamp(rec.ClaimExpiresAt)
}
