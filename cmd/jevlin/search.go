package main

// The search command: the CLI as the tool.
//
//	jevlin search [-tier fast] [-format json|model] <query words>
//
// posts the query straight to the search router and prints the answer.
// No daemon, no proxy, no tool server: a skill names this command and any
// agent that can run a command can use it.
//
//   - the router meters the request against the participant's own key,
//     which is sent in Authorization;
//   - the trace envelope rides in the body so the router can group one
//     task's searches. It comes from, in order: the JEVLIN_TRACE_BRIDGE
//     variable a hook put in front of this command; the workspace lineage
//     file a hook wrote (named by JEVLIN_LINEAGE, or found by walking
//     up from the working directory); or, with no hook at all, a hashed
//     per-shell session identity. JEVLIN_TRACE=off sends none.
//
// Nothing about the search is recorded on this machine.
//
// Two knowing trade-offs, documented rather than hidden: an argv query
// rides in process arguments (visible in `ps` and shell history on the
// user's own machine — it is not a credential; the key comes from the
// environment or the owner-only credentials file, see credentials.go), and
// a search with no hook around it has thinner lineage. The first of those
// is why agents are pointed at `search --stdin` instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const (
	searchUserAgent   = "jevlin"
	renderTotalCap    = 64 << 10
	renderAnswerCap   = 4000
	renderSnippetCap  = 400
	renderCitationCap = 8
)

type searchOps struct {
	getppid  func() int
	hostname func() (string, error)
	// traceKey returns the installation's trace key from the state dir,
	// creating it on first use. nil or an error means there is none.
	traceKey func(stateDir string) ([]byte, error)
	getwd    func() (string, error)
	// spawnConnectResume starts the detached connect -resume (agent
	// onboarding design §5.5's "next invocation of anything" — in
	// practice, search: the one path an agent invokes routinely). Called
	// after every served search. shouldResume gates it on a cheap local
	// disk check first, so this never fires when there is nothing to
	// resume.
	spawnConnectResume func(cfgPath string) error
	now                func() time.Time
	// hook is the filesystem the lineage file is read and bumped through.
	hook hookOps
}

func realSearchOps() searchOps {
	return searchOps{
		getppid:            os.Getppid,
		hostname:           os.Hostname,
		traceKey:           loadTraceKey,
		getwd:              os.Getwd,
		spawnConnectResume: startConnectResume,
		now:                time.Now,
		hook:               realHookOps(),
	}
}

func loadTraceKey(stateDir string) ([]byte, error) {
	store, err := auth.OpenStoreExisting(stateDir)
	if err != nil {
		return nil, err
	}
	return store.TraceKey()
}

func cmdSearch(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	ops := realSearchOps()
	ops.hook.getenv = getenv
	return searchMain(ops, args, stdin, stdout, stderr, getenv)
}

// searchFault names, as a value rather than as prose, why a search did not
// produce a protocol success. The empty value means it did.
//
// Every recovery decision the client or an agent makes is derived from one
// of these plus the HTTP status — never from the text of an error. That is
// the whole reason the type exists: message wording that classification
// depends on is an API nobody agreed to, and one rephrasing silently
// changes what a participant is told to do.
type searchFault string

const (
	faultNone            searchFault = ""
	faultNoCredential    searchFault = "not_connected"
	faultTransport       searchFault = "transport"
	faultTimeout         searchFault = "search_timeout"
	faultCanceled        searchFault = "canceled"
	faultRouterStatus    searchFault = "router_status"
	faultOversized       searchFault = "router_response_too_large"
	faultInvalidResponse searchFault = "invalid_router_response"
	faultMissingID       searchFault = "missing_request_id"
)

// routerAttempt is one POST's answer, already bounded.
type routerAttempt struct {
	Status  int
	Header  http.Header
	Raw     []byte
	BodyErr error
}

// searchCall is everything one search sends. Recency, DomainFilter,
// MaxResults and Providers are absent (nil) unless the --stdin caller
// supplied them: the router accepts their absence as "use your default",
// and sending a zeroed value instead would silently override that default
// with one this client chose, not one the caller or the router asked for.
type searchCall struct {
	Endpoint     string
	Key          string
	Query        string
	Tier         string
	Recency      *string
	DomainFilter []string
	MaxResults   *int
	Providers    []string
	// View is "" or "merged" — never "full". "full" is this client's own
	// rendering default and not a value the router accepts; "merged" asks
	// the router to add its own merged list and per-arm indexes to the
	// response, and changes nothing else about how the search runs.
	View string
	// Objective and SearchQueries are the query forms (Jevlin-core #55),
	// absent unless the --stdin caller sent them. A router that predates
	// them refuses the whole request, so performSearch's one retry leaves
	// them out (FormsDropped).
	Objective     *string
	SearchQueries []string
	// MergedLimit and MaxChars size the merged view (the router's window
	// and its excerpt budget), absent unless the --stdin caller sent them.
	// The router accepts them only beside view "merged", which every
	// --stdin search sends.
	MergedLimit *int
	MaxChars    *int
	// PageChars caps each page's excerpt. FastSet names the user's fast
	// preset and rides only with tier "fast".
	PageChars *int
	FastSet   string
	// Cache is "" or "refresh" (--fresh, or "cache" in a --stdin request)
	// and goes out only when set: the router's default is the field's absence.
	Cache string
	Trace *traceEnvelope
}

