package main

// `jevlin search --stdin`: the stable machine path.
//
// The query arrives as JSON on stdin and never as shell text, which is the
// point of the mode — an agent composing a command line has to escape the
// query correctly every time, and a query is exactly the kind of string
// that contains quotes, backticks, semicolons and newlines. Here it is one
// JSON string, decoded once, forwarded byte-for-byte.
//
// The answer is one envelope. An agent decides what to do next from ok,
// retryable and action, never by reading a message, and never by matching
// on the router's prose.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

const (
	machineRequestVersion = 1
	// machineStdinMax bounds the request before any JSON decoding. Read
	// max+1, for the same reason the router's response is: a truncating
	// read would hand the decoder a prefix and call the result a request.
	machineStdinMax = 1 << 20
)

// Stable client codes for a request this client refused to send. All of
// them are exit 2 / usage_error / fix_input: the caller has something to
// correct, and no amount of retrying corrects it.
const (
	codeInputTooLarge        = "input_too_large"
	codeInvalidUTF8          = "invalid_utf8"
	codeInvalidJSON          = "invalid_json"
	codeUnsupportedVersion   = "unsupported_version"
	codeUnknownField         = "unknown_field"
	codeMissingQuery         = "missing_query"
	codeEmptyQuery           = "empty_query"
	codeUnexpectedArgument   = "unexpected_argument"
	codeInvalidFlags         = "invalid_flags"
	codeInvalidRecency       = "invalid_recency"
	codeInvalidDomainFilter  = "invalid_domain_filter"
	codeInvalidMaxResults    = "invalid_max_results"
	codeInvalidView          = "invalid_view"
	codeInvalidProviders     = "invalid_providers"
	codeInvalidObjective     = "invalid_objective"
	codeInvalidSearchQueries = "invalid_search_queries"
)

// inputError is a local refusal with a stable machine code. It is a type
// rather than a string so the classifier reads the code off the value.
type inputError struct {
	Code string
	Msg  string
}

func (e *inputError) Error() string { return e.Msg }

func inputErrorf(code, msg string) *inputError { return &inputError{Code: code, Msg: msg} }

// machineSearchRequest is the v1 request, after validation. The three
// router options are pointers/slices left nil when the caller's request
// did not carry them, so the body this client sends can tell "absent"
// from "present and empty" the same way the wire request does.
type machineSearchRequest struct {
	query        string
	tier         string
	recency      *string
	domainFilter []string
	maxResults   *int
	providers    []string
	// view is "" (absent, meaning "full") or the caller's validated
	// choice of "full" or "merged" — never any other string, since
	// validateView refuses anything else before this is ever set.
	view string
	// objective and searchQueries are the query forms: what the caller is
	// trying to find out, in its own words, and up to four reformulations
	// of query. nil when the request did not carry them, so a field the
	// caller left out is left out of the router's body too.
	objective     *string
	searchQueries []string
}

// wireSearchRequest is the decoded shape. Pointers distinguish an absent
// field from a present empty one, which is the difference between
// missing_query and empty_query. recency, domainFilter and maxResults are
// decoded as raw JSON first so a type mismatch (a number where a string
// was expected, and so on) can be answered with a message naming the
// field, rather than the generic "unknown field" a struct-typed decode
// failure would give no matter which field caused it.
type wireSearchRequest struct {
	Version       *int            `json:"version"`
	Query         *string         `json:"query"`
	Tier          *string         `json:"tier"`
	Recency       json.RawMessage `json:"recency"`
	DomainFilter  json.RawMessage `json:"domain_filter"`
	MaxResults    json.RawMessage `json:"max_results"`
	Providers     json.RawMessage `json:"providers"`
	View          json.RawMessage `json:"view"`
	Objective     json.RawMessage `json:"objective"`
	SearchQueries json.RawMessage `json:"search_queries"`
}

