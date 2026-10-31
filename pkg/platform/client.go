// Package platform is the client for the search platform's public
// control plane (agent onboarding design, search-platform-agent-
// onboarding-design.md §5.1-§5.2): register and poll. It is a separate
// package from pkg/auth on purpose — two contracts, two owners. pkg/auth
// keeps the records this client holds; this implements search-router's
// own contract, this design doc. Two hosts, not one — live testing found
// agents-v1.jevlin.ai serves register/status while platform.jevlin.ai is
// the human portal a claim_url/console_url points at; see Client's own
// doc comment. Of the status body, this client reads the fields
// AgentStatus names and no others.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/internal/netdial"
	"github.com/jevlinai/jevlin-go/internal/termtext"
	"github.com/jevlinai/jevlin-go/pkg/auth"
)

const (
	clientTimeout = 30 * time.Second
	maxBodyBytes  = 1 << 20

	// minPollInterval floors whatever a register response advertises in
	// poll.interval_s. A misbehaving or malicious control plane
	// advertising 0 — or a stub that forgot to set it — must not turn
	// connect's poll into a tight loop against a real service.
	minPollInterval = 2 * time.Second
	// maxPollInterval ceilings the same value (WP2-adversarial-review
	// finding 11): connect's own foreground budget is a few minutes, and
	// a detached resume runs after every search regardless — an
	// advertised interval_s in the hours (or a bogus 100000) must not
	// park a poll for a day. The caller's own remaining budget still
	// bounds the actual sleep further; this is the platform-facing half
	// of that bound.
	maxPollInterval = 60 * time.Second
)

// ErrAgentNotFound is Status's answer to a 404: an unknown agent, or a
// key that does not belong to it — the design deliberately gives both
// the same answer (§5.2), one no oracle.
var ErrAgentNotFound = errors.New("platform: agent not found")

// platformDialer is exactly the *net.Dialer http.DefaultTransport itself
// uses, named here rather than left inside a closure so a test can read
// its Timeout/KeepAlive directly.
var platformDialer = &net.Dialer{Timeout: netdial.DefaultTimeout, KeepAlive: netdial.DefaultKeepAlive}

// platformTransport is a clone of http.DefaultTransport — the search
// platform is reached over the participant's ordinary network path, the
// same as a browser visiting the claim URL would be, and every field
// DefaultTransport itself tunes (ForceAttemptHTTP2, TLSHandshakeTimeout,
// IdleConnTimeout, MaxIdleConns, ExpectContinueTimeout, its own Proxy)
// carries over unchanged from what leaving Transport nil gave every
// request before this — only DialContext is replaced, by
// netdial.For(platformDialer). Package-level and constructed once: every
// Client this package builds shares one connection pool.
var platformTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = netdial.For(platformDialer)
	return t
}()

// newPlatformClient is the only kind of http.Client this package may
// construct, mirroring pkg/auth/credentialclient.go's
// newCredentialClient in shape: bounded, same-origin redirects via
// auth.SameOriginRedirects (exported by that package precisely for
// reuse like this — see its own doc comment) because Status, Me and
// ClaimCode carry the participant's freshly-minted sr- key in
// Authorization, even though Register itself carries no credential yet.
// TestNoBareHTTPClientInThisPackage enforces this module-wide, the same
// sweep cmd/jevlin/boundary_test.go runs.
func newPlatformClient() *http.Client {
	return &http.Client{
		Timeout:       clientTimeout,
		CheckRedirect: auth.SameOriginRedirects,
		Transport:     platformTransport,
	}
}

// Client talks to two search-platform origins (config.Platform):
// apiBaseURL for the actual register/status requests
// (AgentsAPIURL — search-router's "Agents" host), and portalBaseURL
// solely to validate a returned claim_url's origin (BaseURL —
// search-router's "Platform" host, the human portal and claim pages).
// These were originally assumed to be one shared origin; live testing
// found the real deployment splits them, confirmed against
// search-router's own skill file.
type Client struct {
	apiBaseURL    string
	portalBaseURL string
	http          *http.Client
}

