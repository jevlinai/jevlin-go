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

	"github.com/jevlinai/jevlin-go/internal/egress"
	"github.com/jevlinai/jevlin-go/internal/netdial"
	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
	"github.com/jevlinai/jevlin-go/pkg/platform"
)

const (
	credentialsFile         = "credentials.json"
	credentialsVersion      = 1
	registrationJournalFile = "registration_pending.json"
	payoutRecordFile        = "payout.json"
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

// credentials is the on-disk shape. Router and AgentsAPI are the origins the
// key was issued for and may be sent to: the router a search, a limits read,
// a turn end and a fetch go to, and the platform's agents API that connect
// and mining enable call. Both are recorded when the key is stored (login
// records the router, connect both) and never inferred from a config
// afterwards: a config can change after the key was issued, and the record is
// what does not. A key whose file records no router is not sent to any router
// (errUnboundRouterKey), and one that records no agents API to no agents API.
type credentials struct {
	V         int    `json:"v"`
	APIKey    string `json:"api_key"`
	Router    string `json:"router"`
	AgentsAPI string `json:"agents_api,omitempty"`
}

// The names a refusal gives the two keys a credentials file holds.
const (
	routerKeySecret     = "the stored router key"   // #nosec G101 -- a name for a refusal message, not a credential
	envRouterKeySecret  = "JEVLIN_API_KEY"          // #nosec G101 -- a name for a refusal message, not a credential
	platformKeySecret   = "the stored platform key" // #nosec G101 -- a name for a refusal message, not a credential
	keyOriginCode       = "key_origin_mismatch"     // under fix_input
	unboundKeyCode      = "credential_unbound"      // under login
	defaultOriginNotice = "jevlin: %s is not the default %s; the key is sent there because %s names it\n"
)

// errUnboundRouterKey is a stored key whose file records no router: it was
// not stored by this client's login or connect, which always record one.
var errUnboundRouterKey = errors.New("the stored key records no router it was issued for, so it is sent to none; " +
	"store it again with: jevlin login")

// errUnboundPlatformKey is the same for the agents API.
var errUnboundPlatformKey = errors.New("the stored key records no agents API it was issued for, so it is sent to none; " +
	"run: jevlin connect")

// keyOriginError is a request that would carry a key to an origin other than
// the one it is recorded with. It names the origins and nothing else.
type keyOriginError struct {
	refused *egress.RefusedError
	fix     string
}

func (e *keyOriginError) Error() string {
	return fmt.Sprintf("%s belongs to %s, and this configuration names %s; it is not sent there. %s",
		e.refused.Secret, e.refused.Bound, e.refused.Requested, e.fix)
}

func (e *keyOriginError) Unwrap() error { return e.refused }

// keyRefusedForOrigin: is err a refusal to send a key to the origin asked
// for? Classified by type, never by message (invariant 15).
func keyRefusedForOrigin(err error) bool { return egress.IsRefused(err) }

// keyUnbound: is err a stored key that records no origin for the service?
func keyUnbound(err error) bool {
	return errors.Is(err, errUnboundRouterKey) || errors.Is(err, errUnboundPlatformKey)
}

// originOrEmpty is egress.Origin, or "" for a URL that has none.
func originOrEmpty(raw string) string {
	o, err := egress.Origin(raw)
	if err != nil {
		return ""
	}
	return o
}

// keyBindingOf is the binding a key minted for cfg's platform is published
// with: cfg's router and agents API, the services it was issued for.
func keyBindingOf(cfg *config.Config) credentials {
	return credentials{Router: originOrEmpty(cfg.Miner.RouterURL.String()), AgentsAPI: originOrEmpty(cfg.Platform.AgentsAPIURL)}
}

// routerKey is the key a router request carries and the origin it may be
// sent to: the effective router for JEVLIN_API_KEY, which is what the
// environment chose for this run, and the recorded router for the stored key.
type routerKey struct {
	value  string
	source keySource
	bound  string
	secret string
}

// routerKeyFailure classifies resolveAPIKey's refusal for a command that
// reports it: the stored key bound to another router is the configuration's
// to fix, one that records no router is login's, anything else connect's.
func routerKeyFailure(err error) (code string, exit int, action string) {
	switch {
	case keyRefusedForOrigin(err):
		return keyOriginCode, exitUsage, actionFixInput
	case keyUnbound(err):
		return unboundKeyCode, exitClientErr, actionLogin
	default:
		return "credential_unreadable", exitClientErr, actionConnect
	}
}

// noteNonDefaultOrigin prints one line on stderr when a key is about to go to
// an origin other than the compiled default for that service, naming it and
// what chose it, so a configuration or environment that redirects a key is
// visible on every call that carries one.
func noteNonDefaultOrigin(stderr io.Writer, origin, service, def, chooser string) {
	if stderr == nil || origin == "" || origin == originOrEmpty(def) {
		return
	}
	fmt.Fprintf(stderr, defaultOriginNotice, origin, service, chooser)
}

// routerChooser names what set the effective router, for the notice.
func routerChooser(getenv func(string) string) string {
	if getenv(config.EnvRouterURL) != "" {
		return config.EnvRouterURL
	}
	return "the configuration"
}

// credentialsPath is the file beside the intake directory: with the
// standard layout that is ~/.jevlin/credentials.json.
func credentialsPath(m config.Miner) string {
	return filepath.Join(minerRoot(m), credentialsFile)
}

// registrationJournal is registration_pending.json beside credentials.json,
// outside the state directory a sandboxed agent may write.
func registrationJournal(m config.Miner) (*auth.RegistrationJournal, error) {
	return auth.OpenRegistrationJournal(minerRoot(m))
}

// payoutRecord is payout.json beside credentials.json, outside the state
// directory a sandboxed agent may write: the address a resume declares
// unattended must be one the participant decided (auth.PayoutRecord).
func payoutRecord(m config.Miner) (*auth.PayoutRecord, error) {
	if m.IntakeDir == "" {
		// minerRoot would be ".", the working directory: a config always
		// derives intake_dir from state_dir, so only a hand-built one gets
		// here, and the record must not land wherever the command ran.
		return nil, errors.New("no mining.intake_dir, so no directory for the payout record")
	}
	return auth.OpenPayoutRecord(minerRoot(m))
}

// loadPayoutAddress reads the address on file beside credentials.json.
func loadPayoutAddress(m config.Miner) (string, bool, error) {
	record, err := payoutRecord(m)
	if err != nil {
		return "", false, err
	}
	return record.Load()
}

// savePayoutAddress writes the address beside credentials.json.
func savePayoutAddress(m config.Miner, address string) error {
	record, err := payoutRecord(m)
	if err != nil {
		return err
	}
	return record.Save(address)
}

// claimRecord is claim.json beside credentials.json: the claim link and
// code (auth.ClaimRecord), written only from the platform's answer or the
// registration journal beside it, by a foreground run or by a resume
// finishing that journal; a resume in Codex's sandbox cannot write here.
func claimRecord(m config.Miner) (*auth.ClaimRecord, error) {
	if m.IntakeDir == "" {
		return nil, errors.New("no mining.intake_dir, so no directory for the claim record")
	}
	return auth.OpenClaimRecord(minerRoot(m))
}

// claimFor is the stored claim link for agentID, ok=false when none is on
// file for that agent. A record that cannot be read is reported on stderr
// (when given) and treated as absent: the caller's answer to a missing link
// is a re-mint or the no-link message, never another source.
func claimFor(m config.Miner, agentID string, stderr io.Writer) (auth.ClaimBootstrap, bool) {
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
func saveClaim(m config.Miner, b auth.ClaimBootstrap) error {
	record, err := claimRecord(m)
	if err != nil {
		return err
	}
	return record.Save(b)
}

// platformKey is the credential connect stored for platform calls, with the
// agents API it was issued by. The search-only environment override does not
// apply to enrollment. It is refused here, before any call, when cfg's agents
// API is not that origin; every request that carries it goes through
// egress.Do bound to the same origin.
func platformKey(cfg *config.Config, getenv func(string) string) (platform.Key, error) {
	_ = getenv
	creds, err := readCredentials(credentialsPath(cfg.Miner))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return platform.Key{}, errors.New("no platform key on file (credentials.json); run `jevlin connect` first")
		}
		return platform.Key{}, err
	}
	if creds.AgentsAPI == "" {
		return platform.Key{}, errUnboundPlatformKey
	}
	if err := checkKeyOrigin(platformKeySecret, creds.AgentsAPI, cfg.Platform.AgentsAPIURL,
		"Use the configuration this installation was set up with, or run jevlin connect for this one"); err != nil {
		return platform.Key{}, err
	}
	return platform.Key{Value: creds.APIKey, Origin: creds.AgentsAPI}, nil
}