// decodeMachineSearchRequest reads exactly one v1 request from r.
//
// Strict, unlike the router's response, and deliberately so: this is the
// client's own contract with its caller, not someone else's product API.
// Unknown fields are refused, which is also what keeps a caller from
// supplying trace or lineage — those are local host state, decided here,
// and a request that could set them would let the caller forge the
// trajectory a search is attributed to.
func decodeMachineSearchRequest(r io.Reader) (machineSearchRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, machineStdinMax+1))
	if err != nil {
		return machineSearchRequest{}, inputErrorf(codeInvalidJSON, "the request could not be read from stdin")
	}
	if int64(len(raw)) > machineStdinMax {
		// Oversized input is a usage error, not a partially parsed request.
		return machineSearchRequest{}, inputErrorf(codeInputTooLarge,
			"the request is larger than the 1 MiB limit")
	}
	if !utf8.Valid(raw) {
		return machineSearchRequest{}, inputErrorf(codeInvalidUTF8, "the request is not valid UTF-8")
	}
	raw = trimUTF8BOM(raw)
	if err := singleJSONObject(raw); err != nil {
		return machineSearchRequest{}, inputErrorf(codeInvalidJSON,
			"the request must be exactly one JSON object with nothing after it")
	}

	// The version is read with a permissive decode first, so a future
	// version is told its version is unsupported rather than being
	// lectured about the new fields that came with it.
	var probe struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Version == nil {
		return machineSearchRequest{}, inputErrorf(codeUnsupportedVersion,
			"the request needs \"version\": 1")
	}
	if *probe.Version != machineRequestVersion {
		return machineSearchRequest{}, inputErrorf(codeUnsupportedVersion,
			"this client speaks search request version 1")
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var wire wireSearchRequest
	if err := dec.Decode(&wire); err != nil {
		return machineSearchRequest{}, inputErrorf(codeUnknownField,
			"the request carries a field this version does not accept")
	}
	if wire.Query == nil {
		return machineSearchRequest{}, inputErrorf(codeMissingQuery, "the request needs a \"query\"")
	}
	// Trimming decides emptiness and nothing else. The query that goes to
	// the router is the caller's bytes, unaltered — leading whitespace,
	// shell metacharacters, combining marks and all.
	if strings.TrimSpace(*wire.Query) == "" {
		return machineSearchRequest{}, inputErrorf(codeEmptyQuery, "the query is empty")
	}
	req := machineSearchRequest{query: *wire.Query}
	if wire.Tier != nil {
		req.tier = *wire.Tier
	}
	if len(wire.Recency) > 0 {
		recency, err := validateRecency(wire.Recency)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.recency = &recency
	}
	if len(wire.DomainFilter) > 0 {
		domainFilter, err := validateDomainFilter(wire.DomainFilter)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.domainFilter = domainFilter
	}
	if len(wire.MaxResults) > 0 {
		maxResults, err := validateMaxResults(wire.MaxResults)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.maxResults = &maxResults
	}
	if len(wire.Providers) > 0 {
		providers, err := validateProviders(wire.Providers)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.providers = providers
	}
	if len(wire.View) > 0 {
		view, err := validateView(wire.View)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.view = view
	}
	if len(wire.Objective) > 0 {
		objective, err := validateObjective(wire.Objective)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.objective = &objective
	}
	if len(wire.SearchQueries) > 0 {
		forms, err := validateSearchQueries(wire.SearchQueries)
		if err != nil {
			return machineSearchRequest{}, err
		}
		req.searchQueries = forms
	}
	return req, nil
}

// isJSONNull reports whether raw is the literal JSON null, distinct from
// an absent field (wire.Recency etc. is empty, not "null", when the
// caller never wrote the key at all) and distinct from a present zero
// value. Checked explicitly, ahead of every option's own unmarshal: for a
// pointer-shaped field like recency and max_results, json.Unmarshal of
// null into the destination happens to leave it at its Go zero value with
// no error, which coincidentally fails the bounds check below and gets
// refused anyway — but "domain_filter" unmarshals null into a nil slice
// exactly as it would for an absent field, so relying on the coincidence
// would silently treat a caller's explicit null as "say nothing". A
// caller who writes the key with a null value has said something, even
// if that something is "I don't know" — never the same as not asking.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// validRecencyWords is the router's own closed vocabulary for "recency",
// mirrored here so a bad value costs the caller no router call.
var validRecencyWords = map[string]bool{"day": true, "week": true, "month": true, "year": true}

func validateRecency(raw json.RawMessage) (string, *inputError) {
	if isJSONNull(raw) {
		return "", inputErrorf(codeInvalidRecency, `"recency" must not be null`)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", inputErrorf(codeInvalidRecency, `"recency" must be a string`)
	}
	if !validRecencyWords[s] {
		return "", inputErrorf(codeInvalidRecency, `"recency" must be one of "day", "week", "month", "year"`)
	}
	return s, nil
}

// domainFilterMax is the router's own cap on the "domain_filter" array,
// mirrored here so an oversized list costs the caller no router call.
const domainFilterMax = 16

// providersMax matches domainFilterMax: the router's own skill file names
// about fifteen arms, and a list longer than every arm that exists is a
// caller mistake, not a fan-out.
const providersMax = 16

// validateProviders mirrors validateDomainFilter's shape rules: an
// explicit null or [] is refused rather than guessed at, and each entry
// must look like a provider name — the code-like charset the router's own
// arm names use (exa, parallel-search, pplx-agent), never whitespace or a
// control character. An unknown name is deliberately NOT refused here:
// the router owns its provider list, and a name this build predates must
// reach it rather than be vetoed by a stale local copy.
func validateProviders(raw json.RawMessage) ([]string, *inputError) {
	if isJSONNull(raw) {
		return nil, inputErrorf(codeInvalidProviders, `"providers" must not be null`)
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, inputErrorf(codeInvalidProviders, `"providers" must be an array of strings`)
	}
	if len(names) == 0 {
		return nil, inputErrorf(codeInvalidProviders, `"providers" must not be empty`)
	}
	if len(names) > providersMax {
		return nil, inputErrorf(codeInvalidProviders,
			fmt.Sprintf(`"providers" accepts at most %d names`, providersMax))
	}
	for _, n := range names {
		if _, ok := machineCodeLike(n); !ok {
			return nil, inputErrorf(codeInvalidProviders,
				fmt.Sprintf(`"providers" entry %q must be a provider name: letters, digits, '.', '_' or '-'`, n))
		}
	}
	return names, nil
}

