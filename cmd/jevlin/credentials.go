package main

// The key below the environment.
//
// `search` sends the participant's sr- key in Authorization; the router
// meters against it and that is what makes a search payable. Until now
// the key lived only in JEVLIN_API_KEY, which works for an agent
// started from a shell that exports it and fails for one launched from a
// Dock icon, a service, or a fresh terminal that never sourced the
// profile. `jevlin login` stores the key once, owner-only, beside
// the rest of the participant's files; `search` reads it when the
// environment has nothing.
//
// Resolution order, and why:
//
//	JEVLIN_API_KEY        the environment wins so a shell can override
//	                         the stored key for one session (a second
//	                         account, a CI runner) without touching disk
//	credentials.json         what login wrote: ~/.jevlin/credentials.json,
//	                         0600, refused if a symlink or readable by
//	                         anyone else — the same posture as the auth
//	                         store's refresh token
//
// The key reaches login on stdin or from a named environment variable,
// never as a flag: argv is visible to every process on the machine and
// lands in shell history. Before the file is written the key is checked
// against the router with a zero-spend probe — a search request with no
// query — so a mistyped key is caught now rather than on the first 401
// an agent sees.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jevlinai/jevlin-go/internal/netdial"
	"github.com/jevlinai/jevlin-go/internal/termtext"
	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const (
	credentialsFile         = "credentials.json"
	credentialsVersion      = 1
	registrationJournalFile = "registration_pending.json"
	claimRecordFile         = "claim.json"

	apiKeyEnv = "JEVLIN_API_KEY" // #nosec G101 -- an env var NAME, not a credential value

	// loginProbeTimeout bounds the verification round trip; a router that
	// does not answer in this long is reported, not waited on.
	loginProbeTimeout = 30 * time.Second
)

// loginProbeDialer is exactly the *net.Dialer http.DefaultTransport itself
// uses (net/http's own DefaultTransport literal), named here rather than
// left inside a closure so a test can read its Timeout/KeepAlive directly.
var loginProbeDialer = &net.Dialer{Timeout: netdial.DefaultTimeout, KeepAlive: netdial.DefaultKeepAlive}

// loginProbeTransport is a clone of http.DefaultTransport — its
// ForceAttemptHTTP2, TLSHandshakeTimeout, IdleConnTimeout, MaxIdleConns and
// ExpectContinueTimeout all carried over unchanged, since this probe
// legitimately behaves like any other client on the participant's network,
// the same as leaving Transport nil did before — with only DialContext
// replaced, by netdial.For(loginProbeDialer): the same dialer
// DefaultTransport's own DialContext already used, now named explicitly so
// a test can intercept it via netdial.Hook. Cloned once, at package init.
var loginProbeTransport = cloneDefaultTransport(loginProbeDialer)

// keySource names where a resolved key came from, for messages that must
// say which source to fix.
type keySource string

const (
	keyFromEnv  keySource = "JEVLIN_API_KEY"
	keyFromFile keySource = "credentials file"
	keyFromNone keySource = ""
)

// credentials is the on-disk shape. Router and AgentsAPI are where the key
// may be sent: the router a search, a limits read, a turn end and a fetch
// go to, and the platform's agents API that connect calls.
// Each is an origin (login has always written its router as the full URL it
// verified against, which reduces to the same origin), and keyService.check
// holds every request carrying the stored key to it.
type credentials struct {
	V         int    `json:"v"`
	APIKey    string `json:"api_key"`
	Router    string `json:"router,omitempty"`
	AgentsAPI string `json:"agents_api,omitempty"`
}

// ── where the stored key may go ─────────────────────────────────────────
//
// The key in credentials.json is sent only to the origin it is bound to,
// whichever config a command was given. Bound means the origin
// credentials.json records for that service; a file an older client wrote
// without one is bound to that service's origin in this installation's own
// config (installationConfigPath, read as a command naming no -config reads
// it), never to a config named on the command line. Origins compare by
// scheme, host and port. A foreground connect records both on the key it
// publishes and fills in a missing one (recordKeyBinding).

