package main

// `jevlin decide --stdin`: typed questions about a state, answered by a
// decision model with probabilities. `jevlin decide --models` lists the
// models this router offers.
//
// It is fetch --stdin's sibling and shares its transport: the stored key,
// the router URL from the config, the deadline, the redirect policy, the
// user agent and the bounded read. The request is strict about its own shape
// and says nothing about the content it carries: the state, each question and
// the vendor's options belong to the caller and the router, so they go on the
// wire byte for byte. The answer is one envelope with the machine header. A
// decision records nothing on this machine, and is one billed request that
// this client never repeats.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Stable client codes for a decide request this client refused to send,
// beside the ones the other machine requests share with it (unknown_field,
// invalid_max_chars and the rest of the reading). All are exit 2 /
// usage_error / fix_input.
const (
	codeMissingModel     = "missing_model"
	codeInvalidModel     = "invalid_model"
	codeMissingState     = "missing_state"
	codeMissingQuestions = "missing_questions"
	codeInvalidQuestions = "invalid_questions"
	codeInvalidOptions   = "invalid_options"
	codeInvalidSources   = "invalid_sources"
)

// The router's codes decide answers with its own advice.
const (
	// decisionsUnavailableCode is the router's 403 for an account that cannot
	// call this model: unclaimed, or with neither credits nor its own key.
	decisionsUnavailableCode = "decisions_unavailable"
	// decisionsNotSupportedCode is this client's name for a router without
	// the endpoints: it answers them with 404 or 405.
	decisionsNotSupportedCode = "decisions_not_supported"
	decisionsDisabledCode     = "decisions_disabled"
	modelUnavailableCode      = "model_unavailable"
	upstreamFailedCode        = "upstream_failed"
	unknownModelCode          = "unknown_model"
)

// decideModelPattern is the router's model id: "vendor/model".
var decideModelPattern = regexp.MustCompile(`^[a-z0-9-]+/[A-Za-z0-9._-]+$`)

// decideRequestFields is every top-level key a v1 request may carry, spelled
// exactly.
var decideRequestFields = map[string]bool{
	"version": true, "model": true, "state": true, "questions": true,
	"options": true, "sources": true, "max_chars": true,
}

// decideBody is what goes to POST /v1/decisions: the request without its
// version, and without any field the caller left out.
type decideBody struct {
	Model     json.RawMessage
	State     json.RawMessage
	Questions json.RawMessage
	Options   json.RawMessage
	Sources   json.RawMessage
	MaxChars  json.RawMessage
}