// searchOutcome is the structured result of running a search. Both
// renderers — the human one and the machine envelope — are built from it,
// so they cannot disagree, and neither is produced by parsing the other.
type searchOutcome struct {
	Fault searchFault
	// Err is diagnostic only. Nothing branches on its text.
	Err           error
	HTTPStatus    int
	RouterErr     searchHostError
	HasRouterErr  bool
	RetryAfterMS  int64
	HasRetryAfter bool

	// ResponseComplete is true only when headers arrived AND the body was
	// read to its end. It is what the detached connect -resume is gated
	// on, and it exists as a field rather than a guess because before
	// PR6 the search returned early on a transport or body-read failure
	// and never reached shouldResume at all. A 4xx or 5xx that arrived
	// whole is complete; a stalled, cut or oversized body is not,
	// whatever its headers said.
	ResponseComplete bool

	Success  routerSuccess
	RawBody  []byte
	Started  time.Time
	Finished time.Time
	Attempts int
	Traced   bool
	Retried  bool
	// FormsDropped: the retry went without objective and search_queries.
	// Either the router refused the request as malformed_json while it
	// carried them, or it refused the trace, and a router that old
	// predates the forms as well.
	FormsDropped bool
}

func (o searchOutcome) ok() bool { return o.Fault == faultNone }

// searchMachineRequested answers "did the caller ask for --stdin" before
// flag parsing, so a flag error can still be reported in the format the
// caller asked for rather than as prose they are not reading.
func searchMachineRequested(args []string) bool { return hasBoolFlag(args, "stdin") }

