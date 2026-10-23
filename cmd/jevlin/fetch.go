package main

// `jevlin fetch --stdin`: the full text of pages a search already found.
//
// A search's merged list gives each page an excerpt; when the excerpt shows
// the page is the right one but not the fact the agent needs, fetch asks
// the router for the page's text, capped and paged. The router serves only
// pages from this tenant's own recent search results (a page outside them
// comes back "not_recent"), at most five a call, so this is not a general
// fetcher, and nothing here tries to make it one.
//
// It is search --stdin's sibling and shares its transport: the stored key,
// the router URL from the config, the deadline, the redirect policy, the
// user agent and the bounded read. The request is strict and validated
// before anything is sent; the answer is one envelope with the machine
// header. Nothing is recorded for mining: a fetch is not a search, and the
// router reconciles nothing against it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Stable client codes for a fetch request this client refused to send,
// beside the ones search --stdin shares with it (unknown_field,
// invalid_max_chars and the rest of the reading). All are exit 2 /
// usage_error / fix_input.
const (
	codeMissingPages    = "missing_pages"
	codeInvalidPages    = "invalid_pages"
	codeInvalidSourceID = "invalid_source_id"
	codeInvalidURL      = "invalid_url"
	codeInvalidOffset   = "invalid_offset"
	codeInvalidQuery    = "invalid_query"
)

// The router's codes and statuses fetch answers with its own advice.
const (
	// fetchUnavailableCode is the router's 403 for an account that is not
	// claimed. The account, not the request, is what is missing.
	fetchUnavailableCode = "fetch_unavailable"
	// fetchNotSupportedCode is this client's name for a router without the
	// endpoint: it answers /v1/fetch with 404 or 405.
	fetchNotSupportedCode = "fetch_not_supported"
)

// The router's bounds on a fetch request, mirrored so a request it would
// refuse costs no call and its answer names the field.
const (
	fetchPagesMax     = 5
	fetchMaxCharsMin  = 1000
	fetchMaxCharsMax  = 50000
	fetchQueryMaxRune = 500
)

// sourceIDPattern is the router's page id: "s_" and 32 lowercase hex digits.
var sourceIDPattern = regexp.MustCompile(`^s_[0-9a-f]{32}$`)

// fetchPageRef is one page asked for: exactly one of a source_id or a URL.
type fetchPageRef struct {
	sourceID string
	url      string
}

// machineFetchRequest is the v1 request, after validation. The options are
// nil when the caller left them out, so the router's defaults apply.
type machineFetchRequest struct {
	pages    []fetchPageRef
	maxChars *int
	offset   *int
	query    *string
}

type wireFetchRequest struct {
	Version  *int            `json:"version"`
	Pages    json.RawMessage `json:"pages"`
	MaxChars json.RawMessage `json:"max_chars"`
	Offset   json.RawMessage `json:"offset"`
	Query    json.RawMessage `json:"query"`
}

type wireFetchPage struct {
	SourceID json.RawMessage `json:"source_id"`
	URL      json.RawMessage `json:"url"`
}

// decodeMachineFetchRequest reads exactly one v1 fetch request from r. It
// is as strict as search's: an unknown field, at the top or in a page, is
// refused, and so is every value outside the router's bounds.
func decodeMachineFetchRequest(r io.Reader) (machineFetchRequest, *inputError) {
	raw, ierr := readMachineRequest(r, "fetch")
	if ierr != nil {
		return machineFetchRequest{}, ierr
	}
	var wire wireFetchRequest
	if err := strictDecode(raw, &wire); err != nil {
		return machineFetchRequest{}, inputErrorf(codeUnknownField,
			"the request carries a field this version does not accept")
	}
	if len(wire.Pages) == 0 {
		return machineFetchRequest{}, inputErrorf(codeMissingPages, `the request needs "pages"`)
	}
	pages, ierr := validateFetchPages(wire.Pages)
	if ierr != nil {
		return machineFetchRequest{}, ierr
	}
	req := machineFetchRequest{pages: pages}
	if len(wire.MaxChars) > 0 {
		n, ierr := validateFetchMaxChars(wire.MaxChars)
		if ierr != nil {
			return machineFetchRequest{}, ierr
		}
		req.maxChars = &n
	}
	if len(wire.Offset) > 0 {
		n, ierr := validateFetchOffset(wire.Offset)
		if ierr != nil {
			return machineFetchRequest{}, ierr
		}
		req.offset = &n
	}
	if len(wire.Query) > 0 {
		q, ierr := validateFetchQuery(wire.Query)
		if ierr != nil {
			return machineFetchRequest{}, ierr
		}
		req.query = &q
	}
	return req, nil
}