// encode writes the body with each value exactly as the caller wrote it. The
// router pins the caller's state bytes, and json.Marshal would compact them
// and escape their < > and &.
func (b decideBody) encode() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for _, f := range []struct {
		key   string
		value json.RawMessage
	}{
		{"model", b.Model},
		{"state", b.State},
		{"questions", b.Questions},
		{"options", b.Options},
		{"sources", b.Sources},
		{"max_chars", b.MaxChars},
	} {
		if len(f.value) == 0 {
			continue
		}
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('"')
		buf.WriteString(f.key)
		buf.WriteString(`":`)
		buf.Write(f.value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// decodeMachineDecideRequest reads exactly one v1 decide request from r. It
// checks the request's shape and nothing inside the state, a question or the
// options, whose rules are the router's.
func decodeMachineDecideRequest(r io.Reader) (decideBody, *inputError) {
	// The shared reader reads "version" without regard to case, so a request
	// whose only version key is misspelled would be told its version is
	// unsupported. The copy lets that one answer be corrected; it holds no more
	// than the reader's own bound.
	var seen bytes.Buffer
	raw, ierr := readMachineRequest(io.TeeReader(r, &seen), "decide")
	if ierr != nil {
		if ierr.Code == codeUnsupportedVersion && misspelledVersion(seen.Bytes()) {
			return decideBody{}, inputErrorf(codeUnknownField,
				"the request carries a field this version does not accept")
		}
		return decideBody{}, ierr
	}
	// Through a map rather than strictDecode: encoding/json matches a struct's
	// fields without regard to case, so a "STATE" beside "state" would pass
	// and replace it, and a billed decision would answer the wrong state.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return decideBody{}, inputErrorf(codeInvalidJSON, "the request must be exactly one JSON object")
	}
	for name := range fields {
		if !decideRequestFields[name] {
			return decideBody{}, inputErrorf(codeUnknownField,
				"the request carries a field this version does not accept")
		}
	}
	body := decideBody{
		Model:     fields["model"],
		State:     fields["state"],
		Questions: fields["questions"],
		Options:   fields["options"],
		Sources:   fields["sources"],
		MaxChars:  fields["max_chars"],
	}
	if ierr := validateDecideModel(body.Model); ierr != nil {
		return decideBody{}, ierr
	}
	if len(body.State) == 0 || isJSONNull(body.State) {
		return decideBody{}, inputErrorf(codeMissingState, `the request needs "state", and it must not be null`)
	}
	if ierr := validateDecideQuestions(body.Questions); ierr != nil {
		return decideBody{}, ierr
	}
	if len(body.Options) > 0 && !isJSONObject(body.Options) {
		return decideBody{}, inputErrorf(codeInvalidOptions, `"options" must be an object`)
	}
	if len(body.Sources) > 0 {
		if ierr := validateDecideSources(body.Sources); ierr != nil {
			return decideBody{}, ierr
		}
	}
	if len(body.MaxChars) > 0 {
		if _, ierr := validateFetchMaxChars(body.MaxChars); ierr != nil {
			return decideBody{}, ierr
		}
	}
	return body, nil
}

// misspelledVersion reports whether raw holds a key encoding/json would read
// as "version" that is not spelled exactly so. Only that key: a future version
// is told its version is unsupported, not lectured about its new fields.
func misspelledVersion(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimUTF8BOM(raw), &fields) != nil {
		return false
	}
	for name := range fields {
		if name != "version" && strings.EqualFold(name, "version") {
			return true
		}
	}
	return false
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func validateDecideModel(raw json.RawMessage) *inputError {
	if len(raw) == 0 || isJSONNull(raw) {
		return inputErrorf(codeMissingModel, `the request needs "model", as jevlin decide --models lists it`)
	}
	var model string
	if json.Unmarshal(raw, &model) != nil || !decideModelPattern.MatchString(model) {
		return inputErrorf(codeInvalidModel, `"model" must be "vendor/model", as jevlin decide --models lists it`)
	}
	return nil
}

func validateDecideQuestions(raw json.RawMessage) *inputError {
	if len(raw) == 0 || isJSONNull(raw) {
		return inputErrorf(codeMissingQuestions, `the request needs "questions", an object naming each question`)
	}
	var questions map[string]json.RawMessage
	if !isJSONObject(raw) || json.Unmarshal(raw, &questions) != nil {
		return inputErrorf(codeInvalidQuestions, `"questions" must be an object naming each question`)
	}
	if len(questions) == 0 {
		return inputErrorf(codeInvalidQuestions, `"questions" must name at least one question`)
	}
	// In sorted order, so the same request is always told about the same key.
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !isJSONObject(questions[name]) {
			return inputErrorf(codeInvalidQuestions, fmt.Sprintf(`"questions" entry %q must be an object`, name))
		}
	}
	return nil
}

// validateDecideSources reads the keys through a map rather than a struct:
// encoding/json matches a struct's fields without regard to case, and a
// "SOURCE_ID" passed here would be forwarded as written and refused there.
func validateDecideSources(raw json.RawMessage) *inputError {
	var entries []json.RawMessage
	if isJSONNull(raw) || json.Unmarshal(raw, &entries) != nil {
		return inputErrorf(codeInvalidSources, `"sources" must be an array of objects`)
	}
	if len(entries) == 0 || len(entries) > fetchPagesMax {
		return inputErrorf(codeInvalidSources, fmt.Sprintf(`"sources" must hold 1 to %d entries`, fetchPagesMax))
	}
	for i, e := range entries {
		var fields map[string]json.RawMessage
		if !isJSONObject(e) || json.Unmarshal(e, &fields) != nil || len(fields) != 1 {
			return inputErrorf(codeInvalidSources, fmt.Sprintf(`"sources" entry %d must be an object with exactly a "source_id"`, i))
		}
		var id string
		value, named := fields["source_id"]
		if !named || isJSONNull(value) || json.Unmarshal(value, &id) != nil || !sourceIDPattern.MatchString(id) {
			return inputErrorf(codeInvalidSources,
				fmt.Sprintf(`"sources" entry %d: "source_id" must be "s_" followed by 32 lowercase hex digits, as a search returned it`, i))
		}
	}
	return nil
}