func searchMain(ops searchOps, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	machine := searchMachineRequested(args)
	// In machine mode everything is in the envelope. An expected protocol
	// outcome must not also explain itself on stderr: a caller reading
	// both would get the same failure twice, in two formats, and the one
	// that is not the contract is the one it would be tempted to parse.
	if machine {
		stderr = io.Discard
	}

	fs := newFlagSet("search", stderr)
	cfgPath := onceStringFlag(fs, "config", "path to TOML config file; given at most once")
	tier := fs.String("tier", "", "search tier accepted by the router, e.g. fast; empty = the router's default")
	format := fs.String("format", "json", "output: json (the router's bytes, verbatim) or model (compact text for an agent)")
	viewFlag := fs.String("view", "", `router view: "merged" adds the router's own cross-provider merged list and per-arm indexes to the raw response; empty or "full" asks for none`)
	timeout := fs.Duration("timeout", defaultSearchTimeout, "whole-search deadline, covering connect, headers, body and the one trace-compatibility retry")
	fs.Bool("stdin", false, "read one version-1 JSON search request from stdin and answer with the machine envelope")
	fresh := fs.Bool("fresh", false, "run the search live instead of serving a result cached in the last 15 minutes; costs the same as any live search")
	if err := fs.Parse(args); err != nil {
		if machine {
			return failSearchInput(stdout, inputErrorf(codeInvalidFlags, "the flags could not be parsed"))
		}
		return exitUsage
	}

	var query string
	var recency *string
	var domainFilter []string
	var maxResults *int
	var providers []string
	var view string
	var objective *string
	var searchQueries []string
	var mergedLimit, maxChars, pageChars *int
	var cache string
	if machine {
		// No positional query in machine mode: two sources for the same
		// value is how a caller ends up sending one and escaping the
		// other.
		if len(fs.Args()) > 0 {
			return failSearchInput(stdout, inputErrorf(codeUnexpectedArgument,
				"--stdin takes the query from the request on stdin, not from the command line"))
		}
		req, err := decodeMachineSearchRequest(stdin)
		if err != nil {
			var ierr *inputError
			if errors.As(err, &ierr) {
				return failSearchInput(stdout, ierr)
			}
			return failSearchInput(stdout, inputErrorf(codeInvalidJSON, "the request could not be read"))
		}
		query = req.query
		if req.tier != "" {
			*tier = req.tier
		}
		recency = req.recency
		domainFilter = req.domainFilter
		maxResults = req.maxResults
		providers = req.providers
		view = req.view
		objective = req.objective
		searchQueries = req.searchQueries
		mergedLimit = req.mergedLimit
		maxChars = req.maxChars
		pageChars = req.pageChars
		cache = req.cache
		// Two sources for one value: a --stdin request says "cache" itself.
		if *fresh {
			return failSearchInput(stdout, inputErrorf(codeInvalidFlags,
				"--fresh is for the command line; a --stdin request carries \"cache\": \"refresh\""))
		}
	} else {
		if *fresh {
			cache = "refresh"
		}
		query = strings.TrimSpace(strings.Join(fs.Args(), " "))
		if query == "" {
			fmt.Fprintln(stderr, "jevlin search: a query is required: jevlin search [-tier fast] [-format model] <query words>, or --stdin")
			return exitUsage
		}
		if *format != "json" && *format != "model" {
			fmt.Fprintf(stderr, "jevlin search: -format must be json or model, not %q\n", *format)
			return exitUsage
		}
		if *viewFlag != "" && *viewFlag != "full" && *viewFlag != "merged" {
			fmt.Fprintf(stderr, "jevlin search: -view must be full or merged, not %q\n", *viewFlag)
			return exitUsage
		}
		view = *viewFlag
	}
	// Refused rather than treated as "no limit": a zero or negative budget
	// used to be the only state this command had, and restoring it by
	// accident is the failure the deadline exists to prevent.
	if *timeout <= 0 {
		if machine {
			return failSearchInput(stdout, inputErrorf(codeInvalidFlags, "-timeout must be positive"))
		}
		fmt.Fprintf(stderr, "jevlin search: -timeout must be positive, not %s\n", *timeout)
		return exitUsage
	}

	cfg, cfgSource, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		if machine {
			return failSearchSetup(stdout, exitTransport, "config_unreadable", actionFixInput, err)
		}
		fmt.Fprintln(stderr, "jevlin: "+configProblem(cfgSource, err))
		return exitTransport
	}
	if cfg.Search.RouterURL == nil {
		if machine {
			return failSearchSetup(stdout, exitTransport, "no_router_configured", actionFixInput,
				errors.New("no router configured ([router] url)"))
		}
		fmt.Fprintln(stderr, "jevlin: no router configured ([router] url)")
		return exitTransport
	}
	// The caller's tier wins (the -tier flag, or the stdin request's
	// "tier"); when neither named one, the participant's own default from
	// `jevlin agents tier`, then [router] tier, is sent; when both are
	// unset, the request carries no tier and the router applies the
	// tenant's default. The agent is told to leave the field alone
	// (skill.md), so in practice this is the participant's setting unless
	// the agent escalated.
	if *tier == "" {
		*tier = searchTierDefault(cfgSource, cfg.Search, getenv)
	}
	if machine {
		mergedLimit, pageChars = agentOutputDefaults(mergedLimit, maxChars, pageChars, searchQueries, *tier)
	}
	key, keySrc, err := resolveAPIKey(getenv, cfg.Search)
	if err == nil && keySrc == keyFromEnv {
		err = envKeyRefusedFor(*cfgPath, cfg, getenv)
	}
	if err != nil {
		if machine {
			if keyRefusedForOrigin(err) {
				return failSearchSetup(stdout, exitUsage, keyOriginCode, actionFixInput, err)
			}
			return failSearchSetup(stdout, exitClientErr, "credential_unreadable", actionConnect, err)
		}
		fmt.Fprintln(stderr, "jevlin:", err)
		if keyRefusedForOrigin(err) {
			return exitUsage
		}
		return exitClientErr
	}
	if key == "" {
		if machine {
			// The value is never named, only its absence. There is
			// nothing in this envelope to leak.
			return failSearchSetup(stdout, exitClientErr, string(faultNoCredential), actionConnect,
				errors.New("this installation holds no search credential; run jevlin connect"))
		}
		fmt.Fprintln(stderr, "jevlin: no API key; the router needs your sr- key to meter the search. Store it once with: jevlin login   (or export JEVLIN_API_KEY)")
		return exitClientErr
	}

	ctx, cancel := searchDeadline(*timeout)
	defer cancel()

	trace, foreignBridge := searchTraceFailingOpen(ops, cfg.Search, cfg.Search.StateDir, getenv, stderr)
	if foreignBridge {
		// Said on stderr, and only there. `-format json` prints the router's
		// own bytes, so there is no envelope of ours to put it in without
		// rewriting the router's answer; and the machine envelope is what the
		// MODEL reads, and a model that knows this variable exists is one of
		// the ways a foreign bridge arrives. A participant debugging a
		// trace runs the search by hand in the host's terminal, where the
		// same variables are set and this line is in front of them — the
		// same place the trace_unsupported retry below is reported.
		fmt.Fprintln(stderr, "jevlin search: ignoring "+bridgeEnv+": this session's host declared the lineage file ("+lineageEnv+") as its trace channel, so a bridge here was written by something else")
	}
	// Only "merged" travels: it is the one view the router accepts, while
	// "full" is this client's own rendering choice and would be refused
	// upstream with a 400. On --stdin it travels whatever the caller asked,
	// because the envelope's merged list IS the router's: the caller's view
	// decides only how much of the router's answer the envelope repeats
	// (the candidates, and each page's excerpts and canonical_url, in
	// "full"). The human form sends it only for -view merged, which is what
	// that flag has always meant.
	forwardView := ""
	if view == "merged" || machine {
		forwardView = "merged"
	}
	out := performSearch(ctx, ops.now, searchCall{
		Endpoint:      strings.TrimRight(cfg.Search.RouterURL.String(), "/") + "/v1/search",
		Key:           key,
		Query:         query,
		Tier:          *tier,
		Recency:       recency,
		DomainFilter:  domainFilter,
		MaxResults:    maxResults,
		Providers:     providers,
		View:          forwardView,
		Objective:     objective,
		SearchQueries: searchQueries,
		MergedLimit:   mergedLimit,
		MaxChars:      maxChars,
		PageChars:     pageChars,
		Cache:         cache,
		FastSet:       fastSetFor(ctx, *tier, cfgSource, strings.TrimRight(cfg.Search.RouterURL.String(), "/"), key, getenv),
		Trace:         trace,
	})
	if out.Retried {
		fmt.Fprintln(stderr, "jevlin search: the router answered "+traceUnsupportedCode+"; retrying once without the trace")
	}

	if out.ok() {
		// The turn end is sent only for a turn in which a search was SERVED.
		markTurnSearched(ops.hook, cfg.Search, trace)
	}

	// An unclaimed participant still needs the claim to resolve eventually.
	// shouldResume is a cheap local disk check (agent onboarding design
	// §5.5) — it costs nothing and spawns nothing when there is no stored
	// registration to resume.
	// Gated on a completed response, which is the boundary this command
	// had before the outcome was made structured: a transport failure or a
	// body that never finished used to return before this line was
	// reached. shouldResume itself is unchanged — it is still the cheap
	// local disk check — but it is not consulted for a search that never
	// got an answer.
	if out.ResponseComplete && ops.spawnConnectResume != nil && shouldResume(cfg, getenv) {
		_ = ops.spawnConnectResume(*cfgPath) // best effort; the next search resumes it if this one could not even start
	}

	if machine {
		env, code := searchEnvelopeOf(out, view)
		emitMachine(stdout, env)
		return code
	}
	return renderSearchForHuman(out, *format, keySrc, stdout, stderr)
}