// strictDecode decodes one JSON value into v, refusing a field v has no
// place for.
func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func validateFetchPages(raw json.RawMessage) ([]fetchPageRef, *inputError) {
	if isJSONNull(raw) {
		return nil, inputErrorf(codeInvalidPages, `"pages" must not be null`)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, inputErrorf(codeInvalidPages, `"pages" must be an array of objects`)
	}
	if len(entries) == 0 || len(entries) > fetchPagesMax {
		return nil, inputErrorf(codeInvalidPages,
			fmt.Sprintf(`"pages" must hold 1 to %d entries`, fetchPagesMax))
	}
	pages := make([]fetchPageRef, 0, len(entries))
	for i, e := range entries {
		var w wireFetchPage
		if t := bytes.TrimSpace(e); len(t) == 0 || t[0] != '{' {
			return nil, inputErrorf(codeInvalidPages, fmt.Sprintf(`"pages" entry %d must be an object`, i))
		}
		if err := strictDecode(e, &w); err != nil {
			return nil, inputErrorf(codeUnknownField,
				fmt.Sprintf(`"pages" entry %d carries a field other than "source_id" or "url"`, i))
		}
		hasID, hasURL := len(w.SourceID) > 0, len(w.URL) > 0
		if hasID == hasURL {
			return nil, inputErrorf(codeInvalidPages,
				fmt.Sprintf(`"pages" entry %d must carry exactly one of "source_id" or "url"`, i))
		}
		if hasID {
			id, ierr := validateSourceID(w.SourceID, i)
			if ierr != nil {
				return nil, ierr
			}
			pages = append(pages, fetchPageRef{sourceID: id})
			continue
		}
		u, ierr := validateFetchURL(w.URL, i)
		if ierr != nil {
			return nil, ierr
		}
		pages = append(pages, fetchPageRef{url: u})
	}
	return pages, nil
}

func validateSourceID(raw json.RawMessage, i int) (string, *inputError) {
	var s string
	if isJSONNull(raw) || json.Unmarshal(raw, &s) != nil || !sourceIDPattern.MatchString(s) {
		return "", inputErrorf(codeInvalidSourceID,
			fmt.Sprintf(`"pages" entry %d: "source_id" must be "s_" followed by 32 lowercase hex digits, as a search returned it`, i))
	}
	return s, nil
}

// validateFetchURL takes an absolute http or https URL with a host. It is
// sent as the caller wrote it: the router matches it against the pages its
// searches returned, and a URL this client rewrote might match none.
func validateFetchURL(raw json.RawMessage, i int) (string, *inputError) {
	bad := inputErrorf(codeInvalidURL,
		fmt.Sprintf(`"pages" entry %d: "url" must be an absolute http or https URL`, i))
	var s string
	if isJSONNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", bad
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", bad
	}
	return s, nil
}

func validateFetchMaxChars(raw json.RawMessage) (int, *inputError) {
	var n int
	if isJSONNull(raw) || json.Unmarshal(raw, &n) != nil || n < fetchMaxCharsMin || n > fetchMaxCharsMax {
		return 0, inputErrorf(codeInvalidMaxChars,
			fmt.Sprintf(`"max_chars" must be an integer from %d to %d`, fetchMaxCharsMin, fetchMaxCharsMax))
	}
	return n, nil
}