// ── the router's answer ─────────────────────────────────────────────────

// routerDecideResponse is the router's answer to /v1/decisions, decoded
// permissively like search's (invariant 6). Answers stays raw per question:
// its values are a union (noul, choice, score, refusal) the router may add
// to, and this client neither drops nor invents any of it. A nil map is a 2xx
// with answers absent or null, which is no answer to a decision.
type routerDecideResponse struct {
	RequestID     string                     `json:"request_id"`
	Model         string                     `json:"model"`
	ModelResolved string                     `json:"model_resolved,omitempty"`
	Answers       map[string]json.RawMessage `json:"answers"`
	Citations     []routerDecideCitation     `json:"citations"`
	Sources       []routerDecideSource       `json:"sources,omitempty"`
	Usage         *routerDecideUsage         `json:"usage,omitempty"`
}

type routerDecideCitation struct {
	URL      string   `json:"url"`
	Title    string   `json:"title,omitempty"`
	Excerpts []string `json:"excerpts,omitempty"`
}

// routerDecideSource and routerDecideUsage use pointers because 0 and false
// are answers and absence is another.
type routerDecideSource struct {
	SourceID   string `json:"source_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	Chars      *int   `json:"chars,omitempty"`
	Cached     *bool  `json:"cached,omitempty"`
	CostMicros *int64 `json:"cost_micros,omitempty"`
}

type routerDecideUsage struct {
	InputTokens     *int64 `json:"input_tokens,omitempty"`
	OutputTokens    *int64 `json:"output_tokens,omitempty"`
	GroundedCalls   *int64 `json:"grounded_calls,omitempty"`
	CostMicros      *int64 `json:"cost_micros,omitempty"`
	PagesCostMicros *int64 `json:"pages_cost_micros,omitempty"`
	CostSource      string `json:"cost_source,omitempty"`
	BYOK            *bool  `json:"byok,omitempty"`
	LatencyMS       *int64 `json:"latency_ms,omitempty"`
}

// routerDecideModels is the router's answer to /v1/decisions/models. Models
// is a pointer so a 2xx with no list is told apart from an empty one.
type routerDecideModels struct {
	Models *[]routerDecideModel `json:"models"`
}

// routerDecideModel shares its nested cost and free types with
// machineDecideModel, so the whole entry converts in one step.
type routerDecideModel struct {
	ID          string           `json:"id"`
	Vendor      string           `json:"vendor"`
	VendorModel string           `json:"vendor_model"`
	Cost        *decideModelCost `json:"cost,omitempty"`
	Free        *decideModelFree `json:"free,omitempty"`
	Grounded    *bool            `json:"grounded,omitempty"`
	Available   *bool            `json:"available,omitempty"`
}

// decideModelCost is in micro-dollars; the router omits a field that is zero.
type decideModelCost struct {
	PerRequestMicros    *int64 `json:"per_request_micros,omitempty"`
	PerPromptKtokMicros *int64 `json:"per_prompt_ktok_micros,omitempty"`
	PerSearchCallMicros *int64 `json:"per_search_call_micros,omitempty"`
}

type decideModelFree struct {
	RequestsPerDay *int64 `json:"requests_per_day,omitempty"`
}

// The machine types are the router's, field for field, converted rather than
// copied so a field either gains stops the build.
type machineDecideCitation struct {
	URL      string   `json:"url"`
	Title    string   `json:"title,omitempty"`
	Excerpts []string `json:"excerpts,omitempty"`
}

type machineDecideSource struct {
	SourceID   string `json:"source_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	Chars      *int   `json:"chars,omitempty"`
	Cached     *bool  `json:"cached,omitempty"`
	CostMicros *int64 `json:"cost_micros,omitempty"`
}