// keyService is one place the stored key is sent. configured is where cfg
// sends it, and whether cfg names that place: in configuration format 1
// every endpoint resolves ([router] url, JEVLIN_ROUTER_URL or the compiled
// default; the agents API likewise), so a loaded config always names both,
// and the unnamed branch below is kept only as the shape a future endpoint
// without a default would take.
type keyService struct {
	name       string
	recorded   func(*credentials) string
	configured func(*config.Config) (string, bool)
}

var (
	routerKeyService = keyService{
		name:     "router",
		recorded: func(c *credentials) string { return c.Router },
		configured: func(cfg *config.Config) (string, bool) {
			// Never nil in a loaded config: [router] url, JEVLIN_ROUTER_URL
			// or the compiled default, and nothing else standing in.
			if cfg.Search.RouterURL == nil {
				return "", false
			}
			return cfg.Search.RouterURL.String(), true
		},
	}
	agentsAPIKeyService = keyService{
		name:     "agents API",
		recorded: func(c *credentials) string { return c.AgentsAPI },
		configured: func(cfg *config.Config) (string, bool) {
			return cfg.Platform.AgentsAPIURL, cfg.Platform.AgentsAPIURL != ""
		},
	}
)

// keyOriginError is a request that would carry the stored key to an origin
// other than the one it is bound to. It names the origins and nothing
// else: no key, no path. unnamed is a binding nothing chose: credentials.json
// records no origin and the installation's config names none, so the origin
// it falls to is a built-in default, which the message does not present as
// the key's.
type keyOriginError struct {
	service   string
	bound     string
	requested string
	unnamed   bool
}

func (e *keyOriginError) Error() string {
	if e.unnamed {
		return fmt.Sprintf("the stored key records no %s, and this installation's own config names none, so it is not sent to %s. "+
			"Run jevlin connect with JEVLIN_CONFIG set to the config this installation uses, which records it", e.service, e.requested)
	}
	return fmt.Sprintf("the stored key belongs to the %s at %s, and this config names %s; it is not sent there. "+
		"Use the config this installation was set up with", e.service, e.bound, e.requested)
}

// keyOriginCode is the machine code for a keyOriginError, under fix_input:
// the same request with the installation's own config succeeds.
const keyOriginCode = "key_origin_mismatch"

// urlOrigin is scheme://host[:port] in lower case, with the scheme's
// default port left out, so https://h and https://h:443 are one origin.
// The host must be plain ASCII: a host written in Unicode is dialed as its
// IDNA form, a different name, and Go's case folding maps some letters
// (U+0130) onto ASCII ones, so a lower-cased Unicode host could read as
// the bound origin while the connection goes to the punycode name. The
// config holds the same rule (config.ParseEndpoint); here it holds for what
// credentials.json records too.
func urlOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("not an absolute http(s) URL")
	}
	for _, c := range []byte(u.Hostname()) {
		if c < 0x21 || c > 0x7e {
			return "", errors.New("not a URL with a plain ASCII host")
		}
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		return scheme + "://[" + host + "]", nil
	}
	return scheme + "://" + host, nil
}

// boundOrigin is the origin the stored key belongs to for this service, and
// whether that origin may be written into credentials.json: one recorded
// already, or one the installation's config names itself.
func (s keyService) boundOrigin(c *credentials, getenv func(string) string) (origin string, named bool, err error) {
	if r := s.recorded(c); r != "" {
		o, err := urlOrigin(r)
		if err != nil {
			return "", false, fmt.Errorf("credentials: the %s it records is %v — re-run: jevlin connect", s.name, err)
		}
		return o, true, nil
	}
	// The installation's own config file decides, never the environment:
	// the endpoint variables apply to a run, and a hook inherits whatever
	// its host's environment set, so an origin taken from them would bind
	// (and recordKeyBinding would persist) a key to a place nothing in
	// this installation chose.
	cfg, _, err := loadConfig("", withoutEndpointVariables(getenv))
	if err != nil {
		return "", false, fmt.Errorf("credentials: the stored key records no %s, and this installation's own config cannot be read to name one: %w", s.name, err)
	}
	raw, named := s.configured(cfg)
	o, err := urlOrigin(raw)
	if err != nil {
		return "", false, fmt.Errorf("credentials: the stored key records no %s, and this installation's own config names none", s.name)
	}
	return o, named, nil
}