func validateFetchOffset(raw json.RawMessage) (int, *inputError) {
	var n int
	if isJSONNull(raw) || json.Unmarshal(raw, &n) != nil || n < 0 {
		return 0, inputErrorf(codeInvalidOffset, `"offset" must be an integer of 0 or more`)
	}
	return n, nil
}

// validateFetchQuery takes the words the router starts a long page's text
// near. A blank one is refused, as a blank objective is: a caller who wrote
// the key owes a value.
func validateFetchQuery(raw json.RawMessage) (string, *inputError) {
	var s string
	if isJSONNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", inputErrorf(codeInvalidQuery, `"query" must be a string`)
	}
	if strings.TrimSpace(s) == "" {
		return "", inputErrorf(codeInvalidQuery, `"query" must not be empty`)
	}
	if utf8.RuneCountInString(s) > fetchQueryMaxRune {
		return "", inputErrorf(codeInvalidQuery,
			fmt.Sprintf(`"query" must be at most %d characters`, fetchQueryMaxRune))
	}
	return s, nil
}

// body is what goes to POST /v1/fetch: the request without its version,
// and without any option the caller left out.
func (r machineFetchRequest) body() map[string]any {
	pages := make([]map[string]string, 0, len(r.pages))
	for _, p := range r.pages {
		if p.sourceID != "" {
			pages = append(pages, map[string]string{"source_id": p.sourceID})
		} else {
			pages = append(pages, map[string]string{"url": p.url})
		}
	}
	body := map[string]any{"pages": pages}
	if r.maxChars != nil {
		body["max_chars"] = *r.maxChars
	}
	if r.offset != nil {
		body["offset"] = *r.offset
	}
	if r.query != nil {
		body["query"] = *r.query
	}
	return body
}

// ── the router's answer ─────────────────────────────────────────────────

// routerFetchResponse is the router's answer to /v1/fetch, decoded
// permissively like search's (invariant 6). Pages is a pointer so a 2xx
// with no "pages" key, which is no answer to a fetch, is told apart from
// an empty list.
type routerFetchResponse struct {
	RequestID string             `json:"request_id"`
	Pages     *[]routerFetchPage `json:"pages"`
	Usage     *routerFetchUsage  `json:"usage,omitempty"`
}

// routerFetchPage is one page as the router returns it. Status is "ok",
// "failed", "not_recent" (the page is not among this tenant's recent search
// results) or "invalid". The numbers and the cached flag are pointers,
// because 0 and false are answers and absence is another.
type routerFetchPage struct {
	SourceID      string  `json:"source_id,omitempty"`
	URL           string  `json:"url,omitempty"`
	Title         string  `json:"title,omitempty"`
	Status        string  `json:"status"`
	Content       *string `json:"content,omitempty"`
	Offset        *int    `json:"offset,omitempty"`
	NextOffset    *int    `json:"next_offset,omitempty"`
	ContentLength *int    `json:"content_length,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Cached        *bool   `json:"cached,omitempty"`
	CostMicros    *int64  `json:"cost_micros,omitempty"`
}

type routerFetchUsage struct {
	CostMicros *int64 `json:"cost_micros,omitempty"`
	LatencyMS  *int64 `json:"latency_ms,omitempty"`
}

// machineFetchPage and machineFetchUsage are the router's, field for field,
// converted rather than copied so a field either gains stops the build.
// Content is the page text the caller asked for and is carried as it came:
// JSON makes its control bytes inert, and any other change would be this
// client editing a page.
type machineFetchPage struct {
	SourceID      string  `json:"source_id,omitempty"`
	URL           string  `json:"url,omitempty"`
	Title         string  `json:"title,omitempty"`
	Status        string  `json:"status"`
	Content       *string `json:"content,omitempty"`
	Offset        *int    `json:"offset,omitempty"`
	NextOffset    *int    `json:"next_offset,omitempty"`
	ContentLength *int    `json:"content_length,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Cached        *bool   `json:"cached,omitempty"`
	CostMicros    *int64  `json:"cost_micros,omitempty"`
}