// renderSearchForHuman is the terminal path: the router's own bytes for
// -format json, compact text for -format model, and a bounded diagnostic
// on stderr. An invalid success is the one case that prints no body at
// all — echoing a malformed or oversized router response back is how
// untrusted bytes reach a terminal unbounded.
func renderSearchForHuman(out searchOutcome, format string, keySrc keySource, stdout, stderr io.Writer) int {
	if out.ok() {
		if format == "model" {
			fmt.Fprint(stdout, renderForModel(out.Success.Response))
		} else {
			_, _ = stdout.Write(out.Success.Raw)
		}
		return exitOK
	}
	switch out.Fault {
	case faultTimeout:
		fmt.Fprintln(stderr, "jevlin: the search did not finish within its deadline; retry, or raise -timeout")
		return exitTransport
	case faultCanceled:
		fmt.Fprintln(stderr, "jevlin: the search was canceled")
		return exitTransport
	case faultTransport:
		fmt.Fprintln(stderr, "jevlin: router:", out.Err)
		return exitTransport
	case faultOversized, faultInvalidResponse, faultMissingID:
		fmt.Fprintf(stderr, "jevlin: the router answered HTTP %d, but the answer is not a usable search response: %v\n",
			out.HTTPStatus, out.Err)
		return exitServerErr
	}
	// A router status. -format json keeps the documented raw-router
	// compatibility output; -format model must not, because the body is
	// remote text and model output goes to a terminal. renderRouterFailure
	// is the sanitized, bounded summary instead.
	if format == "model" {
		fmt.Fprint(stdout, renderRouterFailure(out))
	} else {
		_, _ = stdout.Write(out.RawBody)
	}
	switch {
	case out.HTTPStatus >= 500:
		fmt.Fprintf(stderr, "\njevlin: HTTP %d\n", out.HTTPStatus)
		return exitServerErr
	case out.HTTPStatus == http.StatusUnauthorized:
		fmt.Fprintf(stderr, "\njevlin: HTTP %d — the router refused the key (from %s); store a valid one with: jevlin login\n", out.HTTPStatus, keySrc)
		return exitClientErr
	case out.HTTPStatus == http.StatusPaymentRequired:
		// Each 402 names the one remedy that clears it, branched on the
		// router's own machine code — never its message — and only for
		// codes this client knows. A 402 with an unrecognized code keeps
		// the bare status line rather than guessing at advice. No portal
		// URL is printed here: the portal's origin is configuration
		// ([platform] url), not something search resolves, and a
		// hardcoded address would be wrong for every other deployment.
		code := ""
		if out.HasRouterErr {
			code = out.RouterErr.Code
		}
		switch code {
		case "credits_exhausted":
			fmt.Fprintf(stderr, "\njevlin: HTTP %d — the account is out of credit; retrying will not fix it. Top up at the platform console.\n", out.HTTPStatus)
		case "spend_ceiling":
			fmt.Fprintf(stderr, "\njevlin: HTTP %d — a spend cap is reached. `jevlin limits` shows the caps and today's spend; the day resets at UTC midnight, the week on Monday, the month on the 1st.\n", out.HTTPStatus)
		case "budget_exceeded":
			fmt.Fprintf(stderr, "\njevlin: HTTP %d — no provider set fits this tier's per-query ceiling; retry with a cheaper -tier, not as-is.\n", out.HTTPStatus)
		default:
			fmt.Fprintf(stderr, "\njevlin: HTTP %d\n", out.HTTPStatus)
		}
		return exitClientErr
	default:
		fmt.Fprintf(stderr, "\njevlin: HTTP %d\n", out.HTTPStatus)
		return exitClientErr
	}
}

// searchTierDefault is the band a request carries when the caller named
// none: the participant's `jevlin agents tier` file beside the config,
// then [router] tier, then nothing — and nothing means the router applies
// the tenant's default, which this client does not guess at.
func searchTierDefault(cfgSource string, m config.Search, getenv func(string) string) string {
	// The path is tierPathFor's and the read readTierSetting's, both the
	// command's own, so `agents tier status` and the search agree.
	if t := readTierFile(readTierSetting, tierPathFor(cfgSource, getenv)); t != "" {
		return t
	}
	return m.Tier
}

// tierFileMaxBytes bounds the tier file: one band name and a newline.
const tierFileMaxBytes = 64

// readTierSetting is the one read of the tier file, for the search and for
// `agents tier`: one no-follow read of a regular file in the installation's
// own root, bounded, as credentials.json is read. A link planted there is
// not followed, and a file over the bound is not read.
func readTierSetting(path string) ([]byte, error) {
	b, _, err := fsx.ReadRegularNoFollow(path, tierFileMaxBytes)
	return b, err
}