// withoutEndpointVariables is getenv with the runtime endpoint variables
// unset, for reading the installation's own config as the file alone says
// it (boundOrigin, recordKeyBinding).
func withoutEndpointVariables(getenv func(string) string) func(string) string {
	return func(name string) string {
		switch name {
		case config.EnvRouterURL, config.EnvPlatformURL, config.EnvAgentsAPIURL:
			return ""
		}
		return getenv(name)
	}
}

// check refuses a request to dest that would carry the stored key c
// anywhere but its bound origin.
func (s keyService) check(c *credentials, dest string, getenv func(string) string) error {
	bound, named, err := s.boundOrigin(c, getenv)
	if err != nil {
		return err
	}
	requested, err := urlOrigin(dest)
	if err != nil {
		return fmt.Errorf("%s: %v", s.name, err)
	}
	if requested != bound {
		return &keyOriginError{service: s.name, bound: bound, requested: requested, unnamed: !named}
	}
	return nil
}

// storedKeyRefusedFor is check against the key on file, for a command that
// refuses before it does anything else: nil when there is no readable key
// (the command's own key read reports that), or when it may go to dest.
func storedKeyRefusedFor(s keyService, m config.Search, dest string, getenv func(string) string) error {
	creds, err := readCredentials(credentialsPath(m))
	if err != nil {
		return nil
	}
	var origin *keyOriginError
	if err := s.check(creds, dest, getenv); errors.As(err, &origin) {
		return err
	}
	return nil
}

// recordKeyBinding fills in an origin credentials.json does not record yet
// with the one the key is already bound to, when the installation's config
// names it, so a file an older client wrote stops depending on that config.
// It never changes a recorded origin and never widens where the key may go.
// Only a run whose config is the installation's own (cfgSource is
// installationConfigPath) writes anything: a recorded origin never changes,
// so one taken from the installation's config while the participant runs
// with another would hold that other config's router refused for good.
// Best effort: the binding holds the same either way.
func recordKeyBinding(m config.Search, cfgSource string, getenv func(string) string) error {
	if own := installationConfigPath(getenv); cfgSource == "" || own == "" || !sameConfigPath(cfgSource, own) {
		return nil
	}
	path := credentialsPath(m)
	creds, err := readCredentials(path)
	if err != nil {
		return err
	}
	changed := false
	for _, f := range []struct {
		s     keyService
		field *string
	}{{routerKeyService, &creds.Router}, {agentsAPIKeyService, &creds.AgentsAPI}} {
		if *f.field != "" {
			continue
		}
		if o, named, err := f.s.boundOrigin(creds, getenv); err == nil && named {
			*f.field, changed = o, true
		}
	}
	if !changed {
		return nil
	}
	return writeCredentials(path, *creds)
}

// keyBindingOf is the binding a key minted for cfg's platform is published
// with: cfg's router and agents API, as configuration format 1 resolves
// them (the file, the endpoint variables, or the compiled defaults). An
// origin cfg does not name would be left unrecorded, and so fall to the
// installation's config; format 1 names both.
func keyBindingOf(cfg *config.Config) credentials {
	var c credentials
	for _, f := range []struct {
		s     keyService
		field *string
	}{{routerKeyService, &c.Router}, {agentsAPIKeyService, &c.AgentsAPI}} {
		raw, named := f.s.configured(cfg)
		if o, err := urlOrigin(raw); err == nil && named {
			*f.field = o
		}
	}
	return c
}

// envKeyRefusedFor is the rule for the environment's key: a search, a limits
// read or a turn end whose key comes from JEVLIN_API_KEY, given an explicit
// -config that is not this installation's own config file
// (installationConfigPath), is refused when that config's router origin
// differs from the router origin of the installation's own config. The
// installation's own config, named or not, is served as before, and so is a
// differing file that names the same router. A router chosen on purpose is
// served by making its config the installation's, with JEVLIN_CONFIG.
func envKeyRefusedFor(cfgPath string, cfg *config.Config, getenv func(string) string) error {
	if cfgPath == "" || unwrittenDefaultConfig(cfgPath, getenv) {
		return nil
	}
	if own := installationConfigPath(getenv); own != "" && sameConfigPath(cfgPath, own) {
		return nil
	}
	raw, _ := routerKeyService.configured(cfg)
	requested, err := urlOrigin(raw)
	if err != nil {
		return err
	}
	bound, named := "", false
	if own, _, err := loadConfig("", getenv); err == nil {
		ownRaw, ownNamed := routerKeyService.configured(own)
		if o, err := urlOrigin(ownRaw); err == nil {
			bound, named = o, ownNamed
		}
	}
	if requested == bound {
		return nil
	}
	return &envKeyOriginError{bound: bound, requested: requested, unnamed: !named}
}