type machineFetchUsage struct {
	CostMicros *int64 `json:"cost_micros,omitempty"`
	LatencyMS  *int64 `json:"latency_ms,omitempty"`
}

type machineFetchResult struct {
	RequestID string             `json:"request_id,omitempty"`
	Pages     []machineFetchPage `json:"pages"`
	Usage     *machineFetchUsage `json:"usage,omitempty"`
}

type fetchEnvelope struct {
	machineHeader
	HTTPStatus   int                 `json:"http_status,omitempty"`
	RequestID    string              `json:"request_id,omitempty"`
	RetryAfterMS *int64              `json:"retry_after_ms,omitempty"`
	Result       *machineFetchResult `json:"result,omitempty"`
	Error        *machineError       `json:"error,omitempty"`
}

// fetchOutcome is one fetch's result, as a value the envelope is built
// from. Fault is search's vocabulary: the transport is the same, and so
// are the ways it fails.
type fetchOutcome struct {
	Fault         searchFault
	Err           error
	HTTPStatus    int
	RouterErr     searchHostError
	HasRouterErr  bool
	RetryAfterMS  int64
	HasRetryAfter bool
	RequestID     string
	Response      routerFetchResponse
}

// performFetch sends one POST under ctx's deadline. Never retried: a fetch
// is billed per page, and nothing the router says licenses a second one.
func performFetch(ctx context.Context, now func() time.Time, endpoint, key string, body map[string]any) fetchOutcome {
	var out fetchOutcome
	attempt, err := postRouter(ctx, routerClient(), endpoint, key, body, nil)
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
	if err := json.Unmarshal(attempt.Raw, &out.Response); err != nil {
		out.Err, out.Fault = errBodyNotJSON, faultInvalidResponse
		return out
	}
	if out.Response.Pages == nil {
		out.Err, out.Fault = errors.New("the router's fetch response carries no pages"), faultInvalidResponse
		return out
	}
	// The body's id wins, as it does for a search; the header stands in when
	// the body has none. A fetch records nothing for mining, so an answer
	// with neither is still an answer, and simply carries no id.
	out.RequestID = strings.TrimSpace(out.Response.RequestID)
	if out.RequestID == "" {
		out.RequestID = strings.TrimSpace(attempt.Header.Get("X-Request-Id"))
	}
	return out
}

// classifyFetch is search's classification, with the router's two answers
// that only a fetch gets read first: an account that is not claimed, and a
// router that has no fetch endpoint. Neither is the request's fault and
// neither changes on a retry.
func classifyFetch(out fetchOutcome) searchClassification {
	if out.Fault != faultRouterStatus {
		return classifySearch(searchOutcome{Fault: out.Fault})
	}
	switch {
	case out.HTTPStatus == http.StatusForbidden && out.HasRouterErr && out.RouterErr.Code == fetchUnavailableCode:
		// The account is not claimed. connect is the action whose meaning
		// is exactly that: the claim workflow needs attention.
		return searchClassification{exitClientErr, fetchUnavailableCode, false, actionConnect}
	case out.HTTPStatus == http.StatusNotFound, out.HTTPStatus == http.StatusMethodNotAllowed:
		return searchClassification{exitClientErr, fetchNotSupportedCode, false, actionReport}
	}
	return classifyRouterStatus(searchOutcome{
		HTTPStatus: out.HTTPStatus, RouterErr: out.RouterErr, HasRouterErr: out.HasRouterErr,
	})
}