// performSearch runs the whole protocol operation under one context: at
// most two POSTs, the second only on an explicit trace_unsupported, and
// both sharing ctx's single absolute deadline.
func performSearch(ctx context.Context, now func() time.Time, call searchCall) (out searchOutcome) {
	body := map[string]any{"query": call.Query}
	if call.Tier != "" {
		body["tier"] = call.Tier
	}
	if call.Recency != nil {
		body["recency"] = *call.Recency
	}
	if call.DomainFilter != nil {
		body["domain_filter"] = call.DomainFilter
	}
	if call.MaxResults != nil {
		body["max_results"] = *call.MaxResults
	}
	if call.Providers != nil {
		body["providers"] = call.Providers
	}
	if call.View != "" {
		body["view"] = call.View
	}
	if call.Objective != nil {
		body["objective"] = *call.Objective
	}
	if call.SearchQueries != nil {
		body["search_queries"] = call.SearchQueries
	}
	if call.MergedLimit != nil {
		body["merged_limit"] = *call.MergedLimit
	}
	if call.MaxChars != nil {
		body["max_chars"] = *call.MaxChars
	}
	if call.PageChars != nil {
		body["page_chars"] = *call.PageChars
	}
	if call.FastSet != "" {
		body["fast_set"] = call.FastSet
	}
	if call.Cache != "" {
		body["cache"] = call.Cache
	}
	out = searchOutcome{Traced: call.Trace != nil}
	if out.Traced {
		body["trace"] = call.Trace
		// The router reads session identity in two places: trace.session_id
		// threads the console's Trajectories view, and a top-level
		// session_id (mirrored into X-Session-Id by postSearch) feeds its
		// live reformulation tracker — "put the same id in both to get
		// both" (the router's skill file). The mirror is the same hashed id
		// the envelope already carries, so nothing new leaves the machine,
		// and it exists only while an envelope rides: JEVLIN_TRACE=off
		// stops both together.
		if call.Trace.SessionID != "" {
			body["session_id"] = call.Trace.SessionID
		}
	}

	// CheckRedirect: this request carries the participant's sr- key in
	// Authorization. net/http's default follows up to ten redirects and
	// replays both the header and the body on a 307/308 — a compromised or
	// misconfigured router redirecting this request would hand the key to
	// whatever host it named. Same-origin bounded, not refused outright: a
	// router legitimately redirecting within its own origin must not break
	// every search. Timeout stays 0: the deadline is ctx's, so it covers
	// the body read too, which a client Timeout would also do but could
	// not share across the two attempts. Transport: searchTransport's own
	// test override takes priority; production leaves it nil and falls
	// back to searchDefaultTransport.
	client := routerClient()

	out.Started = now()
	attempt, err := postSearch(ctx, client, call, body)
	out.Attempts++
	// The router said, in its own machine code, that it does not accept a
	// field this request carried. One retry without it, on the SAME ctx, so
	// the fallback gets only what is left of the original budget. Never
	// recursive: this is the only place a second POST is issued, and two
	// answers license it, each by its exact code.
	_, carriesForms := body["objective"]
	if _, ok := body["search_queries"]; ok {
		carriesForms = true
	}
	dropForms := func() {
		delete(body, "objective")
		delete(body, "search_queries")
		out.FormsDropped = true
	}
	switch {
	case err != nil:
	case out.Traced && searchTraceUnsupported(attempt.Status, attempt.Raw, attempt.BodyErr):
		// The session mirror goes with the trace: a router old enough to
		// refuse the trace field predates the top-level session_id too,
		// and this retry exists exactly for those routers. It predates the
		// query forms as well, and there is no second retry to spend on
		// them, so they go too.
		delete(body, "trace")
		delete(body, "session_id")
		if carriesForms {
			dropForms()
		}
		out.Retried = true
		attempt, err = postSearch(ctx, client, call, body)
		out.Attempts++
	case carriesForms && searchFormsRefused(attempt.Status, attempt.Raw, attempt.BodyErr):
		// A router that predates objective and search_queries refuses the
		// whole request over them, as malformed_json, and so does one whose
		// body limit the forms pushed this request past: the code cannot
		// tell the two apart, and the answer to both is the same search on
		// query alone. A malformed_json to a request without the forms is
		// not this, and is answered as it came.
		dropForms()
		attempt, err = postSearch(ctx, client, call, body)
		out.Attempts++
	}
	out.Finished = now()

	if err != nil {
		out.Err = err
		out.Fault = contextFault(ctx, err, faultTransport)
		return out
	}
	out.HTTPStatus = attempt.Status
	out.RawBody = attempt.Raw
	if ms, ok := retryAfterMS(attempt.Header, now()); ok {
		out.RetryAfterMS, out.HasRetryAfter = ms, true
	}

	// Headers arriving is not a response arriving. If the body read ended
	// because the shared search context expired or was canceled, that is
	// what happened — regardless of what the status line said. A 401 whose
	// body stalled until the deadline is a timeout the caller may retry,
	// not an authorization failure to send them to `login` over.
	if attempt.BodyErr != nil {
		if f := contextFault(ctx, attempt.BodyErr, faultNone); f != faultNone {
			out.Err, out.Fault = attempt.BodyErr, f
			return out
		}
	}
	out.ResponseComplete = attempt.BodyErr == nil

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
		if errors.Is(attempt.BodyErr, errBodyOversized) {
			out.Fault = faultOversized
		} else {
			out.Fault = faultInvalidResponse
		}
		return out
	}
	success, derr := decodeRouterSuccess(attempt.Raw, attempt.Header.Get("X-Request-Id"))
	if derr != nil {
		out.Err = derr
		if errors.Is(derr, errNoRequestID) {
			out.Fault = faultMissingID
		} else {
			out.Fault = faultInvalidResponse
		}
		return out
	}
	out.Success = success
	return out
}

// contextFault separates a deliberate cancellation from an expired
// deadline, falling back to otherwise. An agent may retry a timeout; it
// must not automatically retry something a person interrupted.
func contextFault(ctx context.Context, err error, otherwise searchFault) searchFault {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return faultTimeout
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return faultCanceled
	default:
		return otherwise
	}
}

// routerClient is the one client every call to the router is made with:
// search's and fetch's. Both carry the participant's sr- key, so both share
// the redirect policy described above, and the deadline is the caller's
// ctx, never a client Timeout.
func routerClient() *http.Client {
	transport := searchTransport
	if transport == nil {
		transport = searchDefaultTransport
	}
	return &http.Client{Timeout: 0, CheckRedirect: auth.SameOriginRedirects, Transport: transport}
}