// keyRefusedForOrigin: is err a refusal to send a key to the origin asked
// for, either key's? Classified by type, never by message (invariant 15).
func keyRefusedForOrigin(err error) bool {
	var stored *keyOriginError
	var env *envKeyOriginError
	return errors.As(err, &stored) || errors.As(err, &env)
}

// envKeyOriginError is envKeyRefusedFor's refusal, worded for the key's
// source: the environment, not the file. unnamed is keyOriginError's: the
// installation's config names no router, so no origin is named as its.
type envKeyOriginError struct {
	bound, requested string
	unnamed          bool
}

func (e *envKeyOriginError) Error() string {
	const choose = "To use this config's router, set JEVLIN_CONFIG to it, which makes it this installation's config"
	if e.unnamed {
		return fmt.Sprintf("%s is sent only to this installation's router, and this installation's own config names none, "+
			"so it is not sent to %s. %s", apiKeyEnv, e.requested, choose)
	}
	return fmt.Sprintf("%s is sent only to this installation's router (%s), and this config names %s; it is not sent there. %s",
		apiKeyEnv, e.bound, e.requested, choose)
}

// credentialsPath is the file in the installation's own directory, the
// parent of the state directory: with the standard layout that is
// ~/.jevlin/credentials.json.
func credentialsPath(m config.Search) string {
	return filepath.Join(installRoot(m), credentialsFile)
}

// registrationJournal is registration_pending.json beside credentials.json,
// outside the state directory a sandboxed agent may write.
func registrationJournal(m config.Search) (*auth.RegistrationJournal, error) {
	return auth.OpenRegistrationJournal(installRoot(m))
}

// claimRecord is claim.json beside credentials.json: the claim link and
// code (auth.ClaimRecord), written only from the platform's answer or the
// registration journal beside it, by a foreground run or by a resume
// finishing that journal; a resume in Codex's sandbox cannot write here.
func claimRecord(m config.Search) (*auth.ClaimRecord, error) {
	if m.StateDir == "" {
		// installRoot would be ".", the working directory: a config always
		// has a state directory unless no user config directory could be
		// found, and the record must not land wherever the command ran.
		return nil, errors.New("no search.state_dir, so no directory for the claim record")
	}
	return auth.OpenClaimRecord(installRoot(m))
}

// claimFor is the stored claim link for agentID, ok=false when none is on
// file for that agent. A record that cannot be read is reported on stderr
// (when given) and treated as absent: the caller's answer to a missing link
// is a re-mint or the no-link message, never another source.
func claimFor(m config.Search, agentID string, stderr io.Writer) (auth.ClaimBootstrap, bool) {
	record, err := claimRecord(m)
	if err != nil {
		return auth.ClaimBootstrap{}, false
	}
	b, ok, err := record.For(agentID)
	if err != nil {
		if stderr != nil {
			fmt.Fprintln(stderr, "jevlin: the claim record beside credentials.json could not be read:", err)
		}
		return auth.ClaimBootstrap{}, false
	}
	return b, ok
}

// saveClaim writes the claim link beside credentials.json.
func saveClaim(m config.Search, b auth.ClaimBootstrap) error {
	record, err := claimRecord(m)
	if err != nil {
		return err
	}
	return record.Save(b)
}

// platformKey uses only the credential connect stored for platform calls.
// The search-only environment override does not apply to the platform. The
// key is returned only when cfg's agents API is the one it is bound to;
// every caller sends it to cfg.Platform.AgentsAPIURL.
func platformKey(cfg *config.Config, getenv func(string) string) (string, error) {
	creds, err := readCredentials(credentialsPath(cfg.Search))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("no platform key on file (credentials.json); run `jevlin connect` first")
		}
		return "", err
	}
	if err := agentsAPIKeyService.check(creds, cfg.Platform.AgentsAPIURL, getenv); err != nil {
		return "", err
	}
	return creds.APIKey, nil
}