// queryFormMaxBytes is the router's bound on objective and on each
// search_queries entry, the same as its bound on query; searchQueriesMax is
// its bound on how many reformulations one request may carry. Both are
// checked here, before the router is called, so a request the router
// would refuse with invalid_request costs nothing and its answer names the
// field.
const (
	queryFormMaxBytes = 4096
	searchQueriesMax  = 4
)

// validateObjective takes the caller's goal as a string of its own. An
// explicit null or a blank string is refused, as an empty view is: a
// caller who wrote the key owes a value, and forwarding nothing in its
// place would be this client deciding what the caller meant.
func validateObjective(raw json.RawMessage) (string, *inputError) {
	if isJSONNull(raw) {
		return "", inputErrorf(codeInvalidObjective, `"objective" must not be null`)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", inputErrorf(codeInvalidObjective, `"objective" must be a string`)
	}
	if strings.TrimSpace(s) == "" {
		return "", inputErrorf(codeInvalidObjective, `"objective" must not be empty`)
	}
	if len(s) > queryFormMaxBytes {
		return "", inputErrorf(codeInvalidObjective,
			fmt.Sprintf(`"objective" must be at most %d bytes`, queryFormMaxBytes))
	}
	return s, nil
}

// validateSearchQueries mirrors validateDomainFilter's shape rules: an
// explicit null or [] is refused rather than guessed at, and so is a list
// longer than the router accepts. Each entry is a query of its own, held to
// query's rules: not blank, at most 4096 bytes, and forwarded as the
// caller's bytes. A repeat of query or of another entry is not refused
// here: the router drops it, and that rule is the router's to keep.
func validateSearchQueries(raw json.RawMessage) ([]string, *inputError) {
	if isJSONNull(raw) {
		return nil, inputErrorf(codeInvalidSearchQueries, `"search_queries" must not be null`)
	}
	var forms []string
	if err := json.Unmarshal(raw, &forms); err != nil {
		return nil, inputErrorf(codeInvalidSearchQueries, `"search_queries" must be an array of strings`)
	}
	if len(forms) == 0 {
		return nil, inputErrorf(codeInvalidSearchQueries, `"search_queries" must not be empty`)
	}
	if len(forms) > searchQueriesMax {
		return nil, inputErrorf(codeInvalidSearchQueries,
			fmt.Sprintf(`"search_queries" accepts at most %d entries`, searchQueriesMax))
	}
	for i, f := range forms {
		if strings.TrimSpace(f) == "" {
			return nil, inputErrorf(codeInvalidSearchQueries,
				fmt.Sprintf(`"search_queries" entry %d is empty`, i))
		}
		if len(f) > queryFormMaxBytes {
			return nil, inputErrorf(codeInvalidSearchQueries,
				fmt.Sprintf(`"search_queries" entry %d must be at most %d bytes`, i, queryFormMaxBytes))
		}
	}
	return forms, nil
}

func validateDomainFilter(raw json.RawMessage) ([]string, *inputError) {
	if isJSONNull(raw) {
		return nil, inputErrorf(codeInvalidDomainFilter, `"domain_filter" must not be null`)
	}
	var hosts []string
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return nil, inputErrorf(codeInvalidDomainFilter, `"domain_filter" must be an array of strings`)
	}
	// An explicit [] is a caller sending a filter that excludes every
	// hostname, which is not a request this client can send meaningfully
	// — the router does not document what an empty domain_filter does,
	// and silently forwarding it is far more likely to be a caller
	// mistake than a deliberate "no domains" request. Refused the same
	// way a bad entry is, rather than passed through as "no filter."
	if len(hosts) == 0 {
		return nil, inputErrorf(codeInvalidDomainFilter, `"domain_filter" must not be empty`)
	}
	if len(hosts) > domainFilterMax {
		return nil, inputErrorf(codeInvalidDomainFilter,
			fmt.Sprintf(`"domain_filter" accepts at most %d hostnames`, domainFilterMax))
	}
	for _, h := range hosts {
		if !bareHostname(h) {
			return nil, inputErrorf(codeInvalidDomainFilter,
				fmt.Sprintf(`"domain_filter" entry %q must be a bare hostname: no scheme, path, port or whitespace`, h))
		}
	}
	return hosts, nil
}