func postSearch(ctx context.Context, client *http.Client, call searchCall, body map[string]any) (routerAttempt, error) {
	return postRouter(ctx, client, call.Endpoint, call.Key, body, func(req *http.Request) {
		if sid, ok := body["session_id"].(string); ok && sid != "" {
			// Read off the body, not off call.Trace, so the header cannot
			// outlive the field: the trace-compatibility retry deletes the
			// key, and the header must disappear with it.
			req.Header.Set("X-Session-Id", sid)
		}
	})
}

// postRouter POSTs body as JSON to one of the router's endpoints with the
// participant's key, and reads the answer against searchMaxBody. header,
// when set, adds what only one endpoint sends.
func postRouter(ctx context.Context, client *http.Client, endpoint, key string, body map[string]any, header func(*http.Request)) (routerAttempt, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return routerAttempt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return routerAttempt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientIdentifier())
	req.Header.Set("Authorization", "Bearer "+key)
	if header != nil {
		header(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return routerAttempt{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	attempt := routerAttempt{Status: resp.StatusCode, Header: resp.Header}
	if resp.ContentLength > searchMaxBody {
		// Declared oversized: refuse without draining it. The streaming
		// max+1 check below stays authoritative — Content-Length is
		// optional and can lie — but when it is present and honest there
		// is no reason to read the ceiling to learn what it already said.
		attempt.BodyErr = errBodyOversized
		return attempt, nil
	}
	attempt.Raw, attempt.BodyErr = readBoundedBody(resp.Body, searchMaxBody)
	return attempt, nil
}

// searchTrace picks the envelope for this search: the host's own channel —
// bridge or lineage file — or the per-shell fallback. nil means send none.
//
// A search believes its host's channel, not whatever variable it finds.
// A host has one channel. Those that can rewrite a command hand the envelope
// over in JEVLIN_TRACE_BRIDGE; Cursor cannot, so its session-start hook
// exports JEVLIN_LINEAGE, and by that declaration says it writes no
// bridge. For such a search a bridge variable is somebody else's — another
// host's hook run by this one, a model that read this repository's
// documentation — and it is dropped unread: not
// decoded, not compared, not used as a fallback when the lineage file turns
// out to be missing. foreignBridge reports that this happened, so the caller
// can say so.
//
// The rule is about the channel, not about catching a wrong label: a bridge
// naming the very harness the host declared is dropped just the same, because
// its session, turn and text are still not this host's. What it guarantees
// is that a search never reaches the router labeled with a harness other
// than the one its host declared. H-R4 (an adapter replaces a bridge it did
// not write) is untouched: it governs hosts whose channel IS the bridge.
// searchTraceFailingOpen is searchTrace, with a panic anywhere in it, from
// the scrubber or anything else that builds, scrubs or caps the envelope,
// costing the trace and never the search (hard invariant 1, #48): the
// search goes with no trace, as under JEVLIN_TRACE=off. Every step returns
// and checks its errors; a panic is what nothing checked, and the scrubber
// is a large and growing set of rules over text the model writes. It is
// said on stderr only, as a foreign bridge is: the machine envelope is what
// the model reads. The panic's value is not repeated, because it can quote
// the text being scrubbed.
func searchTraceFailingOpen(ops searchOps, m config.Search, stateDir string, getenv func(string) string, stderr io.Writer) (env *traceEnvelope, foreignBridge bool) {
	defer func() {
		if recover() != nil {
			env, foreignBridge = nil, false
			fmt.Fprintln(stderr, "jevlin search: sending this search without a trace: preparing the trace failed")
		}
	}()
	return searchTrace(ops, m, stateDir, getenv)
}

// traceOff: JEVLIN_TRACE switches traces off entirely, read the same way
// by the search, the turn end and every hook that builds a trace, so no
// spelling the search accepts leaves a hook still keeping the session.
func traceOff(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("JEVLIN_TRACE"))) {
	case "off", "0", "false":
		return true
	}
	return false
}