// looksLikePlatformKey is a shape check, not a verification — the
// platform is the only party that can actually confirm a key, and this
// exists solely so `connect` can refuse to silently overwrite a
// credentials.json that plainly holds something else (WP2-adversarial-
// review finding 14), not to validate a key this client did not itself
// mint.
func looksLikePlatformKey(key string) bool {
	return strings.HasPrefix(key, "sr-") && len(key) > 3
}

// resolveAPIKey applies the order documented above. A credentials file
// that exists but cannot be trusted (symlink, readable by others,
// unparseable) preserves its error rather than selecting another source.
// The stored key is returned only when m's router is the one it is bound to
// (a *keyOriginError otherwise); every caller sends it to m.RouterURL.
func resolveAPIKey(getenv func(string) string, m config.Search) (string, keySource, error) {
	if k := getenv(apiKeyEnv); k != "" {
		return k, keyFromEnv, nil
	}
	creds, err := readCredentials(credentialsPath(m))
	switch {
	case err == nil:
		if m.RouterURL != nil {
			if err := routerKeyService.check(creds, m.RouterURL.String(), getenv); err != nil {
				return "", keyFromNone, err
			}
		}
		return creds.APIKey, keyFromFile, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", keyFromNone, err
	}
	return "", keyFromNone, nil
}

// readCredentials reads and validates the file. Mode checks are gated by
// posixModes: Go reports 0777 for every file on Windows, where the
// directory ACL is the guard.
// credentialsMaxBytes bounds credentials.json, a version and a key.
const credentialsMaxBytes = 64 << 10

// credentialsCheckedHook runs between readCredentials' checks on the name and
// its read, when a test sets it: the seam a test uses to replace the file at
// exactly that moment. Nil in production.
var credentialsCheckedHook func(path string)

func readCredentials(path string) (*credentials, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("credentials: %s is a symlink; refusing", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credentials: %s is not a regular file; refusing", path)
	}
	if posixModes && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials: %s is readable by others (%04o); refusing — chmod 600 it, or re-run: jevlin login",
			path, info.Mode().Perm())
	}
	if credentialsCheckedHook != nil {
		credentialsCheckedHook(path)
	}
	// The checks above name the file; it is read by one open that refuses a
	// link and does not wait on a FIFO, and the mode is checked again on
	// what was opened, so the name cannot be replaced between them.
	data, held, err := fsx.ReadRegularNoFollow(path, credentialsMaxBytes)
	if err != nil {
		return nil, err
	}
	if posixModes && held.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("credentials: %s is readable by others (%04o); refusing — chmod 600 it, or re-run: jevlin login",
			path, held.Mode().Perm())
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("credentials: %s does not parse: %w — re-run: jevlin login", path, err)
	}
	if c.V != credentialsVersion || strings.TrimSpace(c.APIKey) == "" {
		return nil, fmt.Errorf("credentials: %s is not a v%d credentials file with a key — re-run: jevlin login", path, credentialsVersion)
	}
	// What the file says is printed by login -show, doctor and search's
	// human mode, so a string holding a character a terminal acts on
	// (internal/termtext: a control, a format or a line-separator
	// character) makes the file unreadable, and the message repeats none
	// of it.
	for _, field := range []string{c.APIKey, c.Router, c.AgentsAPI} {
		if termtext.HasControlChar(field) {
			return nil, fmt.Errorf("credentials: %s holds a character a terminal acts on; refusing — re-run: jevlin login", path)
		}
	}
	return &c, nil
}