// bareHostname is deliberately strict rather than a URL parse: a scheme
// or a path both put a "/" in the string, and a port puts a ":" in it, so
// refusing both catches every shape the router's own rule names without
// needing to parse the entry as a URL first (which would accept things a
// bare hostname is not, such as a scheme-relative "//host").
func bareHostname(h string) bool {
	if h == "" {
		return false
	}
	if strings.ContainsAny(h, ":/") {
		return false
	}
	for _, r := range h {
		if unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validateMaxResults(raw json.RawMessage) (int, *inputError) {
	if isJSONNull(raw) {
		return 0, inputErrorf(codeInvalidMaxResults, `"max_results" must not be null`)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, inputErrorf(codeInvalidMaxResults, `"max_results" must be an integer`)
	}
	if n < 1 || n > 25 {
		return 0, inputErrorf(codeInvalidMaxResults, `"max_results" must be between 1 and 25`)
	}
	return n, nil
}

// validateView refuses anything but the two shapes this client renders.
// An absent "view" never reaches here — wire.View is empty and
// decodeMachineSearchRequest leaves req.view at its zero value, which
// means "full" everywhere it is read. Decoded as raw JSON first, like
// recency, domain_filter and max_results: a plain *string field would let
// an explicit "view": null decode to the same nil the field has when the
// caller never wrote the key at all, silently treating a stated null as
// absent instead of refusing it — the same gap already closed for
// domain_filter. An explicit "" is not "full" either: a caller who wrote
// the key owes a real value, the same as any other option here.
func validateView(raw json.RawMessage) (string, *inputError) {
	if isJSONNull(raw) {
		return "", inputErrorf(codeInvalidView, `"view" must not be null`)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", inputErrorf(codeInvalidView, `"view" must be a string`)
	}
	switch s {
	case "full", "merged":
		return s, nil
	default:
		return "", inputErrorf(codeInvalidView, `"view" must be "full" or "merged"`)
	}
}

// ── the envelope ────────────────────────────────────────────────────────

type searchEnvelope struct {
	machineHeader
	HTTPStatus   int            `json:"http_status,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
	RetryAfterMS *int64         `json:"retry_after_ms,omitempty"`
	Result       *machineResult `json:"result,omitempty"`
	Mining       *machineMining `json:"mining,omitempty"`
	Error        *machineError  `json:"error,omitempty"`
}

// machineResult is the client-owned view of a search: the fields this
// client understands, not a passthrough of whatever the router sent. The
// raw router JSON stays available on the human -format json path for
// callers that deliberately want it.
//
// The query is not echoed. The caller sent it; repeating it back costs
// bytes and puts the one piece of model-influenceable text in this
// envelope somewhere it serves no purpose.
type machineResult struct {
	RequestID string `json:"request_id"`
	Chosen    int    `json:"chosen"`
	SessionID string `json:"session_id,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	// Decision and Usage are nil, and so absent from the envelope, exactly
	// when the router's own response omitted them — never a zeroed struct
	// standing in for "the router didn't say."
	Decision *machineDecision `json:"decision,omitempty"`
	Usage    *machineUsage    `json:"usage,omitempty"`
	// Indexes is the router's indexes.by_provider, in its order: each arm's
	// status, and for one that answered its result count and latency. It is
	// how a caller tells "nothing exists" from "an index failed": a status
	// other than "ok" means that arm's pages are missing from merged. Absent
	// exactly when the router sent no indexes block, like Decision and Usage.
	Indexes *[]machineIndex `json:"indexes,omitempty"`
	// Merged is present in every view: it is the whole reason the option
	// exists, so an agent that asked for "merged" still gets it, and one
	// that didn't ask gets it anyway ("an agent may always read merged
	// first" — S3). It is the router's merged view, in the router's order
	// and cut to the router's window: one merge, the router's, which every
	// deployed router has returned since before v0.1.0. This client used to
	// run a second one of its own over the candidates, and every benchmark
	// that included it was measuring a merge nobody maintained.
	Merged []machineMergedPage `json:"merged"`
	// Coverage, MergedLimit, MergedTotal and ExcerptsTrimmed are the
	// router's account of its merged view, carried when the router sent
	// them (Jevlin-core #55 adds them) and absent when it did not: the
	// window it applied, how many pages there were before it, and whether
	// its character budget cut an excerpt.
	Coverage        *machineCoverage `json:"coverage,omitempty"`
	MergedLimit     *int             `json:"merged_limit,omitempty"`
	MergedTotal     *int             `json:"merged_total,omitempty"`
	ExcerptsTrimmed bool             `json:"excerpts_trimmed,omitempty"`
	// FormsDropped says the search ran without the caller's objective and
	// search_queries: the router refused the request as malformed_json
	// while it carried them — because it predates them, or because the
	// body was more than it reads — and the one retry left them out. Not
	// a failure: the search ran, on query alone.
	FormsDropped bool `json:"forms_dropped,omitempty"`
	// Candidates is a pointer so "view":"merged" can omit the key
	// entirely (a nil pointer with omitempty) while "full" — the
	// default — always carries it, even on the (real-router-never-sends-
	// this) edge case of zero candidates: a non-nil pointer to an empty
	// slice is not "empty" to encoding/json, only a nil pointer is.
	Candidates *[]machineCandidate `json:"candidates,omitempty"`
}

// machineMergedPage is one page of the router's merged view, mapped field
// for field. The six fields this envelope has always carried keep their
// names and types; the rest are the router's own evidence for the page,
// present when the router sent them.
type machineMergedPage struct {
	URL string `json:"url"`
	// ResultID is the router's name for the page in the result stored under
	// request_id, so a later call can refer to the page without its URL.
	ResultID string `json:"result_id,omitempty"`
	// CanonicalURL is the key the router merged the page under. It is
	// informational: cite URL, which is a spelling a provider returned.
	CanonicalURL string `json:"canonical_url,omitempty"`
	Title        string `json:"title,omitempty"`
	// Published is the page's date as a provider gave it, RFC 3339, absent
	// when none did. Carried as the router's string, never parsed: the
	// caller compares it with the question's time, and a reformatted date
	// would be this client's claim rather than the provider's.
	Published string `json:"published,omitempty"`
	// Snippet is the router's excerpt: since Jevlin-core #55 every
	// provider's text for the page, joined; before it, the longest one.
	Snippet  string   `json:"snippet,omitempty"`
	Excerpts []string `json:"excerpts,omitempty"`
	// FoundBy is the providers that listed the page, each once, in the
	// order the router lists them.
	FoundBy []string `json:"found_by"`
	// FoundIn is where those providers cited the page in the result the
	// router stores under request_id: one entry per FoundBy provider, in
	// the same order. It is what keeps a merged-only envelope joinable
	// (dropin-miner#126). With "view":"merged" the candidates list is omitted, so
	// without these positions nothing maps a page back into the stored
	// result — and the router's own feedback events (result.fetched,
	// result.cited) name a candidate and a citation, not a URL.
	FoundIn []machineMergedPosition `json:"found_in"`
	// BestRank is 0-based, as it always was here; the router's is 1-based.
	BestRank int `json:"best_rank"`
	// Votes, Groups, Exclusive and MatchedTerms are the router's
	// corroboration for the page (Jevlin-core #55): distinct (index group,
	// query form) pairs that listed it, distinct index groups, whether
	// exactly one group did, and the query's identifiers found in it.
	Votes        int      `json:"votes,omitempty"`
	Groups       int      `json:"groups,omitempty"`
	Exclusive    bool     `json:"exclusive,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
}

// machineMergedPosition is one (candidate, citation) position in the
// router's own result: the candidate's index in result.candidates —
// every candidate, not only the "ok" ones the merge reads — and the
// citation's index within that candidate's list.
type machineMergedPosition struct {
	Candidate int `json:"candidate"`
	Citation  int `json:"citation"`
}

// machineCoverage is the router's account of its whole merge, not only
// the window (Jevlin-core #55): how many pages, how many only one index
// group returned and how many two or more did, pages per group, and —
// when search_queries were sent — pages per query form, index 0 being the
// query itself, and how many only a reformulation found.
type machineCoverage struct {
	Pages             int            `json:"pages"`
	Exclusive         int            `json:"exclusive"`
	Corroborated      int            `json:"corroborated"`
	Groups            int            `json:"groups"`
	ByGroup           map[string]int `json:"by_group,omitempty"`
	ExclusiveByGroup  map[string]int `json:"exclusive_by_group,omitempty"`
	ByForm            []int          `json:"by_form,omitempty"`
	ReformulationOnly int            `json:"reformulation_only,omitempty"`
}

// machineDecision is the router's decision block, carried through: which
// tier ran, which providers it dispatched, and which it dropped for cost.
type machineDecision struct {
	Tier      string   `json:"tier,omitempty"`
	Providers []string `json:"providers,omitempty"`
	Trimmed   []string `json:"trimmed,omitempty"`
}

// machineUsage is the router's cost and cache ledger for the whole
// search. The maintainer's call, 2026-09-18: a participant should see
// what a search costs the network even though it is free to them.
type machineUsage struct {
	CostMicros int64 `json:"cost_micros,omitempty"`
	CacheHit   bool  `json:"cache_hit,omitempty"`
	Pending    int   `json:"pending,omitempty"`
	LatencyMS  int64 `json:"latency_ms,omitempty"`
	// DeadlineHit, present only when true as CacheHit is, says the router
	// answered at its deadline with arms still out: the search was cut
	// short, and an empty or thin merged list is not evidence of absence.
	DeadlineHit bool `json:"deadline_hit,omitempty"`
}

// machineIndex is one arm's line in result.indexes. Results and LatencyMS
// are absent, not zero, when the router did not send them, which it does
// for an arm that did not answer; an arm that answered with nothing says
// "results": 0.
type machineIndex struct {
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	Results   *int   `json:"results,omitempty"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
}

type machineCandidate struct {
	Provider  string            `json:"provider,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Status    string            `json:"status,omitempty"`
	Chosen    bool              `json:"chosen"`
	Answer    string            `json:"answer,omitempty"`
	Error     string            `json:"error,omitempty"`
	Citations []machineCitation `json:"citations,omitempty"`
	// CostMicros and LatencyMS are per-candidate, unlike the search-wide
	// Usage above: each arm has its own cost and its own clock. LatencyMS
	// is the candidate's total latency (latency.total_ms on the wire),
	// not its time to first byte.
	CostMicros int64 `json:"cost_micros,omitempty"`
	LatencyMS  int64 `json:"latency_ms,omitempty"`
}

type machineCitation struct {
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// machineResultOf is data, not presentation. Answers and snippets are
// carried as the router sent them: JSON already makes control bytes
// syntactically inert, and rewriting a provider's answer to make it
// prettier would corrupt the thing the caller asked for. The terminal
// sanitizing belongs to the human renderer, where a control byte would
// actually do something.
func machineResultOf(s routerSuccess, view string) *machineResult {
	r := s.Response
	out := &machineResult{
		RequestID:       s.RequestID,
		Chosen:          r.Chosen,
		Merged:          mergedOf(r),
		MergedLimit:     r.MergedLimit,
		MergedTotal:     r.MergedTotal,
		ExcerptsTrimmed: r.ExcerptsTrimmed,
	}
	if r.Coverage != nil {
		cov := machineCoverage(*r.Coverage)
		out.Coverage = &cov
	}
	if r.Session != nil {
		out.SessionID = r.Session.ID
	}
	if r.Usage != nil {
		out.LatencyMS = r.Usage.LatencyMS
		out.Usage = &machineUsage{
			CostMicros:  r.Usage.CostMicros,
			CacheHit:    r.Usage.CacheHit,
			Pending:     r.Usage.Pending,
			LatencyMS:   r.Usage.LatencyMS,
			DeadlineHit: r.Usage.DeadlineHit,
		}
	}
	if r.Indexes != nil {
		indexes := make([]machineIndex, 0, len(r.Indexes.ByProvider))
		for _, ix := range r.Indexes.ByProvider {
			indexes = append(indexes, machineIndex(ix))
		}
		out.Indexes = &indexes
	}
	if r.Decision != nil {
		out.Decision = &machineDecision{
			Tier:      r.Decision.Tier,
			Providers: r.Decision.Providers,
			Trimmed:   r.Decision.Trimmed,
		}
	}
	candidates := make([]machineCandidate, 0, len(r.Candidates))
	for i, c := range r.Candidates {
		mc := machineCandidate{
			Provider:   c.Provider,
			Kind:       c.Kind,
			Status:     c.Status,
			Chosen:     i == r.Chosen,
			Answer:     c.Answer,
			Error:      c.Error,
			CostMicros: c.CostMicros,
		}
		if c.Latency != nil {
			mc.LatencyMS = c.Latency.TotalMS
		}
		for _, cit := range c.Citations {
			// Field by field, and on purpose not rank and form: they are the
			// router's keys for a citation, which mergedOf reads to find
			// found_in's positions, and the envelope's position is the
			// index in this list. TestTheRouterCitationsFieldsAreDecided
			// pins routerCitation's fields, so one the router grows stops
			// the build's tests until someone decides whether the machine
			// contract carries it.
			mc.Citations = append(mc.Citations, machineCitation{URL: cit.URL, Title: cit.Title, Snippet: cit.Snippet})
		}
		candidates = append(candidates, mc)
	}
	// "merged" is the one view that omits candidates; every other value
	// validateView accepts ("full") or leaves absent keeps them.
	if view != "merged" {
		out.Candidates = &candidates
	}
	return out
}

// mergedOf maps the router's merged view into the envelope's, in the
// router's order, with no re-sorting and no re-deduplication: the order and
// the page identity are the router's contract, and a second opinion here
// is what this replaced.
//
// A 200 to a search that asked for the merged view and has no "merged" key
// is the router breaking that contract. It maps to an empty list, never to
// a failure: the search ran, and its candidates are intact. Nothing is
// said on stderr: machine mode writes none, because the host that runs the
// search hands its stderr to the model as well, and the envelope is the
// answer.
func mergedOf(r routerResponse) []machineMergedPage {
	if r.Merged == nil {
		return []machineMergedPage{}
	}
	pages := make([]machineMergedPage, 0, len(*r.Merged))
	for _, m := range *r.Merged {
		page := machineMergedPage{
			URL:          m.URL,
			ResultID:     m.ResultID,
			CanonicalURL: m.CanonicalURL,
			Title:        m.Title,
			Published:    m.Published,
			Snippet:      m.Excerpt,
			Excerpts:     m.Excerpts,
			FoundBy:      []string{},
			FoundIn:      []machineMergedPosition{},
			// The router's rank is 1-based and this envelope's has always
			// been 0-based. A rank below 1 is not one the router writes;
			// it is held at 0 rather than carried out as a negative.
			BestRank:     max(m.BestRank-1, 0),
			Votes:        m.Votes,
			Groups:       m.Groups,
			Exclusive:    m.Exclusive,
			MatchedTerms: m.MatchedTerms,
		}
		// found_by on the wire is one entry per listing — a provider that
		// found the page under the query and under a reformulation is
		// listed twice. The envelope's is the providers, each once, in the
		// router's order, and each provider's position is taken from its
		// first listing.
		seen := map[string]bool{}
		var firsts []routerFoundBy
		for _, fb := range m.FoundBy {
			if seen[fb.Provider] {
				continue
			}
			seen[fb.Provider] = true
			page.FoundBy = append(page.FoundBy, fb.Provider)
			firsts = append(firsts, fb)
		}
		page.FoundIn = positionsOf(r.Candidates, firsts)
		pages = append(pages, page)
	}
	return pages
}

// positionsOf finds each listing's (candidate, citation) position in the
// router's candidates. The candidate is the provider's index in the whole
// list. The citation is found by its (form, rank) pair, never computed as
// rank − 1: since Jevlin-core #55 a candidate's ranks restart for each
// query form, so a rank names one citation only together with its form,
// and rank − 1 points at the wrong one the moment a candidate carries two
// forms.
//
// found_in is read in step with found_by, entry for entry. So when any
// listing cannot be found — a provider with no candidate, or no citation
// at that form and rank, which a router keeping its own contract never
// sends — the page carries no positions at all. A shorter list would pair
// every later position with the wrong provider, which is worse than none,
// and a guessed one would name a citation the provider never gave.
func positionsOf(candidates []routerCandidate, listings []routerFoundBy) []machineMergedPosition {
	positions := make([]machineMergedPosition, 0, len(listings))
	for _, fb := range listings {
		ci := slices.IndexFunc(candidates, func(c routerCandidate) bool { return c.Provider == fb.Provider })
		if ci < 0 {
			return []machineMergedPosition{}
		}
		ti := slices.IndexFunc(candidates[ci].Citations, func(c routerCitation) bool {
			return c.Form == fb.Form && c.Rank == fb.Rank
		})
		if ti < 0 {
			return []machineMergedPosition{}
		}
		positions = append(positions, machineMergedPosition{Candidate: ci, Citation: ti})
	}
	return positions
}

// machineMining is the economic half of the answer, and it is separate
// from ok on purpose: a successful search with mining disabled, undecided
// or degraded is still a successful search. Nothing here is called
// "earned" — the AS reconciles observations against the provider, and this
// client never claims a reward on its own say-so.
type machineMining struct {
	// State is the persisted runtime decision, in its own canonical
	// spelling: enabled, disabled, undecided or degraded.
	State string `json:"state"`
	// Configured is [miner] enabled — whether intake exists at all.
	Configured bool `json:"configured"`
	// Recorded says whether THIS search's observation was written. False
	// with State "enabled" means capture failed; Health says why.
	Recorded bool            `json:"recorded"`
	Detail   string          `json:"detail,omitempty"`
	Health   []machineHealth `json:"health,omitempty"`
}

type machineHealth struct {
	Component string `json:"component"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
}

func healthOf(records []auth.HealthRecord) []machineHealth {
	if len(records) == 0 {
		return nil
	}
	out := make([]machineHealth, 0, len(records))
	for _, r := range records {
		out = append(out, machineHealth{
			Component: string(r.Component),
			Reason:    string(r.Reason),
			Detail:    boundMessage(r.Detail),
		})
	}
	return out
}

// ── classification ──────────────────────────────────────────────────────

// searchClassification is the recovery advice for one outcome. It is
// computed from the fault value and the HTTP status, and from nothing
// else — in particular, never from the text of an error or of the
// router's "error" field.
type searchClassification struct {
	ExitCode  int
	Code      string
	Retryable bool
	Action    string
}

func classifySearch(out searchOutcome) searchClassification {
	switch out.Fault {
	case faultNone:
		return searchClassification{exitOK, "ok", false, actionNone}
	case faultNoCredential:
		return searchClassification{exitClientErr, string(faultNoCredential), false, actionConnect}
	case faultTimeout:
		return searchClassification{exitTransport, string(faultTimeout), true, actionRetry}
	case faultCanceled:
		// A person stopped this. Telling an agent to retry it would
		// undo the interruption.
		return searchClassification{exitTransport, string(faultCanceled), false, actionNone}
	case faultTransport:
		return searchClassification{exitTransport, "transport_failed", true, actionRetry}
	case faultOversized, faultInvalidResponse, faultMissingID:
		// A 2xx the client could not use. Not a safe automatic replay
		// signal: the request may well have been served and billed, and
		// repeating it would not make the answer parseable.
		return searchClassification{exitServerErr, string(out.Fault), false, actionReport}
	}
	return classifyRouterStatus(out)
}

func classifyRouterStatus(out searchOutcome) searchClassification {
	c := searchClassification{Code: routerCodeFor(out)}
	switch {
	case out.HTTPStatus >= 500:
		c.ExitCode, c.Retryable, c.Action = exitServerErr, true, actionRetry
	case out.HTTPStatus == 401:
		// 401 means this key is not accepted. It does NOT mean "never
		// registered" — a registered installation with an expired or
		// rotated key gets exactly this, and sending it to connect would
		// start a second registration for one participant. connect is for
		// the registration/claim workflow; this is a credential.
		c.ExitCode, c.Retryable, c.Action = exitClientErr, false, actionLogin
	case out.HTTPStatus == 403:
		c.ExitCode, c.Retryable, c.Action = exitClientErr, false, actionCheckAccess
	case out.HTTPStatus == 429:
		c.ExitCode, c.Retryable, c.Action = exitClientErr, true, actionRetry
	case requestFixable(out.HTTPStatus):
		c.ExitCode, c.Retryable, c.Action = exitClientErr, false, actionFixInput
	default:
		// Every other 4xx. Not retryable and not the caller's input
		// either — a 404 or a 409 from the search endpoint is a
		// deployment or state problem a caller cannot edit its way out
		// of, and saying fix_input would send it round a loop.
		c.ExitCode, c.Retryable, c.Action = exitClientErr, false, actionReport
	}
	return c
}

// requestFixable lists the statuses that name something the SEARCH CALLER
// can actually change: the request it composed.
//
// 400, 413 and 422 are about the query, the tier, or how much of them
// there is — a caller can send a different one. The rest of the 4xx range
// is not. A 405, 406, 411, 414, 415 or 431 is about the method, the Accept
// header, the framing, the endpoint URI or the headers, and every one of
// those is chosen by this client, not by its caller. Telling an agent to
// fix_input for them sends it round a loop editing a query that was never
// the problem, so they report instead.
func requestFixable(status int) bool {
	switch status {
	case 400, 413, 422:
		return true
	default:
		return false
	}
}

// routerCodeFor prefers the search host's own machine code, so an agent
// sees a stable identifier the router owns. It is accepted only when it
// looks like a code: an arbitrary human sentence in that field must not
// become one, which is the same rule that keeps prose out of action.
func routerCodeFor(out searchOutcome) string {
	if out.HasRouterErr {
		if code, ok := machineCodeLike(out.RouterErr.Code); ok {
			return code
		}
	}
	switch {
	case out.HTTPStatus >= 500:
		return "router_error"
	case out.HTTPStatus == 401:
		return "unauthorized"
	case out.HTTPStatus == 403:
		return "forbidden"
	case out.HTTPStatus == 429:
		return "rate_limited"
	default:
		return "router_rejected"
	}
}

const routerCodeCap = 64

func machineCodeLike(s string) (string, bool) {
	if s == "" || len(s) > routerCodeCap {
		return "", false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return "", false
		}
	}
	return s, true
}

// ── assembling the answer ───────────────────────────────────────────────

// searchEnvelopeOf builds the whole answer from the structured outcome.
// It reads the normalized request id off the outcome rather than the raw
// body, so the identity in the envelope is the identity the observation
// was recorded against.
func searchEnvelopeOf(out searchOutcome, mining *machineMining, view string) (searchEnvelope, int) {
	c := classifySearch(out)
	env := searchEnvelope{
		machineHeader: newMachineHeader("search", c.ExitCode, c.Code, c.Retryable, c.Action),
		HTTPStatus:    out.HTTPStatus,
		Mining:        mining,
	}
	if out.HasRetryAfter && (c.Retryable || out.HTTPStatus == 429) {
		ms := out.RetryAfterMS
		env.RetryAfterMS = &ms
	}
	if out.ok() {
		env.RequestID = out.Success.RequestID
		env.Result = machineResultOf(out.Success, view)
		env.Result.FormsDropped = out.FormsDropped
		return env, c.ExitCode
	}
	if out.HasRouterErr {
		env.Error = routerMessage(out.RouterErr.Error)
	}
	if env.Error == nil {
		env.Error = clientMessage(out.Err)
	}
	return env, c.ExitCode
}

// failSearchInput answers a request this client refused to send. It never
// reaches the router, so there is no HTTP status and no mining state to
// report.
func failSearchInput(stdout io.Writer, err *inputError) int {
	env := searchEnvelope{
		machineHeader: newMachineHeader("search", exitUsage, err.Code, false, actionFixInput),
		Error:         clientMessage(err),
	}
	emitMachine(stdout, env)
	return exitUsage
}

// failSearchSetup answers a local failure that stopped the search before
// any request: an unreadable config, no router, no credential. The exit
// code is the one this command has always used for that state; only the
// explanation is new.
func failSearchSetup(stdout io.Writer, exitCode int, code, action string, err error) int {
	env := searchEnvelope{
		machineHeader: newMachineHeader("search", exitCode, code, false, action),
		Error:         clientMessage(err),
	}
	emitMachine(stdout, env)
	return exitCode
}