func searchTrace(ops searchOps, m config.Search, stateDir string, getenv func(string) string) (env *traceEnvelope, foreignBridge bool) {
	if traceOff(getenv) {
		return nil, false
	}
	harness := getenv("JEVLIN_HARNESS")
	lineageDeclared := getenv(lineageEnv) != ""

	if bridge := getenv(bridgeEnv); bridge != "" {
		if lineageDeclared {
			foreignBridge = true
		} else if decoded := decodeTraceBridge(bridge); decoded != nil {
			if harness != "" {
				decoded.Harness = harness
			}
			return capTrace(decoded), false
		}
	}

	now := ops.now()
	var (
		lf   *lineageFile
		path string
	)
	// session is the hashed id of the session this shell was started in,
	// when the host exported one. It is the test of ownership for the file
	// the host declared, exactly as it is for the walk.
	//
	// The declared path says WHERE a session's lineage lives, not WHOSE it
	// currently holds. Cursor keys the file by workspace alone, and
	// every hook event writes the current conversation's id into it, so two
	// conversations open on one workspace — two chat tabs on a project —
	// share one file and take turns owning it. Both shells name that same
	// path. Adopting it by path alone sent one conversation's search to the
	// router under the other's id, with the other's text, advancing a counter
	// both share. A file holding another session is therefore not adopted
	// and not advanced; the search goes on to the walk, which refuses it for
	// the same reason, and ends at its own per-shell identity under the
	// declared harness. With no session exported the declared file is
	// believed as before: every shell started before the variable existed.
	// Cursor now keys the file by conversation as well, so a file of
	// its own is what a Cursor search declares; this guard stays for every
	// file a session did not name for itself.
	session := getenv(sessionEnv)
	if p := getenv(lineageEnv); p != "" {
		if l, ok := loadLineage(ops.hook, p); ok && now.Sub(l.UpdatedAt) <= lineageMaxAge && (session == "" || l.SessionID == session) {
			lf, path = l, p
		}
	}
	if lf == nil && m.SessionsDir != "" {
		if cwd, err := ops.getwd(); err == nil {
			// harness is what says whose search this is. Without it the walk
			// answers nothing, because anything it found up the tree would be
			// another session's.
			lf, path = lineageForCwd(ops.hook, m.SessionsDir, cwd, harness, session, now)
		}
	}
	if lf != nil {
		lf.Seq++
		env := lf.envelope()
		if env != nil {
			if harness != "" {
				env.Harness = harness
			}
			_ = saveLineage(ops.hook, path, lf, now)
			return capTrace(env), foreignBridge
		}
	}

	// No hook anywhere: the parent shell stands in for the session. One
	// agent session keeps one shell, so its pid is stable across calls.
	// Host and pid are guessable, so they are keyed with the installation's
	// trace key, never hashed bare: a plain hash of them could be enumerated
	// back to the hostname. With no key the search gets a one-off id.
	sessionID := ""
	if ops.traceKey != nil {
		if key, err := ops.traceKey(stateDir); err == nil {
			host, _ := ops.hostname()
			sessionID = traceKeyedHash(key, host+"|"+strconv.Itoa(ops.getppid()))
		}
	}
	if sessionID == "" {
		sessionID = traceRandomID()
	}
	return capTrace(&traceEnvelope{
		V:         traceVersion,
		Harness:   orString(harness, "cli"),
		SessionID: sessionID,
		CallID:    traceRandomID(),
	}), foreignBridge
}

// ── the router's answer, as much of it as this client reads ─────────────

type routerResponse struct {
	RequestID  string            `json:"request_id"`
	Query      string            `json:"query"`
	Chosen     int               `json:"chosen"`
	Candidates []routerCandidate `json:"candidates"`
	// Merged is the router's merged view, which every --stdin search asks
	// for. A pointer, so a 200 with no "merged" key — the router breaking
	// its contract — is told apart from an empty merge, "merged": [].
	Merged *[]routerMergedPage `json:"merged,omitempty"`
	// Coverage, MergedLimit, MergedTotal and ExcerptsTrimmed come with the
	// merged view from Jevlin-core #55 on; a router before it sends none of
	// them, and each decodes to absent.
	Coverage        *routerCoverage `json:"coverage,omitempty"`
	MergedLimit     *int            `json:"merged_limit,omitempty"`
	MergedTotal     *int            `json:"merged_total,omitempty"`
	ExcerptsTrimmed bool            `json:"excerpts_trimmed,omitempty"`
	Session         *struct {
		ID string `json:"id"`
	} `json:"session,omitempty"`
	// Decision and Usage are pointers, not values, so a router response
	// that omits them decodes to nil rather than to a zeroed struct this
	// client cannot tell apart from a genuine zero — the difference
	// between "the router said nothing about this" and "the router said
	// zero", which the machine envelope must preserve (omitempty on the
	// pointer, never on a value that would print as {} or 0).
	Decision *routerDecision `json:"decision,omitempty"`
	Usage    *routerUsage    `json:"usage,omitempty"`
	// Indexes is the router's per-provider account of the search, sent
	// with the merged view: which arms answered, which failed or timed out
	// or were skipped, and how many results each gave. A pointer, like
	// Decision and Usage, so a router that omits it is told apart from one
	// that sent an empty list.
	Indexes *routerIndexes `json:"indexes,omitempty"`
	// FiltersApplied is the router's account of the request's recency and
	// domain_filter (Jevlin-core #68): which providers each was sent to and
	// how many pages the router itself dropped for falling outside it. A
	// router before #68 sends none, and then nothing says the results were
	// filtered.
	FiltersApplied *routerFiltersApplied `json:"filters_applied,omitempty"`
}

// routerFiltersApplied carries one key per filter the request carried, and
// only those: a request with recency alone gets no domain_filter entry.
type routerFiltersApplied struct {
	Recency      *routerRecencyFilter `json:"recency,omitempty"`
	DomainFilter *routerDomainFilter  `json:"domain_filter,omitempty"`
}

// routerRecencyFilter and routerDomainFilter keep the router's lists as
// pointers, so "not_sent": [] stays an empty list and a list the router
// left out stays out: the envelope repeats them field for field. Dropped is
// the pages the router removed for falling outside the filter, on top of
// whatever the providers that took it left out themselves.
type routerRecencyFilter struct {
	Requested      string    `json:"requested,omitempty"`
	PublishedAfter string    `json:"published_after,omitempty"`
	SentTo         *[]string `json:"sent_to,omitempty"`
	NotSent        *[]string `json:"not_sent,omitempty"`
	Dropped        *int      `json:"dropped,omitempty"`
}

type routerDomainFilter struct {
	Requested []string  `json:"requested,omitempty"`
	SentTo    *[]string `json:"sent_to,omitempty"`
	NotSent   *[]string `json:"not_sent,omitempty"`
	Dropped   *int      `json:"dropped,omitempty"`
}

// routerIndexes is the router's "indexes" block. Only by_provider is read:
// selected, ok and pending are counts over that same list.
type routerIndexes struct {
	ByProvider []routerIndex `json:"by_provider"`
}