// checkKeyOrigin is egress.Check against a configured destination, worded for
// a command that refuses before it sends anything.
func checkKeyOrigin(secret, bound, dest, fix string) error {
	u, err := url.Parse(dest)
	if err != nil {
		return fmt.Errorf("%s: %v", secret, err)
	}
	if err := egress.Check(secret, bound, u); err != nil {
		var refused *egress.RefusedError
		if errors.As(err, &refused) && refused.Bound != "" {
			return &keyOriginError{refused: refused, fix: fix}
		}
		return err
	}
	return nil
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

// resolveAPIKey applies the order documented above and says where the key
// may go. JEVLIN_API_KEY goes to the effective router, which is what this run
// was configured with; the stored key goes only to the router its file
// records, and is refused here, before any request, when m's router is
// another. A credentials file that exists but cannot be trusted (symlink,
// readable by others, unparseable) preserves its error rather than selecting
// another source. Every caller sends the key through egress with the bound
// origin returned here.
func resolveAPIKey(getenv func(string) string, m config.Miner) (routerKey, error) {
	if k := getenv(apiKeyEnv); k != "" {
		return routerKey{value: k, source: keyFromEnv, bound: originOrEmpty(m.RouterURL.String()), secret: envRouterKeySecret}, nil
	}
	creds, err := readCredentials(credentialsPath(m))
	switch {
	case err == nil:
		if creds.Router == "" {
			return routerKey{}, errUnboundRouterKey
		}
		if err := checkKeyOrigin(routerKeySecret, creds.Router, m.RouterURL.String(),
			"Use the configuration this installation was set up with, or store the key for this router with: jevlin login"); err != nil {
			return routerKey{}, err
		}
		return routerKey{value: creds.APIKey, source: keyFromFile, bound: creds.Router, secret: routerKeySecret}, nil
	case !errors.Is(err, fs.ErrNotExist):
		return routerKey{}, err
	}
	return routerKey{source: keyFromNone}, nil
}

// readCredentials reads and validates the file. Mode checks are gated by
// posixModes exactly as the wallet's are: Go reports 0777 for every file
// on Windows, where the directory ACL is the guard.
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
	// The key is bound to the router it is about to be stored for, so the
	// probe goes through the same guard as every later request carrying it.
	resp, err := egress.Do(&http.Client{Timeout: loginProbeTimeout, CheckRedirect: auth.SameOriginRedirects, Transport: loginProbeTransport},
		req, "the key being stored", originOrEmpty(routerURL))
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
		fmt.Fprintf(stderr, "jevlin: config (%s): %v\n", orDefaults(cfgSource), err)
		return exitTransport
	}
	path := credentialsPath(cfg.Miner)

	switch {
	case *show:
		return loginShow(stdout, stderr, getenv, cfg.Miner, path)
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

	router := strings.TrimRight(cfg.Miner.RouterURL.String(), "/")
	if !*noVerify {
		noteNonDefaultOrigin(stderr, originOrEmpty(router), "router", config.DefaultRouterURL, routerChooser(getenv))
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
			fmt.Fprintf(stderr, "jevlin login: %s refused this key (HTTP %s); nothing was stored\n  keys are minted at platform.jevlin.ai → Keys, and must belong to the account you enrolled with\n", router, status)
			return exitClientErr
		}
	}

	// The router recorded is where the key may be sent from now on. The
	// agents API a key was published with by connect is kept when the same
	// key is stored again, and dropped for another key, which was not issued
	// there as far as this client knows.
	stored := credentials{APIKey: key, Router: originOrEmpty(router)}
	if prior, err := readCredentials(path); err == nil && prior.APIKey == key {
		stored.AgentsAPI = prior.AgentsAPI
	}
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
func loginShow(stdout, stderr io.Writer, getenv func(string) string, m config.Miner, path string) int {
	resolved, err := resolveAPIKey(getenv, m)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin login:", err)
		return exitClientErr
	}
	key, source := resolved.value, resolved.source
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
		fmt.Fprintf(stdout, "  stored for %s, and sent only there\n", c.Router)
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