// writeCredentials creates the file 0600 from the first byte: an O_EXCL
// temp file that never existed with wider bits, then a rename over the
// final name. A reader racing the write sees either the old file or the
// new one, never a partial or a briefly world-readable one.
func writeCredentials(path string, c credentials) error {
	c.V = credentialsVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ") // #nosec G117 -- this IS the credential's one legitimate persisted home: ~/.jevlin/credentials.json, written 0600 below
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := fmt.Sprintf("%s.%s.tmp", path, randomSuffix())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- our own state dir
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// maskKey shows enough to recognize a key and nothing that would let
// anyone use it: the prefix through the first dash and the last four.
func maskKey(k string) string {
	if len(k) <= 10 {
		return "…"
	}
	prefix := ""
	if i := strings.IndexByte(k, '-'); i >= 0 && i < 6 {
		prefix = k[:i+1]
	}
	return prefix + "…" + k[len(k)-4:]
}

// ── the probe ───────────────────────────────────────────────────────────

// probeOutcome is what the router said about a key without spending
// anything on it.
type probeOutcome int

const (
	probeAccepted    probeOutcome = iota // the key authenticated; the request failed for another reason, as intended
	probeRefused                         // 401/403: the router does not know this key
	probeUnavailable                     // 5xx or no answer: nothing learned
)

// probeKey posts to /v1/search with no query and one unknown field. A
// router that authenticates first answers 401 for a bad key and a 4xx
// schema error for a good one; nothing is served, so nothing is billed.
// A 2xx would mean the router served an empty search, which is still a
// verified key.
func probeKey(ctx context.Context, routerURL, key string) (probeOutcome, string, error) {
	endpoint := strings.TrimRight(routerURL, "/") + "/v1/search"
	payload, _ := json.Marshal(map[string]any{"jevlin_probe": buildVersion()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return probeUnavailable, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", clientIdentifier())
	req.Header.Set("Authorization", "Bearer "+key)
	// CheckRedirect: this probe exists to verify the key before it is ever
	// stored, and it carries that key in Authorization to do it — the same
	// reasoning as search.go's client, and it is the same key.
	resp, err := (&http.Client{Timeout: loginProbeTimeout, CheckRedirect: auth.SameOriginRedirects, Transport: loginProbeTransport}).Do(req)
	if err != nil {
		return probeUnavailable, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return probeRefused, resp.Status, nil
	case resp.StatusCode >= 500:
		return probeUnavailable, resp.Status, nil
	default:
		return probeAccepted, resp.Status, nil
	}
}

// ── the command ─────────────────────────────────────────────────────────

// cmdLogin stores, shows or forgets the key.
//
//	jevlin login                 read the key from stdin, verify, store
//	jevlin login -key-env VAR    read it from $VAR instead of stdin
//	jevlin login -show           where a search would get its key from
//	jevlin login -forget         remove the stored file
func cmdLogin(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := newFlagSet("login", stderr)
	cfgPath := flags.String("config", "", "path to TOML config file")
	keyEnv := flags.String("key-env", "", "read the key from this environment variable instead of stdin")
	show := flags.Bool("show", false, "report where a search would get its key from, masked")
	forget := flags.Bool("forget", false, "remove the stored credentials file")
	noVerify := flags.Bool("no-verify", false, "store the key without checking it against the router")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "jevlin login: the key is read from stdin (or -key-env VAR), never from an argument")
		return exitUsage
	}
	if *show && *forget {
		fmt.Fprintln(stderr, "jevlin login: -show and -forget are exclusive")
		return exitUsage
	}

	cfg, cfgSource, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin: "+configProblem(cfgSource, err))
		return exitTransport
	}
	path := credentialsPath(cfg.Search)

	switch {
	case *show:
		return loginShow(stdout, stderr, getenv, cfg.Search, path)
	case *forget:
		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintf(stdout, "no stored key at %s\n", path)
				return exitOK
			}
			fmt.Fprintln(stderr, "jevlin login: forget:", err)
			return exitTransport
		}
		fmt.Fprintf(stdout, "removed %s\n", path)
		if getenv(apiKeyEnv) != "" {
			fmt.Fprintln(stdout, "note: JEVLIN_API_KEY is still set in this shell, so searches here keep working")
		}
		return exitOK
	}

	var key string
	if *keyEnv != "" {
		key = strings.TrimSpace(getenv(*keyEnv))
		if key == "" {
			fmt.Fprintf(stderr, "jevlin login: %s is empty or not set\n", *keyEnv)
			return exitUsage
		}
	} else {
		fmt.Fprint(stderr, "paste your sr- key, then press Enter (it is not echoed, logged, or put in a command line): ")
		key, err = readSecret(stdin)
		fmt.Fprintln(stderr)
		if err != nil {
			fmt.Fprintln(stderr, "jevlin login:", err)
			return exitUsage
		}
	}

	router := ""
	if cfg.Search.RouterURL != nil {
		router = strings.TrimRight(cfg.Search.RouterURL.String(), "/")
	}
	if !*noVerify {
		if router == "" {
			fmt.Fprintln(stderr, "jevlin login: no router configured to verify against ([router] url); use -no-verify to store anyway")
			return exitTransport
		}
		ctx, cancel := operatorContext(loginProbeTimeout)
		defer cancel()
		outcome, status, err := probeKey(ctx, router, key)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "jevlin login: could not reach %s to verify the key: %v\n  nothing was stored; retry, or store unverified with -no-verify\n", router, err)
			return exitTransport
		case outcome == probeUnavailable:
			fmt.Fprintf(stderr, "jevlin login: %s answered HTTP %s, so the key could not be verified\n  nothing was stored; retry, or store unverified with -no-verify\n", router, status)
			return exitServerErr
		case outcome == probeRefused:
			fmt.Fprintf(stderr, "jevlin login: %s refused this key (HTTP %s); nothing was stored\n  keys are minted at platform.jevlin.ai → Keys, and must belong to the account you registered with\n", router, status)
			return exitClientErr
		}
	}

	// The router recorded is where the key may be sent from now on, under
	// connect's rule (keyBindingOf): the router this run is configured with.
	stored := credentials{APIKey: key, Router: keyBindingOf(cfg).Router}
	if err := writeCredentials(path, stored); err != nil {
		fmt.Fprintln(stderr, "jevlin login: write:", err)
		return exitTransport
	}
	verb := "verified against " + router + " and "
	if *noVerify {
		verb = "NOT verified; "
	}
	fmt.Fprintf(stdout, "key %s %sstored in %s (owner-only)\n", maskKey(key), verb, path)
	if env := getenv(apiKeyEnv); env != "" && env != key {
		fmt.Fprintln(stdout, "note: JEVLIN_API_KEY is set in this shell to a different key, and the environment wins here")
	}
	return exitOK
}