// New builds a Client. Both URLs are trusted as already validated
// (https-or-loopback, invariant 5) by pkg/config; this package does not
// re-validate them.
func New(apiBaseURL, portalBaseURL string) *Client {
	return &Client{
		apiBaseURL:    strings.TrimRight(apiBaseURL, "/"),
		portalBaseURL: strings.TrimRight(portalBaseURL, "/"),
		http:          newPlatformClient(),
	}
}

// Registration is what Register returns (§5.1), flattened and with the
// poll interval already floor-clamped.
type Registration struct {
	AgentID        string
	Key            string
	ClaimURL       string
	ClaimCode      string
	ClaimExpiresAt string
	PollInterval   time.Duration
	Tier           string
}

type registerWireResponse struct {
	AgentID        string `json:"agent_id"`
	Key            string `json:"key"`
	ClaimURL       string `json:"claim_url"`
	ClaimCode      string `json:"claim_code"`
	ClaimExpiresAt string `json:"claim_expires_at"`
	Poll           struct {
		URL       string `json:"url"`
		IntervalS int    `json:"interval_s"`
	} `json:"poll"`
	Tier string `json:"tier"`
}

// Register calls POST /v1/agents/register. name, client and
// requestedScopes are all optional; per §5.1, "a client that has nothing
// to say omits the fields rather than inventing values" — so each is
// left out of the request body entirely when empty, not sent as "" / [].
// client names the tool build speaking ("my-tool/1.0" in the router's
// skill file, bounded there at 128 characters; the caller sends its
// User-Agent spelling). requestedScopes is a hint only (the claim page's
// pre-tick); it grants nothing on its own.
func (c *Client) Register(ctx context.Context, name, client string, requestedScopes []string) (*Registration, error) {
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	if client != "" {
		body["client"] = client
	}
	if len(requestedScopes) > 0 {
		body["requested_scopes"] = requestedScopes
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("platform: encode register request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/v1/agents/register", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("platform: build register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("platform: register request failed: %w", err)
	}
	defer drainAndClose(resp.Body)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("platform: read register response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, refusal(resp.StatusCode, data)
	}
	var wire registerWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("platform: parse register response: %w", err)
	}
	if wire.AgentID == "" || wire.Key == "" || wire.ClaimURL == "" {
		return nil, errors.New("platform: register response missing agent_id, key or claim_url")
	}
	// Refused here, before connect journals the response: a field the
	// client would not save later wedges the journal it has already
	// written, and the key in it is published first.
	if err := auth.ValidAgentID(wire.AgentID); err != nil {
		return nil, fmt.Errorf("platform: register response: %w; refusing", err)
	}
	// The key goes into credentials.json, which readCredentials refuses
	// when it is blank, and into an Authorization header: a key that is
	// whitespace, or holds a blank or a control character anywhere, is not
	// one this client could publish, and journaling it first wedged the
	// journal on a publication that could never verify.
	if strings.TrimSpace(wire.Key) == "" || strings.ContainsAny(wire.Key, " \t") || hasControlChar(wire.Key) {
		return nil, errors.New("platform: register response's key is blank or holds a blank or a control character; refusing")
	}
	if err := validatePlatformURL(wire.ClaimURL, c.portalBaseURL); err != nil {
		return nil, err
	}
	if err := auth.ValidClaimCode(wire.ClaimCode); err != nil {
		return nil, fmt.Errorf("platform: claim_code: %w; refusing", err)
	}
	if err := auth.ValidTimestamp(wire.ClaimExpiresAt); err != nil {
		return nil, fmt.Errorf("platform: register response: claim_expires_at: %w; refusing", err)
	}
	return &Registration{
		AgentID:        wire.AgentID,
		Key:            wire.Key,
		ClaimURL:       wire.ClaimURL,
		ClaimCode:      wire.ClaimCode,
		ClaimExpiresAt: wire.ClaimExpiresAt,
		Tier:           wire.Tier,
		PollInterval:   clampPollInterval(time.Duration(wire.Poll.IntervalS) * time.Second),
	}, nil
}

func clampPollInterval(d time.Duration) time.Duration {
	if d < minPollInterval {
		return minPollInterval
	}
	if d > maxPollInterval {
		return maxPollInterval
	}
	return d
}

// hasControlChar is internal/termtext's rule: any control, format or line
// separator character. None legitimately appears in an agent id, a claim
// URL, a claim code, a claim time, a scope or a refusal message.
// A REFUSAL, not a strip, because a platform response containing one is
// not a shape this client trusts enough to guess what was meant
// (WP2-adversarial-review finding 12). pkg/auth reads its records back
// through the same rule, which is why the rule is not kept here.
func hasControlChar(s string) bool { return termtext.HasControlChar(s) }

// isLoopbackHostname mirrors pkg/config's and pkg/auth's own
// isLoopbackHost — each package that validates a URL's host keeps this
// tiny check locally rather than importing another package for four
// lines (the existing house convention: see pkg/auth/discovery.go and
// pkg/config/config.go's own copies).
func isLoopbackHostname(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validatePlatformURL enforces invariant 12 on any platform-supplied,
// origin-locked URL that gets printed to a terminal or persisted to disk
// verbatim — register's claim_url, and poll's console_url below. A
// control character in either could forge the client's own output, and
// an off-origin URL could point a participant at a page that is not
// actually this platform's. Absolute HTTPS (or loopback, matching this
// codebase's http(s)-or-loopback convention elsewhere), origin exactly
// equal to baseURL's own; nothing widens it.
//
// The origin is not the whole of what is printed. url.Parse accepts a space,
// U+3000 or any other non-ASCII rune in a path, so "<origin>/claim/X
// https://evil.example/claim" has the right origin, and a terminal that
// turns URLs into links makes the second one clickable on the line the
// participant is told to open. So every byte must be printable ASCII
// without the space, the URL must name no user (a "user@" before the host
// is shown first and opens the platform anyway), and it must re-serialize
// to exactly the text given: what is checked is what is printed.
func validatePlatformURL(raw, baseURL string) error {
	if hasControlChar(raw) {
		return errors.New("platform: claim_url contains a control character; refusing")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x21 || raw[i] > 0x7e {
			return errors.New("platform: claim_url holds a space or a character outside printable ASCII; refusing")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("platform: claim_url does not parse: %w", err)
	}
	if !u.IsAbs() {
		return errors.New("platform: claim_url is not an absolute URL")
	}
	if u.User != nil {
		return errors.New("platform: claim_url names a user before its host; refusing")
	}
	if u.String() != raw {
		return errors.New("platform: claim_url does not read back as the text it was given; refusing")
	}
	httpsOrLoopback := u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHostname(u.Hostname()))
	if !httpsOrLoopback {
		return fmt.Errorf("platform: claim_url scheme %q is not https (or http on loopback)", u.Scheme)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("platform: configured base URL does not parse: %w", err)
	}
	if u.Scheme != base.Scheme || u.Host != base.Host {
		return fmt.Errorf("platform: claim_url origin %s://%s does not match the configured [platform] url origin %s://%s",
			u.Scheme, u.Host, base.Scheme, base.Host)
	}
	return nil
}

// ValidateStoredClaimURL applies validatePlatformURL to a claim_url read
// back from local state rather than straight off the wire. agent.json lives
// in a sandbox-writable state_dir, so the copy there is only as trustworthy
// as the last thing that wrote the file: it must pass the same check
// against the configured [platform] url before it is shown to anyone.
func ValidateStoredClaimURL(raw, baseURL string) error {
	return validatePlatformURL(raw, strings.TrimRight(baseURL, "/"))
}

// AgentStatus is what Status returns (§5.2), flattened. A field of the
// body it does not name is not read.
type AgentStatus struct {
	Status         string // "unclaimed" | "claimed" | "expired"
	Scopes         []string
	ClaimExpiresAt string
	ClaimedAt      string
	// ConsoleURL is the agent's project page on the platform, once
	// claimed — search-router added this specifically so a re-approval
	// message can send a human straight to the real console instead of
	// making them submit an already-consumed claim code first, just to
	// reach a "go to console" link from the resulting error page. Empty
	// when absent (an older platform, or an unclaimed agent, for which
	// there is no project yet) — callers fall back to the generic claim
	// address.
	ConsoleURL string
}

// Status calls GET /v1/agents/{id}, the client's poll. Authentication
// here is diagnostic (§5.2): the key must belong to the agent, but an
// expired-unclaimed key still authenticates so the client can learn
// "expired" and stop polling — only a revoked key, or an unknown
// agent/key pairing, answers 404.
func (c *Client) Status(ctx context.Context, agentID, key string) (*AgentStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/v1/agents/"+url.PathEscape(agentID), nil)
	if err != nil {
		return nil, fmt.Errorf("platform: build status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("platform: status request failed: %w", err)
	}
	defer drainAndClose(resp.Body)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("platform: read status response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAgentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refusal(resp.StatusCode, data)
	}
	out, err := decodeAgentStatusWire(data, c.portalBaseURL)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// agentStatusWire is the status-poll body's wire shape (§5.2): what
// Status decodes, and what Me's own response carries in addition to
// agent_id and the claim bootstrap fields. Factored out so the two
// decode identically — see decodeAgentStatusWire's own doc comment.
type agentStatusWire struct {
	Status         string   `json:"status"`
	Scopes         []string `json:"scopes"`
	ClaimExpiresAt string   `json:"claim_expires_at"`
	ClaimedAt      string   `json:"claimed_at"`
	ConsoleURL     string   `json:"console_url"`
}

// decodeAgentStatusWire normalizes a status-poll body (§5.2) into an
// AgentStatus: status enum validation, the scopes' shape and console_url's
// origin-lock all happen here once, so
// Status and Me — which answers with the same body plus agent_id/claim
// fields — cannot drift on how they interpret otherwise-identical bytes.
func decodeAgentStatusWire(data []byte, portalBaseURL string) (AgentStatus, error) {
	var wire agentStatusWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return AgentStatus{}, fmt.Errorf("platform: parse status response: %w", err)
	}
	if wire.Status == "" {
		return AgentStatus{}, errors.New("platform: status response carried no status")
	}
	// WP2-adversarial-review finding 13: an unrecognized status (a typo,
	// a future value this build predates, or a hostile response) must
	// not be trusted as anything more specific than "keep polling" — the
	// alternative, falling through the switch statements callers build
	// on this value, has previously let an unrecognized string like
	// "PENDING_REVIEW" reach a path meant for a known status. unclaimed is
	// the one status every caller already treats as "nothing to do yet,
	// try again later," which is the correct, safe default here.
	switch wire.Status {
	case "unclaimed", "claimed", "expired":
	default:
		wire.Status = "unclaimed"
		wire.Scopes = nil
	}
	// Every identifier here lands in agent.json, which status prints, so each
	// is held to the shape a record holds it to (auth.ValidScope and the
	// rest): a token, never a sentence, and never a control character.
	for _, sc := range wire.Scopes {
		if auth.ValidScope(sc) != nil {
			return AgentStatus{}, errors.New("platform: a scope in the status response is not an identifier; refusing")
		}
	}
	if auth.ValidTimestamp(wire.ClaimExpiresAt) != nil || auth.ValidTimestamp(wire.ClaimedAt) != nil {
		return AgentStatus{}, errors.New("platform: a claim time in the status response is not a timestamp; refusing")
	}
	// console_url gets the same origin-lock and control-character check
	// as register's claim_url (invariant 12) — but dropped, not failed,
	// when invalid. Unlike claim_url, which IS the point of a register
	// call (a registration with no trustworthy claim link is useless),
	// console_url is supplementary to an otherwise-good status poll: the
	// scopes this response also carries are still worth having even if a
	// hostile or buggy console_url arrived alongside them.
	consoleURL := wire.ConsoleURL
	if consoleURL != "" && validatePlatformURL(consoleURL, portalBaseURL) != nil {
		consoleURL = ""
	}
	return AgentStatus{
		Status:         wire.Status,
		Scopes:         wire.Scopes,
		ClaimExpiresAt: wire.ClaimExpiresAt,
		ClaimedAt:      wire.ClaimedAt,
		ConsoleURL:     consoleURL,
	}, nil
}

// AgentIdentity is what the key alone can recover: the agent's id and
// the same status body the poll returns, plus the claim bootstrap when
// the platform includes it for a still-unclaimed agent (B.1's known
// gap — /me does not carry these today, so ClaimURL/ClaimCode are only
// ever populated when the platform actually sends them).
type AgentIdentity struct {
	AgentID   string
	ClaimURL  string // present only while unclaimed, and only once the platform serves it
	ClaimCode string
	AgentStatus
}

// Me answers GET /v1/agents/me with the key alone (added by the router
// team so a lost or corrupt local registration can be rebuilt without a
// human re-approving, agent onboarding design rule 8 amendment; see
// AGENTS.md/PR-B doc for the design escalation this closes). ErrAgentNotFound
// on 404 (an unknown or revoked key, same no-oracle answer Status gives);
// RefusalError otherwise.
func (c *Client) Me(ctx context.Context, key string) (*AgentIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/v1/agents/me", nil)
	if err != nil {
		return nil, fmt.Errorf("platform: build self-lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("platform: self-lookup request failed: %w", err)
	}
	defer drainAndClose(resp.Body)
	// Deliberately not Status's io.LimitReader(maxBodyBytes): an oversized
	// 200 here is a read/protocol error (max+1 lets us tell "exactly at
	// the limit" from "truncated silently at the limit" apart), not a
	// bounded non-success HTTP answer, so it must not decode into a
	// RefusalError.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("platform: read self-lookup response: %w", err)
	}
	if len(data) > maxBodyBytes {
		return nil, errors.New("platform: self-lookup response exceeds the size limit; refusing")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAgentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refusal(resp.StatusCode, data)
	}
	status, err := decodeAgentStatusWire(data, c.portalBaseURL)
	if err != nil {
		return nil, err
	}
	var wire struct {
		AgentID   string `json:"agent_id"`
		ClaimURL  string `json:"claim_url"`
		ClaimCode string `json:"claim_code"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("platform: parse self-lookup response: %w", err)
	}
	if wire.AgentID == "" {
		return nil, errors.New("platform: self-lookup response carried no agent_id")
	}
	if err := auth.ValidAgentID(wire.AgentID); err != nil {
		return nil, fmt.Errorf("platform: self-lookup response: %w; refusing", err)
	}
	if err := auth.ValidClaimCode(wire.ClaimCode); err != nil {
		return nil, fmt.Errorf("platform: claim_code: %w; refusing", err)
	}
	if wire.ClaimURL != "" {
		if err := validatePlatformURL(wire.ClaimURL, c.portalBaseURL); err != nil {
			return nil, err
		}
	}
	return &AgentIdentity{
		AgentID:     wire.AgentID,
		ClaimURL:    wire.ClaimURL,
		ClaimCode:   wire.ClaimCode,
		AgentStatus: status,
	}, nil
}

// ClaimBootstrap is a fresh claim link for a still-unclaimed agent: what
// ClaimCode returns, and the three fields register's response carries for
// the same purpose.
type ClaimBootstrap struct {
	ClaimURL       string
	ClaimCode      string
	ClaimExpiresAt string
}

// ClaimCode calls POST /v1/agents/{agent_id}/claim-code with the key. The
// platform mints a FRESH claim_url/claim_code and the old one dies — the
// original code is never re-served (only its hash is stored), so this is
// the one way back to a working human link for an agent whose link was
// lost while still unclaimed (the router's skill file, "Lost the claim
// link too"; it closes B.1's gap, where /v1/agents/me answers without the
// claim bootstrap). The fresh code is short-lived (about an hour).
//
// Minting is a state change, not a read: the caller must want the old
// code dead. A claimed agent has nothing to claim and answers 404, the
// same no-oracle answer an unknown or revoked key gets — ErrAgentNotFound
// either way, deliberately indistinguishable. The claim_url gets the same
// origin-lock as register's (invariant 12): a registration link this
// client will print must point at the configured portal.
func (c *Client) ClaimCode(ctx context.Context, agentID, key string) (*ClaimBootstrap, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBaseURL+"/v1/agents/"+url.PathEscape(agentID)+"/claim-code", nil)
	if err != nil {
		return nil, fmt.Errorf("platform: build claim-code request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("platform: claim-code request failed: %w", err)
	}
	defer drainAndClose(resp.Body)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("platform: read claim-code response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAgentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refusal(resp.StatusCode, data)
	}
	var wire struct {
		ClaimURL       string `json:"claim_url"`
		ClaimCode      string `json:"claim_code"`
		ClaimExpiresAt string `json:"claim_expires_at"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("platform: parse claim-code response: %w", err)
	}
	if wire.ClaimURL == "" {
		return nil, errors.New("platform: claim-code response carried no claim_url")
	}
	if err := validatePlatformURL(wire.ClaimURL, c.portalBaseURL); err != nil {
		return nil, err
	}
	if err := auth.ValidClaimCode(wire.ClaimCode); err != nil {
		return nil, fmt.Errorf("platform: claim_code: %w; refusing", err)
	}
	if err := auth.ValidTimestamp(wire.ClaimExpiresAt); err != nil {
		return nil, fmt.Errorf("platform: claim-code response: claim_expires_at: %w; refusing", err)
	}
	return &ClaimBootstrap{
		ClaimURL:       wire.ClaimURL,
		ClaimCode:      wire.ClaimCode,
		ClaimExpiresAt: wire.ClaimExpiresAt,
	}, nil
}

// RefusalError is a structured refusal from the platform's public
// routes: a machine-readable code plus the message, so a caller can act
// on WHICH refusal this is rather than parsing prose. The envelope
// shape ({"error":{"code","message"}}) is the router's published skill
// file's (read 2026-10-02), which states the rule refusal() below already
// applies: the same code is mirrored top-level, and a client branches
// on the top-level code, never on the message.
type RefusalError struct {
	Status  int
	Code    string
	Message string
}

func (e *RefusalError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("platform: refused (%d %s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("platform: refused with status %d", e.Status)
}

// refusal classifies a bounded non-success HTTP answer. The router's
// error envelope carries a top-level code as well as the nested
// error.code this package originally expected; per B.2, the top-level
// one wins when both are present, and classification never reads the
// message (the message is prose for a human, not something to branch
// on).
func refusal(status int, raw []byte) error {
	var env struct {
		Code  string `json:"code"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		code := env.Code
		if code == "" {
			code = env.Error.Code
		}
		// Both are printed verbatim by Error(): a code carrying a control
		// character is no code at all, and such a message is dropped
		// (the code alone still classifies the refusal).
		if code != "" && !hasControlChar(code) {
			msg := env.Error.Message
			if hasControlChar(msg) {
				msg = ""
			}
			return &RefusalError{Status: status, Code: code, Message: msg}
		}
	}
	return &RefusalError{Status: status}
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}