type machineDecideUsage struct {
	InputTokens     *int64 `json:"input_tokens,omitempty"`
	OutputTokens    *int64 `json:"output_tokens,omitempty"`
	GroundedCalls   *int64 `json:"grounded_calls,omitempty"`
	CostMicros      *int64 `json:"cost_micros,omitempty"`
	PagesCostMicros *int64 `json:"pages_cost_micros,omitempty"`
	CostSource      string `json:"cost_source,omitempty"`
	BYOK            *bool  `json:"byok,omitempty"`
	LatencyMS       *int64 `json:"latency_ms,omitempty"`
}

type machineDecideResult struct {
	RequestID     string                     `json:"request_id,omitempty"`
	Model         string                     `json:"model,omitempty"`
	ModelResolved string                     `json:"model_resolved,omitempty"`
	Answers       map[string]json.RawMessage `json:"answers"`
	Citations     []machineDecideCitation    `json:"citations"`
	Sources       []machineDecideSource      `json:"sources,omitempty"`
	Usage         *machineDecideUsage        `json:"usage,omitempty"`
}

type machineDecideModel struct {
	ID          string           `json:"id"`
	Vendor      string           `json:"vendor"`
	VendorModel string           `json:"vendor_model"`
	Cost        *decideModelCost `json:"cost,omitempty"`
	Free        *decideModelFree `json:"free,omitempty"`
	Grounded    *bool            `json:"grounded,omitempty"`
	Available   *bool            `json:"available,omitempty"`
}

type machineDecideModels struct {
	Models []machineDecideModel `json:"models"`
}

// decideEnvelope's Result is a *machineDecideResult or a *machineDecideModels.
type decideEnvelope struct {
	machineHeader
	HTTPStatus   int           `json:"http_status,omitempty"`
	RequestID    string        `json:"request_id,omitempty"`
	RetryAfterMS *int64        `json:"retry_after_ms,omitempty"`
	Result       any           `json:"result,omitempty"`
	Error        *machineError `json:"error,omitempty"`
}

// decideOutcome is one call's result, as a value the envelope is built from.
// Decision or Models is set on a success, whichever was asked. BodyRetryable
// is the router's own "retryable" on an error body.
type decideOutcome struct {
	Fault         searchFault
	Err           error
	HTTPStatus    int
	RouterErr     searchHostError
	HasRouterErr  bool
	BodyRetryable bool
	RetryAfterMS  int64
	HasRetryAfter bool
	RequestID     string
	Decision      *routerDecideResponse
	Models        *routerDecideModels
}

// performDecide sends one POST under ctx's deadline. Never retried: a
// decision is billed, and nothing the router says licenses a second one.
func performDecide(ctx context.Context, now func() time.Time, endpoint, key string, body []byte) decideOutcome {
	attempt, err := postRouterBytes(ctx, routerClient(), endpoint, key, body, nil)
	return readDecideAnswer(ctx, now, attempt, err, decodeDecision)
}

// performDecideModels sends one GET under ctx's deadline.
func performDecideModels(ctx context.Context, now func() time.Time, endpoint, key string) decideOutcome {
	attempt, err := getRouter(ctx, routerClient(), endpoint, key)
	return readDecideAnswer(ctx, now, attempt, err, decodeDecideModels)
}

func decodeDecision(raw []byte, out *decideOutcome) error {
	var resp routerDecideResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return errBodyNotJSON
	}
	if resp.Answers == nil {
		return errors.New("the router's decision response carries no answers")
	}
	out.Decision = &resp
	out.RequestID = strings.TrimSpace(resp.RequestID)
	return nil
}

func decodeDecideModels(raw []byte, out *decideOutcome) error {
	var resp routerDecideModels
	if err := json.Unmarshal(raw, &resp); err != nil {
		return errBodyNotJSON
	}
	if resp.Models == nil {
		return errors.New("the router's models response carries no models")
	}
	out.Models = &resp
	return nil
}