// routerIndex is one provider's line in indexes.by_provider. Results and
// LatencyMS are pointers because the router sends them only for an arm that
// answered, and an arm that answered with no results says 0: absent and zero
// are different answers.
type routerIndex struct {
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	Results   *int   `json:"results,omitempty"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
}

// routerDecision is the router's own account of which arms it ran.
type routerDecision struct {
	Tier      string   `json:"tier,omitempty"`
	Providers []string `json:"providers,omitempty"`
	// Trimmed lists providers the router dropped because the cost
	// estimate exceeded the tier's ceiling, per the router's own contract
	// (SKILL.md's "trimmed rather than the request refused"). Absent when
	// nothing was trimmed.
	Trimmed []string `json:"trimmed,omitempty"`
	// Build names the router build that answered (Jevlin-core #68): its
	// version and a hash of its configuration, so two answers can be told
	// apart by what produced them.
	Build *routerBuild `json:"build,omitempty"`
}

type routerBuild struct {
	Version    string `json:"version,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
}

// routerUsage is the router's ledger for the whole search: what it cost,
// whether the answer came from cache, and whether slow-lane arms are
// still landing.
type routerUsage struct {
	CostMicros int64 `json:"cost_micros,omitempty"`
	CacheHit   bool  `json:"cache_hit,omitempty"`
	// CacheRefresh says the request asked for a live search (cache
	// "refresh") and got one.
	CacheRefresh bool  `json:"cache_refresh,omitempty"`
	Pending      int   `json:"pending,omitempty"`
	LatencyMS    int64 `json:"latency_ms,omitempty"`
	// DeadlineHit says the router answered at its deadline with arms still
	// out, so what came back is less than the search would have found.
	DeadlineHit bool `json:"deadline_hit,omitempty"`
}

type routerCandidate struct {
	Provider   string           `json:"provider"`
	Kind       string           `json:"kind"`
	Status     string           `json:"status"`
	Answer     string           `json:"answer,omitempty"`
	Error      string           `json:"error,omitempty"`
	Citations  []routerCitation `json:"citations,omitempty"`
	CostMicros int64            `json:"cost_micros,omitempty"`
	// CostSource is "reported" when it is the provider's own number, and
	// something else (typically "modeled") otherwise. Carried through
	// unread by this client's own logic — a participant reading -format
	// json is the consumer, not a branch here.
	CostSource string         `json:"cost_source,omitempty"`
	Latency    *routerLatency `json:"latency,omitempty"`
}

type routerLatency struct {
	TTFBMS  int64 `json:"ttfb_ms,omitempty"`
	TotalMS int64 `json:"total_ms,omitempty"`
}

// routerCitation's Rank is the citation's 1-based place in its
// candidate's list, after the router's normalisation; Form is the query
// form that found it, 0 (absent) for query and n for the n-th
// search_queries entry. Since Jevlin-core #55 ranks restart for each form,
// so the two together name one citation, which is how mergedOf finds a
// merged page's position.
type routerCitation struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Rank    int    `json:"rank,omitempty"`
	Form    int    `json:"form,omitempty"`
}

// routerMergedPage is one page of the router's merged view. Every field
// Jevlin-core #55 adds — canonical_url, groups, exclusive, votes,
// matched_terms and a found_by entry's form — decodes as optional, so the
// same binary reads the view before and after it. Published is the page's
// date as the first provider that gave one gave it (RFC 3339, absent or
// null when none did); ResultID is the router's stable name for the page
// within this search's request_id. SourceID is its name across searches:
// the same page found by two searches has the same one, and it is what a
// fetch names a page by.
type routerMergedPage struct {
	URL          string          `json:"url"`
	ResultID     string          `json:"result_id,omitempty"`
	SourceID     string          `json:"source_id,omitempty"`
	CanonicalURL string          `json:"canonical_url,omitempty"`
	Title        string          `json:"title,omitempty"`
	Published    string          `json:"published,omitempty"`
	Excerpt      string          `json:"excerpt,omitempty"`
	Excerpts     []string        `json:"excerpts,omitempty"`
	BestRank     int             `json:"best_rank"`
	FoundBy      []routerFoundBy `json:"found_by"`
	Groups       int             `json:"groups,omitempty"`
	Exclusive    bool            `json:"exclusive,omitempty"`
	Votes        int             `json:"votes,omitempty"`
	MatchedTerms []string        `json:"matched_terms,omitempty"`
}

// routerFoundBy is one listing of a merged page: the provider, its 1-based
// rank for the page, and the query form it was listed under.
type routerFoundBy struct {
	Provider string `json:"provider"`
	Rank     int    `json:"rank"`
	Form     int    `json:"form,omitempty"`
}

// routerCoverage is the router's account of its whole merge. Its fields are
// machineCoverage's, field for field, so machineResultOf converts rather
// than copies: a field either gains stops the build until the other has it.
type routerCoverage struct {
	Pages             int            `json:"pages"`
	Exclusive         int            `json:"exclusive"`
	Corroborated      int            `json:"corroborated"`
	Groups            int            `json:"groups"`
	ByGroup           map[string]int `json:"by_group,omitempty"`
	ExclusiveByGroup  map[string]int `json:"exclusive_by_group,omitempty"`
	ByForm            []int          `json:"by_form,omitempty"`
	ReformulationOnly int            `json:"reformulation_only,omitempty"`
}

func (r routerResponse) chosen() *routerCandidate {
	if r.Chosen < 0 || r.Chosen >= len(r.Candidates) {
		return nil
	}
	return &r.Candidates[r.Chosen]
}