// loginShow reports resolution without printing a key: which source
// would win, masked, and what the others hold.
func loginShow(stdout, stderr io.Writer, getenv func(string) string, m config.Search, path string) int {
	key, source, err := resolveAPIKey(getenv, m)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin login:", err)
		return exitClientErr
	}
	if key == "" {
		fmt.Fprintf(stdout, "no key: JEVLIN_API_KEY is unset, %s does not exist\n  store one with: jevlin login\n", path)
		return exitOK
	}
	fmt.Fprintf(stdout, "key %s from %s\n", maskKey(key), source)
	if source != keyFromFile {
		if c, ferr := readCredentials(path); ferr == nil {
			fmt.Fprintf(stdout, "also stored: %s in %s (shadowed by the environment)\n", maskKey(c.APIKey), path)
		} else {
			fmt.Fprintf(stdout, "nothing stored at %s\n", path)
		}
	} else if c, ferr := readCredentials(path); ferr == nil && c.Router != "" {
		// The origin, never the recorded text: the binding is the origin
		// (keyService.check), and urlOrigin reads it back only from a URL
		// with a plain ASCII host.
		if o, err := urlOrigin(c.Router); err == nil {
			fmt.Fprintf(stdout, "  verified against %s when stored\n", o)
		}
	}
	return exitOK
}

// readSecret reads the key with echo off when stdin is a terminal, so the
// paste does not land on screen or in a scrollback; anything else (a pipe,
// a heredoc, a test buffer) is read as one line.
func readSecret(r io.Reader) (string, error) {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		data, err := term.ReadPassword(int(f.Fd()))
		if err != nil {
			return "", err
		}
		return secretFromLine(data)
	}
	return readSecretLine(r)
}

// readSecretLine reads one line and trims it; a final line without a
// newline (a piped `printf '%s'`) counts.
func readSecretLine(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	return secretFromLine(data)
}

// secretFromLine keeps the first line and rejects an empty or spaced paste.
func secretFromLine(data []byte) (string, error) {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	key := strings.TrimSpace(string(line))
	if key == "" {
		return "", errors.New("no key on stdin")
	}
	if strings.ContainsAny(key, " \t") {
		return "", errors.New("the key contains whitespace; paste only the key")
	}
	return key, nil
}