// readDecideAnswer is performFetch's reading of one attempt, with decode
// standing in for the shape of a success.
func readDecideAnswer(ctx context.Context, now func() time.Time, attempt routerAttempt, err error, decode func([]byte, *decideOutcome) error) decideOutcome {
	var out decideOutcome
	if err != nil {
		out.Err = err
		out.Fault = contextFault(ctx, err, faultTransport)
		return out
	}
	out.HTTPStatus = attempt.Status
	if ms, ok := retryAfterMS(attempt.Header, now()); ok {
		out.RetryAfterMS, out.HasRetryAfter = ms, true
	}
	if attempt.BodyErr != nil {
		if f := contextFault(ctx, attempt.BodyErr, faultNone); f != faultNone {
			out.Err, out.Fault = attempt.BodyErr, f
			return out
		}
	}
	if attempt.Status < 200 || attempt.Status > 299 {
		out.Fault = faultRouterStatus
		if attempt.BodyErr == nil {
			if e, ok := decodeSearchHostError(attempt.Raw); ok {
				out.RouterErr, out.HasRouterErr = e, true
				var flag struct {
					Retryable bool `json:"retryable"`
				}
				if json.Unmarshal(attempt.Raw, &flag) == nil {
					out.BodyRetryable = flag.Retryable
				}
			}
		}
		return out
	}
	if attempt.BodyErr != nil {
		out.Err = attempt.BodyErr
		out.Fault = faultInvalidResponse
		if errors.Is(attempt.BodyErr, errBodyOversized) {
			out.Fault = faultOversized
		}
		return out
	}
	if err := singleJSONObject(attempt.Raw); err != nil {
		out.Err, out.Fault = err, faultInvalidResponse
		return out
	}
	// The answers are carried as raw bytes, and so skip the replacement of
	// invalid UTF-8 that decoding into a string does: without this check they
	// could put invalid UTF-8 in the envelope.
	if !utf8.Valid(attempt.Raw) {
		out.Err, out.Fault = errBodyNotJSON, faultInvalidResponse
		return out
	}
	if err := decode(attempt.Raw, &out); err != nil {
		out.Err, out.Fault = err, faultInvalidResponse
		return out
	}
	// The body's id wins, as it does for a search; the header stands in when
	// the body has none. Nothing here depends on the id, so an answer with
	// neither is still an answer, and simply carries no id.
	if out.RequestID == "" {
		out.RequestID = strings.TrimSpace(attempt.Header.Get("X-Request-Id"))
	}
	return out
}

// classifyDecide is search's classification, with the router's answers that
// only a decision gets read first. By exact code and status, never by message.
func classifyDecide(out decideOutcome) searchClassification {
	if out.Fault != faultRouterStatus {
		return classifySearch(searchOutcome{Fault: out.Fault})
	}
	var code string
	if out.HasRouterErr {
		code = out.RouterErr.Code
	}
	switch {
	case out.HTTPStatus == http.StatusForbidden && code == decisionsUnavailableCode:
		// As for fetch's 403: the account, which the claim workflow is about,
		// is what needs attention, and the closed action set has nothing nearer.
		return searchClassification{exitClientErr, decisionsUnavailableCode, false, actionConnect}
	case out.HTTPStatus == http.StatusNotFound, out.HTTPStatus == http.StatusMethodNotAllowed:
		return searchClassification{exitClientErr, decisionsNotSupportedCode, false, actionReport}
	case out.HTTPStatus == http.StatusServiceUnavailable && code == decisionsDisabledCode:
		// This router has decisions switched off, which no retry changes. Exit 3
		// as for decisions_not_supported and as fetch_disabled is, so the two
		// agent-facing commands agree.
		return searchClassification{exitClientErr, decisionsDisabledCode, false, actionReport}
	case out.HTTPStatus == http.StatusServiceUnavailable && code == modelUnavailableCode:
		// The platform holds no working key for this model, and no retry
		// changes that.
		return searchClassification{exitServerErr, modelUnavailableCode, false, actionReport}
	case out.HTTPStatus == http.StatusServiceUnavailable && code == upstreamFailedCode:
		// A decision is billed, so only the router's own word makes it safe
		// to send again; absent, it is not retryable.
		if out.BodyRetryable {
			return searchClassification{exitServerErr, upstreamFailedCode, true, actionRetry}
		}
		return searchClassification{exitServerErr, upstreamFailedCode, false, actionReport}
	}
	return classifyRouterStatus(searchOutcome{
		HTTPStatus: out.HTTPStatus, RouterErr: out.RouterErr, HasRouterErr: out.HasRouterErr,
	})
}