func fetchEnvelopeOf(out fetchOutcome) (fetchEnvelope, int) {
	c := classifyFetch(out)
	env := fetchEnvelope{
		machineHeader: newMachineHeader("fetch", c.ExitCode, c.Code, c.Retryable, c.Action),
		HTTPStatus:    out.HTTPStatus,
	}
	if out.HasRetryAfter && (c.Retryable || out.HTTPStatus == http.StatusTooManyRequests) {
		ms := out.RetryAfterMS
		env.RetryAfterMS = &ms
	}
	if out.Fault == faultNone {
		env.RequestID = out.RequestID
		result := &machineFetchResult{RequestID: out.RequestID, Pages: []machineFetchPage{}}
		for _, p := range *out.Response.Pages {
			result.Pages = append(result.Pages, machineFetchPage(p))
		}
		if u := out.Response.Usage; u != nil {
			mu := machineFetchUsage(*u)
			result.Usage = &mu
		}
		env.Result = result
		return env, c.ExitCode
	}
	if out.HasRouterErr {
		env.Error = routerMessage(out.RouterErr.Error)
	}
	if env.Error == nil {
		switch c.Code {
		case fetchNotSupportedCode:
			env.Error = clientMessage(errors.New("this router has no fetch endpoint; use the host's own fetch tool"))
		default:
			env.Error = clientMessage(out.Err)
		}
	}
	return env, c.ExitCode
}

func failFetch(stdout io.Writer, exitCode int, code, action string, err error) int {
	emitMachine(stdout, fetchEnvelope{
		machineHeader: newMachineHeader("fetch", exitCode, code, false, action),
		Error:         clientMessage(err),
	})
	return exitCode
}

// ── the command ─────────────────────────────────────────────────────────

func cmdFetch(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return fetchMain(time.Now, args, stdin, stdout, stderr, getenv)
}

func fetchMain(now func() time.Time, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if !hasBoolFlag(args, "stdin") {
		fmt.Fprintln(stderr, "jevlin fetch: --stdin is required: send one version-1 JSON request on stdin")
		return exitUsage
	}
	// Machine mode only, so everything is in the envelope, as for search
	// --stdin: nothing on stderr for a caller to parse instead.
	stderr = io.Discard
	fs := newFlagSet("fetch", stderr)
	cfgPath := onceStringFlag(fs, "config", "path to TOML config file; given at most once")
	timeout := fs.Duration("timeout", defaultSearchTimeout, "whole-fetch deadline, covering connect, headers and body")
	fs.Bool("stdin", false, "read one version-1 JSON fetch request from stdin and answer with the machine envelope")
	if err := fs.Parse(args); err != nil {
		return failFetch(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("the flags could not be parsed"))
	}
	if len(fs.Args()) > 0 {
		return failFetch(stdout, exitUsage, codeUnexpectedArgument, actionFixInput,
			errors.New("--stdin takes the request from stdin, not from the command line"))
	}
	if *timeout <= 0 {
		return failFetch(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("-timeout must be positive"))
	}
	req, ierr := decodeMachineFetchRequest(stdin)
	if ierr != nil {
		return failFetch(stdout, exitUsage, ierr.Code, actionFixInput, ierr)
	}

	cfg, _, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		return failFetch(stdout, exitTransport, "config_unreadable", actionFixInput, err)
	}
	if cfg.Miner.RouterURL == nil {
		return failFetch(stdout, exitTransport, "no_router_configured", actionFixInput,
			errors.New("no router configured (miner.router_url or a [[provider]] upstream)"))
	}
	key, _, err := resolveAPIKey(getenv, cfg.Miner)
	if err != nil {
		return failFetch(stdout, exitClientErr, "credential_unreadable", actionConnect, err)
	}
	if key == "" {
		return failFetch(stdout, exitClientErr, string(faultNoCredential), actionConnect,
			errors.New("this installation holds no search credential; run jevlin connect"))
	}

	ctx, cancel := searchDeadline(*timeout)
	defer cancel()
	out := performFetch(ctx, now, strings.TrimRight(cfg.Miner.RouterURL.String(), "/")+"/v1/fetch", key, req.body())
	env, code := fetchEnvelopeOf(out)
	emitMachine(stdout, env)
	return code
}