func decideEnvelopeOf(out decideOutcome) (decideEnvelope, int) {
	c := classifyDecide(out)
	env := decideEnvelope{
		machineHeader: newMachineHeader("decide", c.ExitCode, c.Code, c.Retryable, c.Action),
		HTTPStatus:    out.HTTPStatus,
	}
	if out.HasRetryAfter && (c.Retryable || out.HTTPStatus == http.StatusTooManyRequests) {
		ms := out.RetryAfterMS
		env.RetryAfterMS = &ms
	}
	if out.Fault == faultNone {
		env.RequestID = out.RequestID
		if out.Models != nil {
			env.Result = machineModelsOf(out.Models)
		} else {
			env.Result = machineDecisionOf(out.Decision, out.RequestID)
		}
		return env, c.ExitCode
	}
	if c.Code == unknownModelCode {
		// The router's message points at its own route; an agent runs this.
		env.Error = clientMessage(errors.New("this router does not offer that model; jevlin decide --models lists the ones it does"))
		return env, c.ExitCode
	}
	if out.HasRouterErr {
		env.Error = routerMessage(out.RouterErr.Error)
	}
	if env.Error == nil {
		switch c.Code {
		case decisionsNotSupportedCode:
			env.Error = clientMessage(errors.New("this router has no decisions endpoint"))
		case decisionsDisabledCode:
			env.Error = clientMessage(errors.New("this router has decisions switched off"))
		default:
			env.Error = clientMessage(out.Err)
		}
	}
	return env, c.ExitCode
}

func machineDecisionOf(r *routerDecideResponse, requestID string) *machineDecideResult {
	result := &machineDecideResult{
		RequestID:     requestID,
		Model:         r.Model,
		ModelResolved: r.ModelResolved,
		Answers:       r.Answers,
		Citations:     []machineDecideCitation{},
	}
	for _, c := range r.Citations {
		result.Citations = append(result.Citations, machineDecideCitation(c))
	}
	for _, s := range r.Sources {
		result.Sources = append(result.Sources, machineDecideSource(s))
	}
	if r.Usage != nil {
		u := machineDecideUsage(*r.Usage)
		result.Usage = &u
	}
	return result
}

func machineModelsOf(r *routerDecideModels) *machineDecideModels {
	result := &machineDecideModels{Models: []machineDecideModel{}}
	for _, m := range *r.Models {
		result.Models = append(result.Models, machineDecideModel(m))
	}
	return result
}

// emitDecide is emitMachine without HTML escaping, because the answers are
// carried as the router wrote them. The encoder still strips the whitespace
// between a raw value's tokens, which is what keeps the envelope one line.
func emitDecide(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func failDecide(stdout io.Writer, exitCode int, code, action string, err error) int {
	emitDecide(stdout, decideEnvelope{
		machineHeader: newMachineHeader("decide", exitCode, code, false, action),
		Error:         clientMessage(err),
	})
	return exitCode
}

// ── the command ─────────────────────────────────────────────────────────

func cmdDecide(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return decideMain(time.Now, args, stdin, stdout, stderr, getenv)
}

// decideUsage is what decide -h prints. The flag set below is built only in
// machine mode, where its output is discarded, so it cannot say this itself.
const decideUsage = `usage: jevlin decide --stdin [-config file] [-timeout duration]
       jevlin decide --models [-config file] [-timeout duration]

Asks a decision model typed questions about a state, for an agent: one
version-1 JSON request on stdin, one JSON envelope on stdout with
result.answers, one per question. A decision is billed and never retried.
--models lists the models, with each one's price and whether this account
can call it, and reads nothing from stdin. Give exactly one of the two.

flags:
  --stdin            read the request from stdin
  --models           list the models
  -timeout duration  the deadline for the whole call (default 1m0s)
  -config file       the config to read, given at most once (jevlin help
                     config)

the request:
  model (vendor/model), state and questions, and optionally options, sources
  and max_chars.

exit status: as for jevlin search.

example:
  jevlin decide --models
`

func decideMain(now func() time.Time, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(stdout, decideUsage)
		return exitOK
	}
	if !hasBoolFlag(args, "stdin") && !hasBoolFlag(args, "models") {
		fmt.Fprintln(stderr, "jevlin decide: --stdin or --models is required: send one version-1 JSON request on stdin, or list the models")
		return exitUsage
	}
	return decideMainIn(signalContext, now, args, stdin, stdout, getenv)
}

// decideMainIn is decideMain under the cancellation the caller chooses, as
// searchDeadline describes: `jevlin mcp` hands it plainContext, so a signal
// stops the server rather than only its call.
func decideMainIn(cancelOn func() (context.Context, context.CancelFunc), now func() time.Time, args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	// Machine mode only, so everything is in the envelope, as for search
	// --stdin: nothing on stderr for a caller to parse instead.
	fs := newFlagSet("decide", io.Discard)
	cfgPath := onceStringFlag(fs, "config", "path to TOML config file; given at most once")
	timeout := fs.Duration("timeout", defaultSearchTimeout, "whole-call deadline, covering connect, headers and body")
	readStdin := fs.Bool("stdin", false, "read one version-1 JSON decide request from stdin and answer with the machine envelope")
	listModels := fs.Bool("models", false, "list the decision models this router offers")
	if err := fs.Parse(args); err != nil {
		return failDecide(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("the flags could not be parsed"))
	}
	if len(fs.Args()) > 0 {
		return failDecide(stdout, exitUsage, codeUnexpectedArgument, actionFixInput,
			errors.New("decide takes its request from stdin, not from the command line"))
	}
	if *timeout <= 0 {
		return failDecide(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("-timeout must be positive"))
	}
	if *readStdin == *listModels {
		return failDecide(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("give exactly one of --stdin and --models"))
	}
	var payload []byte
	if *readStdin {
		body, ierr := decodeMachineDecideRequest(stdin)
		if ierr != nil {
			return failDecide(stdout, exitUsage, ierr.Code, actionFixInput, ierr)
		}
		payload = body.encode()
	}

	cfg, _, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		return failDecide(stdout, exitTransport, "config_unreadable", actionFixInput, err)
	}
	if cfg.Search.RouterURL == nil {
		return failDecide(stdout, exitTransport, "no_router_configured", actionFixInput,
			errors.New("no router configured ([router] url)"))
	}
	// The stored key goes only to the router it records (resolveAPIKey), and
	// JEVLIN_API_KEY only to the installation's router, as for search, fetch
	// and limits. Either refusal is the config's to fix, not a reason to connect.
	key, keySrc, err := resolveAPIKey(getenv, cfg.Search)
	if err == nil && keySrc == keyFromEnv {
		err = envKeyRefusedFor(*cfgPath, cfg, getenv)
	}
	if err != nil {
		if keyRefusedForOrigin(err) {
			return failDecide(stdout, exitUsage, keyOriginCode, actionFixInput, err)
		}
		return failDecide(stdout, exitClientErr, "credential_unreadable", actionConnect, err)
	}
	if key == "" {
		return failDecide(stdout, exitClientErr, string(faultNoCredential), actionConnect,
			errors.New("this installation holds no search credential; run jevlin connect"))
	}

	ctx, cancel := searchDeadline(cancelOn, *timeout)
	defer cancel()
	endpoint := strings.TrimRight(cfg.Search.RouterURL.String(), "/") + "/v1/decisions"
	var out decideOutcome
	if *listModels {
		out = performDecideModels(ctx, now, endpoint+"/models", key)
	} else {
		out = performDecide(ctx, now, endpoint, key, payload)
	}
	env, code := decideEnvelopeOf(out)
	emitDecide(stdout, env)
	return code
}
