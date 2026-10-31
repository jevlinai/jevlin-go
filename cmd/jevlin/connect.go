package main

// connect: the agent-onboarding client (agent onboarding design,
// search-platform-agent-onboarding-design.md §5.5). It registers with
// the search platform, prints the claim URL and code, and polls (bounded)
// until claimed. search works from the moment this stores the key, before
// the claim.
//
// Resume is one poll, not a loop (§5.5): after a served search, search.go
// checks the local registration file with no network and spawns a
// detached `connect -resume`. The resume performs exactly one poll, acts,
// exits — anything else is a daemon by another name.
//
// Structural invariant 11: this file carries no os/exec import. Nothing
// about registering or polling legitimately launches a process; the only
// spawns in this codebase are search.go's and the hooks' detached
// children, and TestConnectNeverImportsOSExec (boundary_test.go) enforces
// it.
//
// Every entry point below — cmdConnect and the detached resume it spawns —
// takes connect.lock for its ENTIRE run, so two invocations (a foreground
// connect and a search-spawned resume, or several resumes racing a burst of
// searches) can never interleave. A resume cooldown stamp keeps shouldResume
// from approving a spawn faster than resumes can possibly complete. Every
// mutation to the stored registration re-reads the on-disk record first
// and fails loudly, never silently, on a write error.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
	"github.com/jevlinai/jevlin-go/pkg/platform"
)

// connectPollBudget and resumePollInterval are vars, not consts, solely
// so a test can shrink them (save, override, t.Cleanup restore) instead
// of waiting out a real 3-minute bound — the production default is what
// matters, and nothing in cmd/jevlin ever overwrites it outside a
// test.
var (
	// connectPollBudget bounds the foreground poll: a human is watching
	// for at most this long, then connect stops and finishes on the next
	// invocation — a detached resume, or a later foreground run.
	connectPollBudget = 3 * time.Minute
	// resumePollInterval paces a resumed poll (foreground repeats, and
	// every detached resume). The server only advertises an interval on
	// Register (§5.1); a resume does not re-register, so there is
	// nothing fresher to honor — a fixed, modest default instead of
	// persisting the original registration's value.
	resumePollInterval = 5 * time.Second
	// resumeCooldown (WP2-adversarial-review finding 1) is the minimum
	// spacing between resume attempts shouldResume will approve. Without
	// it, a burst of searches in claim-approval-watching range each
	// spawns its own detached resume; connect.lock stops them from
	// interleaving, but a pile of processes that only ever queue on the
	// lock and exit is still waste this stops before it starts.
	resumeCooldown = 5 * time.Second
)

// errConnectNoConfig is ruling D-R1's connect refusal: resolution
// (describeConfigSource) found no config file at all, not even the
// installation's own, so there is nothing here connect may register
// against.
var errConnectNoConfig = errors.New("no config file found — looked at -config, JEVLIN_CONFIG " +
	"and the installation's own config; run `jevlin setup` first")

func connectLockPath(stateDir string) string { return filepath.Join(stateDir, "connect.lock") }
func resumeStampPath(stateDir string) string { return filepath.Join(stateDir, "connect_resume.json") }

// unclaimedNoLinkMessage is what a foreground run says when it has no claim
// link to print and the platform did not issue a fresh one. It used to send
// the participant to `jevlin connect -force`, which registers nothing while a
// registration is on file: the advice was the command they had just run.
const unclaimedNoLinkMessage = "this agent is unclaimed, and no claim link is on file for it: the platform did not issue " +
	"a fresh one (see above). Run `jevlin connect` again to retry, or wait for this registration to expire and " +
	"connect again for a new one"

// printableClaimURL returns a stored claim URL only when it still passes
// invariant 12 against the configured [platform] url, which may have
// changed since the link was stored; a copy that fails is treated as
// absent, and the rejected value itself is never echoed. The link comes
// from the claim record beside credentials.json (claimFor), never from
// agent.json. stderr may be nil.
func printableClaimURL(raw, baseURL string, stderr io.Writer) string {
	if raw == "" {
		return ""
	}
	if platform.ValidateStoredClaimURL(raw, baseURL) != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "jevlin: the stored claim link is not on the configured [platform] url (%s); not printing it\n", baseURL)
		}
		return ""
	}
	return raw
}

// remintClaimLink asks the platform for a fresh claim link for a still-
// unclaimed registration whose link is gone (B.1/B.3), persists it, and
// reports whether reg now carries one it is safe to print. The platform's
// POST /v1/agents/{id}/claim-code is the one way back to a working human
// link: the original code is never re-served, and minting kills whatever
// code existed before — which is why only a deliberate foreground connect
// calls this, never -resume (a detached run must not invalidate a link a
// person may still be holding). Persisted before it is shown (invariant
// 7's order): a link that could not be saved is not printed, because the
// next run would mint again and kill it mid-use.
//
// On any failure the caller falls back to exactly the B.3 dead end this
// closes — against a platform without the route (404) nothing changes but
// one line on stderr.
func remintClaimLink(ctx context.Context, client *platform.Client, store *auth.Store, m config.Search, reg *auth.AgentRegistration, key string, stderr io.Writer) (auth.ClaimBootstrap, bool) {
	fresh, err := client.ClaimCode(ctx, reg.AgentID, key)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin: could not mint a fresh claim link:", err)
		return auth.ClaimBootstrap{}, false
	}
	claim := auth.ClaimBootstrap{AgentID: reg.AgentID, ClaimURL: fresh.ClaimURL, ClaimCode: fresh.ClaimCode, ClaimExpiresAt: fresh.ClaimExpiresAt}
	if err := saveClaim(m, claim); err != nil {
		fmt.Fprintln(stderr, "jevlin: persist the fresh claim link:", err)
		return auth.ClaimBootstrap{}, false
	}
	updated := *reg
	updated.ClaimExpiresAt = fresh.ClaimExpiresAt
	if err := store.SaveAgentRegistration(updated); err != nil {
		fmt.Fprintln(stderr, "jevlin: persist the fresh claim link's expiry:", err)
		return auth.ClaimBootstrap{}, false
	}
	*reg = updated
	return claim, true
}

// publishRebuiltRegistration writes the record a rebuild takes from what
// GET /v1/agents/me answered (B.3): the platform's own state in agent.json,
// and, while the agent is unclaimed, its claim link in the claim record
// beside credentials.json. When /me carried no link for an unclaimed agent
// (B.1's known gap) it mints one (B.4): one deliberate foreground attempt,
// and on failure this run is exactly the B.3 dead end it always was. stop
// reports that the run ends here, with code. A record that could not be
// persisted is retried from scratch by the next run, which is why nothing
// else is written first.
func publishRebuiltRegistration(ctx context.Context, client *platform.Client, store *auth.Store, m config.Search, identity *platform.AgentIdentity, credKey string, stdout, stderr io.Writer) (reg auth.AgentRegistration, code int, stop bool) {
	reg = auth.AgentRegistration{
		AgentID:        identity.AgentID,
		Status:         identity.Status,
		Scopes:         identity.Scopes,
		ClaimExpiresAt: identity.ClaimExpiresAt,
	}
	if serr := store.SaveAgentRegistration(reg); serr != nil {
		fmt.Fprintln(stderr, "jevlin: rebuilt registration from the platform but could not persist it:", serr)
		return auth.AgentRegistration{}, exitTransport, true
	}
	if identity.Status != "unclaimed" {
		return reg, exitOK, false
	}
	if identity.ClaimURL != "" {
		claim := auth.ClaimBootstrap{AgentID: identity.AgentID, ClaimURL: identity.ClaimURL, ClaimCode: identity.ClaimCode, ClaimExpiresAt: identity.ClaimExpiresAt}
		if err := saveClaim(m, claim); err != nil {
			// Not printed unless persisted: the print below finds no
			// record for this agent and re-mints, or says it has none.
			fmt.Fprintln(stderr, "jevlin: could not persist the claim link the platform returned:", err)
		}
		return reg, exitOK, false
	}
	if _, ok := remintClaimLink(ctx, client, store, m, &reg, credKey, stderr); !ok {
		fmt.Fprintln(stdout, unclaimedNoLinkMessage)
		return reg, exitOK, true
	}
	// The claim record holds a live link now; the ordinary narration and
	// poll treat this like any other unclaimed registration.
	return reg, exitOK, false
}

// rebuildUnknownRegistration is B.3's rebuild for a record that decodes but
// names an agent the platform does not know for the stored key (hard
// invariant 13: a record the platform does not recognize is rebuilt like
// a lost one). The platform resolves a key to its own agent and answers
// not found for any other id, so the record and the key disagree: an older
// build crashed between writing a replacement's key and its record, or a
// sandboxed command rewrote agent.json to name another agent. The key is
// the authority (it lives beside credentials.json, outside every writable
// root), and /v1/agents/me says which agent it is. Nothing local changes
// before /me answers. ok=false is nothing to rebuild from: /me failed,
// found no agent for the key, or named the very agent the status call did
// not know, and the caller keeps the behavior it had before. Never called
// by -resume or under -force, which replaces rather than recovers.
func rebuildUnknownRegistration(ctx context.Context, client *platform.Client, store *auth.Store, m config.Search, old auth.AgentRegistration, credKey string, stdout, stderr io.Writer) (reg auth.AgentRegistration, code int, stop, ok bool) {
	identity, err := client.Me(ctx, credKey)
	if err != nil {
		if !errors.Is(err, platform.ErrAgentNotFound) {
			fmt.Fprintln(stderr, "jevlin: could not ask the platform which agent the stored key belongs to:", err)
		}
		return old, exitOK, false, false
	}
	if identity.AgentID == old.AgentID {
		return old, exitOK, false, false
	}
	reg, code, stop = publishRebuiltRegistration(ctx, client, store, m, identity, credKey, stdout, stderr)
	if reg.AgentID != "" {
		// Only the id /me returned is named, once persisted: the id agent.json
		// held came from a directory a sandboxed command can write, and a
		// message never repeats what a record there says.
		fmt.Fprintf(stderr, "jevlin: agent.json named an agent the platform does not know for the stored key; "+
			"rebuilt the registration from the platform (agent %s)\n", reg.AgentID)
	}
	if stop {
		return reg, code, true, true
	}
	return reg, exitOK, false, true
}

// resumeStamp records the last time -resume was attempted (successfully
// spawned or not — the point is pacing attempts, not counting successes).
type resumeStamp struct {
	V           int       `json:"v"`
	LastAttempt time.Time `json:"last_attempt"`
}

func readResumeStamp(path string) resumeStamp {
	data, err := fsx.ReadRegular(path, stampMaxBytes)
	if err != nil {
		return resumeStamp{}
	}
	var st resumeStamp
	if json.Unmarshal(data, &st) != nil || st.V != 1 {
		return resumeStamp{}
	}
	return st
}

func writeResumeStamp(path string, st resumeStamp) error {
	st.V = 1
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// The state dir is a writable root of Codex's sandbox: fsx stages under
	// a random name it creates exclusively, so no link at a predictable
	// temporary name is written through (the old "<path>.<pid>.tmp" was).
	return fsx.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data, 0o600)
}

type registrationPublicationOptions struct {
	force                bool
	replacingExpired     bool
	allowCorruptPreserve bool
	previousPlatformKey  string
	// binding is where the published key may be sent: the origins the
	// journal recorded when the key was minted (pendingBinding). A journal
	// an older client wrote records none, and its key falls to the
	// installation's own config until a connect under that config records
	// them.
	binding credentials
}

// pendingRegistrationFromPlatform is the journal record of a key cfg's
// platform just minted, with the router and agents API it is for
// (keyBindingOf), so whichever run finishes the journal publishes the key
// with the origins it was minted under rather than its own config's.
func pendingRegistrationFromPlatform(reg *platform.Registration, cfg *config.Config) auth.PendingRegistration {
	b := keyBindingOf(cfg)
	return auth.PendingRegistration{
		AgentID:        reg.AgentID,
		Key:            reg.Key,
		ClaimURL:       reg.ClaimURL,
		ClaimCode:      reg.ClaimCode,
		ClaimExpiresAt: reg.ClaimExpiresAt,
		Status:         "unclaimed",
		PollIntervalMS: reg.PollInterval.Milliseconds(),
		Router:         b.Router,
		AgentsAPI:      b.AgentsAPI,
	}
}

// pendingBinding is the binding a journal's key is published with.
func pendingBinding(p auth.PendingRegistration) credentials {
	return credentials{Router: p.Router, AgentsAPI: p.AgentsAPI}
}

func agentRegistrationFromPending(reg auth.PendingRegistration) auth.AgentRegistration {
	return auth.AgentRegistration{
		AgentID:        reg.AgentID,
		Status:         reg.Status,
		ClaimExpiresAt: reg.ClaimExpiresAt,
	}
}

func claimFromPending(reg auth.PendingRegistration) auth.ClaimBootstrap {
	return auth.ClaimBootstrap{AgentID: reg.AgentID, ClaimURL: reg.ClaimURL, ClaimCode: reg.ClaimCode, ClaimExpiresAt: reg.ClaimExpiresAt}
}

func sameAgentRegistration(a auth.AgentRegistration, p auth.PendingRegistration) bool {
	return a.AgentID == p.AgentID && a.Status == p.Status && a.ClaimExpiresAt == p.ClaimExpiresAt
}

func credentialFileState(path string) (exists bool, key string, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return true, "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, "", fmt.Errorf("credentials: %s is not a regular file", path)
	}
	creds, err := readCredentials(path)
	if err != nil {
		return true, "", err
	}
	return true, creds.APIKey, nil
}

func preflightFreshRegistration(m config.Search, force bool) error {
	exists, key, err := credentialFileState(credentialsPath(m))
	if !exists {
		return nil
	}
	if err != nil {
		return fmt.Errorf("credentials.json exists but cannot be trusted; refusing before remote Register: %w", err)
	}
	if !force && looksLikePlatformKey(key) {
		return errors.New("credentials.json already holds a platform key; refusing to mint another agent. Pass -force if you mean to switch this installation to a new registration")
	}
	if !force {
		return errors.New("credentials.json already exists; refusing to overwrite it before remote Register. Pass -force if you mean to replace this installation's platform credential")
	}
	return nil
}

func publishPlatformCredential(path, key string, opts registrationPublicationOptions) error {
	published := opts.binding
	published.APIKey = key
	exists, current, err := credentialFileState(path)
	if !exists {
		return writeCredentials(path, published)
	}
	if err != nil {
		return fmt.Errorf("cannot publish platform credential: %w", err)
	}
	if current == key {
		return nil
	}
	if opts.force || (opts.replacingExpired && opts.previousPlatformKey != "" && current == opts.previousPlatformKey) {
		return writeCredentials(path, published)
	}
	return errors.New("cannot publish platform credential: credentials.json contains a different platform key")
}

func publishPendingRegistration(store *auth.Store, m config.Search, pending auth.PendingRegistration, opts registrationPublicationOptions) (auth.AgentRegistration, error) {
	if err := publishPlatformCredential(credentialsPath(m), pending.Key, opts); err != nil {
		return auth.AgentRegistration{}, err
	}
	// The claim link goes beside the credential, before agent.json: the
	// record that tells a person which link to open is written only here,
	// by the rebuild and by the re-mint, never by anything that runs in a
	// sandbox.
	if pending.ClaimURL != "" {
		if err := saveClaim(m, claimFromPending(pending)); err != nil {
			return auth.AgentRegistration{}, fmt.Errorf("cannot publish the claim link: %w", err)
		}
	}

	reg, ok, err := store.LoadAgentRegistration()
	if err != nil {
		if !opts.allowCorruptPreserve {
			return auth.AgentRegistration{}, fmt.Errorf("cannot publish agent registration: %w", err)
		}
		// Not only a record that does not decode: a link, a directory, a FIFO
		// or a file others can read at agent.json refuses to load too, and a
		// sandboxed command can leave any of them there. Refusing on them left
		// the journal unfinished and every later run failing the same way,
		// with the journal's key already published. The name is renamed aside,
		// never opened or followed, and the journal's record published.
		setAside := store.SetAsideUnreadableAgentRegistration
		if errors.Is(err, auth.ErrAgentRegistrationCorrupt) {
			setAside = store.PreserveCorruptAgentRegistration
		}
		if err := setAside(); err != nil {
			return auth.AgentRegistration{}, err
		}
		ok = false
	}
	// credentials.json holds the journal's key now: publishPlatformCredential
	// refused anything else. So agent.json must describe that key's agent,
	// and one naming another agent is the state directory's word against
	// the journal's and the credential's, both outside it. It used to win:
	// publication refused "a different identity" after the key was already
	// published, the journal stayed, and every later run failed the same
	// way, the planted record holding the registration hostage. Now it is
	// set aside, kept as evidence, and the journal's record is published.
	if ok && reg.AgentID != pending.AgentID {
		if err := store.SetAsideAgentRegistration(); err != nil {
			return auth.AgentRegistration{}, fmt.Errorf("cannot set aside agent.json, which names another agent: %w", err)
		}
		ok = false
	}
	if !ok || !sameAgentRegistration(reg, pending) {
		if err := store.SaveAgentRegistration(agentRegistrationFromPending(pending)); err != nil {
			return auth.AgentRegistration{}, err
		}
	}

	verifiedCreds, err := readCredentials(credentialsPath(m))
	if err != nil || verifiedCreds.APIKey != pending.Key {
		return auth.AgentRegistration{}, errors.New("cannot verify published platform credential")
	}
	verifiedReg, ok, err := store.LoadAgentRegistration()
	if err != nil || !ok || !sameAgentRegistration(verifiedReg, pending) {
		return auth.AgentRegistration{}, errors.New("cannot verify published agent registration")
	}
	if pending.ClaimURL != "" {
		if claim, ok := claimFor(m, pending.AgentID, nil); !ok || claim != claimFromPending(pending) {
			return auth.AgentRegistration{}, errors.New("cannot verify the published claim link")
		}
	}
	return verifiedReg, nil
}

// cmdConnect is the text path unless -json is asked for, in which case it
// is the same run with its narration captured and one envelope emitted
// from what the run persisted.
//
// The registration implementation is shared: the same single-flight lock,
// the same pending-registration recovery, the same poll loop. A JSON mode
// that took a different path through registration would be a second,
// less-tested registration client.
func cmdConnect(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return connectCommand(args, stdin, stdout, stderr, getenv, false)
}

// connectAdmitted is connect for a caller whose own lifecycle admission
// covers this run: setup, which holds setup.lock and must not take the
// lifecycle gate again after it (lifecycle.go's lock order).
func connectAdmitted(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return connectCommand(args, stdin, stdout, stderr, getenv, true)
}

func connectCommand(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, admitted bool) int {
	if !jsonRequested(args) {
		return connectRun(args, stdin, stdout, stderr, getenv, false, admitted)
	}
	cfgPath, parseErr := connectMachineFlags(args)
	// Ruling D-R1: with no config file resolved at all, loadConfig still
	// succeeds (it falls back to built-in defaults, which is exactly the
	// state connect must refuse to register against). describeConfigSource
	// is the same pure, side-effect-free resolution connectRun itself uses,
	// so this and connectRun's own check can never disagree.
	if parseErr == nil && describeConfigSource(cfgPath, getenv) == "" {
		emitMachine(stdout, commandEnvelope{
			machineHeader: newMachineHeader("connect", exitTransport, "config_not_found", false, actionFixInput),
			Error:         clientMessage(errConnectNoConfig),
		})
		return exitTransport
	}
	// The stored key goes only to the agents API it is bound to; a config
	// naming another is a request to change, said before anything is read
	// or written. connectRun refuses the same way for the text path.
	if parseErr == nil {
		if err := connectKeyRefusal(cfgPath, getenv); err != nil {
			emitMachine(stdout, keyOriginEnvelope(err))
			return exitUsage
		}
	}
	var narration bytes.Buffer
	code := connectRun(args, stdin, io.Discard, &narration, getenv, true, admitted)
	if code == exitLifecycleBusy {
		// Structural, like the human-decision sentinel below: the gate was
		// held for this whole wait, and nothing of the installation was read
		// or written. The same call may succeed once the other operation ends.
		retryAfter := lifecycleBusyRetryAfter.Milliseconds()
		emitMachine(stdout, lifecycleBusyEnvelope{
			machineHeader: newMachineHeader("connect", exitTransport, "lifecycle_busy", true, actionRetry),
			RetryAfterMS:  &retryAfter,
			Error:         clientMessage(errLifecycleBusy),
		})
		return exitTransport
	}
	if code == exitConfigNotFound {
		// Structural, like the two sentinels above: resolution ran and
		// found nothing, before anything of an installation was opened or
		// written. Not retryable — running it again with the same
		// environment resolves nothing new; fix_input is what applies to an
		// environment/config problem a participant must correct before this
		// command can do anything.
		emitMachine(stdout, commandEnvelope{
			machineHeader: newMachineHeader("connect", exitTransport, "config_not_found", false, actionFixInput),
			Error:         clientMessage(errConnectNoConfig),
		})
		return exitTransport
	}
	if code == exitKeyOffOrigin {
		emitMachine(stdout, keyOriginEnvelope(connectKeyRefusal(cfgPath, getenv)))
		return exitUsage
	}
	if code == exitForceDoesNotRebuild {
		// -force met a record naming another agent than the stored key's,
		// and -force does not rebuild: the same command can never succeed,
		// so the instruction is to change the input, not to retry it.
		emitMachine(stdout, commandEnvelope{
			machineHeader: newMachineHeader("connect", exitUsage, "force_does_not_rebuild", false, actionFixInput),
			Error: &machineError{
				Message: "agent.json names another agent than the stored key's, and -force does not rebuild a " +
					"registration; run `jevlin connect` without -force to rebuild it from the platform",
				Source: "client",
			},
		})
		return exitUsage
	}
	if parseErr != nil {
		// connectRun's own parser has now produced the real usage
		// failure, and the caller still gets exactly one envelope. No
		// store is read for it: an invalid command says nothing about
		// this installation, and reporting some default installation's
		// registration against it would be worse than reporting nothing.
		emitMachine(stdout, commandEnvelope{
			machineHeader: newMachineHeader("connect", code, "invalid_flags", false, actionFixInput),
			Error:         clientMessage(parseErr),
		})
		return code
	}
	emitMachine(stdout, connectEnvelope(cfgPath, getenv, code, narration.String()))
	return code
}

// connectKeyRefusal is the stored key's binding as connect asks it: nil
// unless credentials.json, or the registration journal beside it, holds a
// key bound to another agents API than the config at cfgPath names. A config
// that does not load is connectRun's to report.
func connectKeyRefusal(cfgPath string, getenv func(string) string) error {
	cfg, _, err := loadConfig(cfgPath, getenv)
	if err != nil {
		return nil
	}
	return connectOriginRefusal(cfg, getenv)
}

// connectOriginRefusal is every key connect may send to cfg's agents API:
// the one in credentials.json, and the one a registration journal is about
// to publish, which is bound to the agents API that minted it.
func connectOriginRefusal(cfg *config.Config, getenv func(string) string) error {
	if err := storedKeyRefusedFor(agentsAPIKeyService, cfg.Search, cfg.Platform.AgentsAPIURL, getenv); err != nil {
		return err
	}
	journal, err := registrationJournal(cfg.Search)
	if err != nil {
		return nil
	}
	pending, ok, err := journal.Load()
	if err != nil || !ok || pending.AgentsAPI == "" {
		return nil
	}
	bound, err := urlOrigin(pending.AgentsAPI)
	if err != nil {
		return nil
	}
	requested, err := urlOrigin(cfg.Platform.AgentsAPIURL)
	if err != nil || requested != bound {
		return &keyOriginError{service: agentsAPIKeyService.name, bound: bound, requested: requested}
	}
	return nil
}

func keyOriginEnvelope(err error) commandEnvelope {
	return commandEnvelope{
		machineHeader: newMachineHeader("connect", exitUsage, keyOriginCode, false, actionFixInput),
		Error:         clientMessage(err),
	}
}

// connectMachineFlags re-reads the flag machine mode needs before the run
// starts: -config for the envelope's own store lookup. Parsing twice is
// cheaper than threading it out of a function with thirty exit points.
//
// It declares the same flags as connectRun — one grammar, not two — and it
// reports a parse failure rather than swallowing it. Swallowing it was the
// bug: zero values look exactly like "no -config given", so a mistyped
// flag was answered as if it were a question about the default
// installation. The caller must be able to tell "the command is invalid"
// from "the command is valid and says nothing".
func connectMachineFlags(args []string) (cfgPath string, err error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := fs.String("config", "", "")
	fs.String("name", "", "")
	fs.Bool("resume", false, "")
	fs.Bool("force", false, "")
	fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return *cfg, nil
}

// machine is explicit, never inferred from stdout == io.Discard: the one
// place inside this function that needs to know is the record-naming-
// another-agent branch below, and detecting it from the writer would be
// guessing the caller's intent from an implementation detail of how
// cmdConnect happens to capture narration today.
func connectRun(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, machine, admitted bool) int {
	fs := newFlagSet("connect", stderr)
	cfgPath := fs.String("config", "", "path to TOML config file")
	name := fs.String("name", "", "a name for this agent (optional)")
	resume := fs.Bool("resume", false, "internal: exactly one poll, act, exit — used by the detached resume search spawns")
	force := fs.Bool("force", false, "overwrite an existing credentials.json even though it already holds a different platform key")
	fs.Bool("json", false, "report as one JSON object instead of text")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	// Lifecycle admission comes before the config is loaded or the state
	// directory opened: an uninstall or upgrade holding the gate must find
	// nothing of this run on disk. -resume is a detached child and never
	// waits; setup's in-process connect is already admitted.
	mode := admitForeground
	switch {
	case admitted:
		mode = admitAlreadyAdmitted
	case *resume:
		mode = admitDetached
	}
	gatePath, err := configGatePath(*cfgPath, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	gate, err := admitOrdinary(gatePath, mode)
	if err != nil {
		if *resume {
			fmt.Fprintf(stderr, "connect -resume: %v; not resuming now\n", err)
			return exitOK
		}
		fmt.Fprintf(stderr, "jevlin connect: %v (%s); nothing was changed, run connect again shortly\n", err, gatePath)
		if machine {
			return exitLifecycleBusy
		}
		return exitTransport
	}
	defer gate.release()

	cfg, cfgSource, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin: "+configProblem(cfgSource, err))
		return exitTransport
	}
	if cfgSource == "" {
		// Ruling D-R1: resolution exhausted every source, including the
		// installation's own config, and found none — connect must not
		// register against built-in-default state (which no participant
		// asked for and no other command's -config would ever land on).
		// Returning here, before OpenStore, means nothing is written: no
		// state directory, no lock, no credential.
		//
		// In JSON mode this is normally caught earlier, by connectCommand's
		// own pre-check (same describeConfigSource call, same cfgPath and
		// getenv, so the two can never disagree) — before the lifecycle
		// gate above is even taken. This is the text-mode refusal and the
		// structural safety net for machine mode: the sentinel exists so
		// that a bug that let this branch run without going through the
		// pre-check still classifies correctly rather than falling into
		// the generic retryable "connect_failed" every other exitTransport
		// gets.
		fmt.Fprintln(stderr, "jevlin connect:", errConnectNoConfig)
		if machine {
			return exitConfigNotFound
		}
		return exitTransport
	}
	// Every platform call below carries the stored key, or the journal's
	// once it is published, so a config naming another agents API than the
	// one it is bound to is refused here, before the state directory is
	// opened. platformKey holds each call to the same rule.
	if err := connectOriginRefusal(cfg, getenv); err != nil {
		if *resume {
			fmt.Fprintln(stderr, "connect -resume:", err)
			return exitOK
		}
		fmt.Fprintln(stderr, "jevlin connect:", err)
		if machine {
			return exitKeyOffOrigin
		}
		return exitUsage
	}
	store, err := auth.OpenStore(cfg.Search.StateDir)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	// The trace key is made here, outside any sandbox and in a directory this
	// run writes anyway, so a search that cannot write there only has to read
	// it and keeps one session id instead of sending a new one each time. Best
	// effort and silent: a search makes the key itself where it can write.
	_, _ = store.EnsureTraceKey()

	if *resume {
		// Pace attempts before anything else — including before the lock
		// attempt below, so even a resume that finds the lock held (and
		// exits immediately) still counts toward the cooldown shouldResume
		// consults. Best effort: a failed write here costs pacing
		// accuracy, not correctness.
		_ = writeResumeStamp(resumeStampPath(cfg.Search.StateDir), resumeStamp{LastAttempt: time.Now()})
	}

	// Single-flight. This lock is held for cmdConnect's ENTIRE run —
	// register, the whole poll loop. A second connect/connect -resume
	// finds it held and exits quietly rather than racing the first for the
	// same registration.
	lock, held, err := tryLockFile(connectLockPath(cfg.Search.StateDir))
	// Admission ends here: from now on this run is excluded by connect.lock
	// alone, and the gate must not be held through a poll loop.
	gate.release()
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	if !held {
		if !*resume {
			fmt.Fprintln(stdout, "connect: another connect is already running; nothing to do")
		}
		return exitOK
	}
	defer func() { _ = unlockFile(lock) }()
	lifecycleEvent("operation locked")

	// ctx bounds SIGINT/SIGTERM cancellation and, for -resume, its one
	// round trip's worth of patience. It deliberately does NOT carry the
	// foreground loop's connectPollBudget: that bound is a plain
	// wall-clock deadline checked between iterations below, each of
	// which gets its own short-lived derived context. Tying ctx itself
	// to connectPollBudget raced the graceful "not claimed yet" exit
	// against ctx.Done() firing mid-select, and ctx.Done() usually won —
	// every foreground call surfaced as "connect: interrupted" instead
	// of the friendly timeout message, because the deadline and the
	// context's own timeout expired at effectively the same instant.
	roundTrip := 30 * time.Second
	outerBound := roundTrip
	if !*resume {
		outerBound = 24 * time.Hour // signal-cancellation only, in practice
	}
	ctx, cancel := operatorContext(outerBound)
	defer cancel()

	client := platform.New(cfg.Platform.AgentsAPIURL, cfg.Platform.BaseURL)

	interval := resumePollInterval
	var reg auth.AgentRegistration
	var existed bool
	var key string

	// A journal in the state directory is from an older release, or forged by
	// a sandboxed command: either way it is not trusted to publish a key.
	if discarded, derr := store.DiscardLegacyPendingRegistration(); derr != nil {
		fmt.Fprintln(stderr, "jevlin: ignoring registration_pending.json in the state directory:", derr)
	} else if discarded {
		fmt.Fprintln(stderr, "jevlin: discarded registration_pending.json from the state directory unread; a sandboxed command can write there")
	}
	journal, err := registrationJournal(cfg.Search)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	pending, hasPending, err := journal.Load()
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	if hasPending {
		reg, err = publishPendingRegistration(store, cfg.Search, pending, registrationPublicationOptions{
			allowCorruptPreserve: true,
			replacingExpired:     pending.ReplaceExpired,
			previousPlatformKey:  pending.PreviousKey,
			binding:              pendingBinding(pending),
		})
		if err != nil {
			fmt.Fprintln(stderr, "jevlin: pending registration recovery:", err)
			return exitTransport
		}
		if err := journal.Clear(); err != nil {
			fmt.Fprintln(stderr, "jevlin: clear pending registration:", err)
			return exitTransport
		}
		if pending.PollIntervalMS > 0 {
			interval = time.Duration(pending.PollIntervalMS) * time.Millisecond
		}
	} else {
		var loadErr error
		reg, existed, loadErr = store.LoadAgentRegistration()
		// loaded is a record read from disk this run, as opposed to one
		// rebuilt from the platform below or published from the journal
		// above: only a record from disk can have been rewritten by a
		// sandboxed command, so only it is asked about before anything is
		// printed.
		loaded := existed
		wasCorrupt := false
		if loadErr != nil {
			if !errors.Is(loadErr, auth.ErrAgentRegistrationCorrupt) {
				fmt.Fprintln(stderr, "jevlin:", loadErr)
				return exitTransport
			}
			// An unreadable registration may be recoverable only when no
			// platform credential exists beside it. preflightFreshRegistration
			// below enforces that rule before any new Register call.
			fmt.Fprintln(stderr, "jevlin: agent registration on file could not be decoded; checking for a credential conflict:", loadErr)
			reg, existed = auth.AgentRegistration{}, false
			wasCorrupt = true
		}

		// B.3: a corrupt or absent record beside a stored platform
		// credential can now be rebuilt from the platform itself
		// (GET /v1/agents/me — pkg/platform's Me, added by the router
		// team specifically so this no longer has to mint a whole new
		// agent) rather than refused outright, amending agent onboarding
		// design rule 8. Observe first: nothing local changes before /me
		// answers. -resume never attempts this (or a fresh Register) —
		// it is the unattended background poll, not the place to
		// originate a new registration or decide a corrupt one is dead.
		// -force bypasses it deliberately, the same "replace, don't
		// recover" signal it already is for the expired-replacement path
		// below.
		if !existed {
			if *resume {
				fmt.Fprintln(stdout, "connect -resume: no usable registration on file; run `jevlin connect` to register")
				return exitOK
			}
			if !*force {
				if credKey, keyErr := platformKey(cfg, getenv); keyErr == nil {
					identity, meErr := client.Me(ctx, credKey)
					if meErr != nil {
						// ErrAgentNotFound and any other /me failure are both
						// treated as "nothing to rebuild": preflightFreshRegistration
						// gives the exact refusal a plain Register attempt would
						// have given today (a stored platform key without -force),
						// naming -force, with agent.json (corrupt or absent) left
						// byte-identical — nothing is renamed on a refusal.
						if !errors.Is(meErr, platform.ErrAgentNotFound) {
							fmt.Fprintln(stderr, "jevlin: could not rebuild the registration from the platform:", meErr)
						}
						if perr := preflightFreshRegistration(cfg.Search, *force); perr != nil {
							fmt.Fprintln(stderr, "jevlin:", perr)
						}
						return exitTransport
					}

					if wasCorrupt {
						if perr := store.PreserveCorruptAgentRegistration(); perr != nil {
							fmt.Fprintln(stderr, "jevlin:", perr)
							return exitTransport
						}
					}
					rebuilt, code, stop := publishRebuiltRegistration(ctx, client, store, cfg.Search, identity, credKey, stdout, stderr)
					if stop {
						return code
					}
					reg, existed = rebuilt, true
				}
			}
		}

		// A foreground connect asks the platform about a record read from
		// disk before it prints a claim link, polls or replaces anything.
		// agent.json is in the state directory, which a sandboxed command
		// can write, and an older build that crashed while replacing an
		// expired registration left a record naming the old agent beside
		// the new agent's key. The key is the authority (it lives beside
		// credentials.json, outside every writable root), and GET
		// /v1/agents/me says which agent it belongs to and what state that
		// agent is in. So the question is /me, not the status of whatever id
		// the record names: an id the key does not own answered not-found
		// and was rebuilt, but an id it does own with a planted link, or a
		// record naming "me" itself, passed. The platform's answer is
		// applied before anything is printed: a claimed agent prints no
		// link, an expired one goes to the replacement below.
		//
		// A record just rebuilt from /me or published from the journal is
		// the platform's answer already and is not asked again (loaded is
		// false for it); nor is a record that already says expired, which
		// the replacement below asks about. Detached resume never asks, and
		// never rebuilds or replaces an identity in the background. -force
		// does not rebuild either: it replaces only an absent, unreadable or
		// expired registration, and says so for a record naming another
		// agent rather than polling an agent the key does not own.
		if existed && loaded && !*resume && reg.Status != "expired" {
			if meKey, keyErr := platformKey(cfg, getenv); keyErr == nil {
				identity, meErr := client.Me(ctx, meKey)
				switch {
				case meErr == nil && identity.AgentID == reg.AgentID:
					if identity.Status != reg.Status || !slices.Equal(identity.Scopes, reg.Scopes) || identity.ClaimExpiresAt != reg.ClaimExpiresAt {
						reg.Status, reg.Scopes, reg.ClaimExpiresAt = identity.Status, identity.Scopes, identity.ClaimExpiresAt
						if err := store.SaveAgentRegistration(reg); err != nil {
							fmt.Fprintln(stderr, "jevlin:", err)
							return exitTransport
						}
					}
				case meErr == nil && *force:
					fmt.Fprintf(stderr, "jevlin: agent.json names another agent than the stored key's (agent %s); "+
						"-force does not rebuild a registration. Run `jevlin connect` without -force to rebuild it from the platform\n",
						identity.AgentID)
					if machine {
						return exitForceDoesNotRebuild
					}
					return exitTransport
				case meErr == nil:
					rebuilt, code, stop := publishRebuiltRegistration(ctx, client, store, cfg.Search, identity, meKey, stdout, stderr)
					if rebuilt.AgentID != "" {
						// Only the id /me returned is named, once persisted:
						// a message never repeats what agent.json, in a
						// directory a sandboxed command can write, says.
						fmt.Fprintf(stderr, "jevlin: agent.json named an agent the stored key does not belong to; "+
							"rebuilt the registration from the platform (agent %s)\n", rebuilt.AgentID)
					}
					if stop {
						return code
					}
					reg = rebuilt
				case errors.Is(meErr, platform.ErrAgentNotFound):
					// The key itself is unknown or revoked, or the platform
					// has no /me: nothing to rebuild from. The poll below
					// says what the platform says about the record's
					// agent, as it always has.
				case reg.Status == "unclaimed":
					// Fail closed: the check that would show the link is
					// this agent's could not be made, so no link is shown,
					// and none is minted.
					fmt.Fprintln(stderr, "jevlin: could not ask the platform which agent the stored key belongs to, so no claim link is shown:", meErr)
					return exitTransport
				default:
					// A claimed record prints no link; the poll below
					// reports what the platform says about it.
				}
			}
		}

		if existed && reg.Status == "expired" {
			if *resume {
				fmt.Fprintln(stdout, "this registration expired before being claimed; run `jevlin connect` again for a new one")
				return exitOK
			}

			oldReg := reg
			oldKey, keyErr := platformKey(cfg, getenv)
			replace := false
			if keyErr == nil {
				st, statusErr := client.Status(ctx, oldReg.AgentID, oldKey)
				switch {
				case statusErr == nil && st.Status == "expired":
					replace = true
				case statusErr == nil:
					reg.Status = st.Status
					reg.Scopes = st.Scopes
					reg.ClaimExpiresAt = st.ClaimExpiresAt
					if err := store.SaveAgentRegistration(reg); err != nil {
						fmt.Fprintln(stderr, "jevlin:", err)
						return exitTransport
					}
				case errors.Is(statusErr, platform.ErrAgentNotFound):
					// Not known is not expired, so nothing is replaced. But
					// the stored key may belong to another agent: an older
					// build that crashed mid-replacement wrote the new key
					// and never the new record. /v1/agents/me says which,
					// and that answer is rebuilt, not a new registration.
					if !*force {
						rebuilt, code, stop, ok := rebuildUnknownRegistration(ctx, client, store, cfg.Search, oldReg, oldKey, stdout, stderr)
						if stop {
							return code
						}
						if ok {
							reg = rebuilt
							break
						}
					}
					fmt.Fprintln(stderr, "jevlin: expired registration is no longer known to the platform; refusing automatic replacement")
					return exitTransport
				default:
					fmt.Fprintln(stderr, "jevlin: could not safely verify the expired registration before replacement, even with -force:", statusErr)
					return exitTransport
				}
			} else {
				fmt.Fprintln(stderr, "jevlin: cannot safely verify the expired registration without its platform credential; refusing replacement even with -force")
				return exitTransport
			}

			if replace {
				fresh, registerErr := client.Register(ctx, *name, clientIdentifier(), nil)
				if registerErr != nil {
					fmt.Fprintln(stderr, "jevlin: register:", registerErr)
					return exitTransport
				}
				pending = pendingRegistrationFromPlatform(fresh, cfg)
				pending.ReplaceExpired = true
				pending.PreviousAgentID = oldReg.AgentID
				pending.PreviousKey = oldKey
				if err := journal.Save(pending); err != nil {
					fmt.Fprintln(stderr, "jevlin: persist pending registration:", err)
					return exitTransport
				}
				reg, err = publishPendingRegistration(store, cfg.Search, pending, registrationPublicationOptions{
					force:                *force,
					replacingExpired:     true,
					allowCorruptPreserve: true,
					previousPlatformKey:  oldKey,
					binding:              pendingBinding(pending),
				})
				if err != nil {
					fmt.Fprintln(stderr, "jevlin: publish replacement registration:", err)
					return exitTransport
				}
				if err := journal.Clear(); err != nil {
					fmt.Fprintln(stderr, "jevlin: clear pending registration:", err)
					return exitTransport
				}
				existed = true
				interval = fresh.PollInterval
			}
		}

		if !existed {
			if err := preflightFreshRegistration(cfg.Search, *force); err != nil {
				fmt.Fprintln(stderr, "jevlin:", err)
				return exitTransport
			}
			// No scope is requested: the claim page decides what the agent
			// is granted, and this client asks for nothing it would act on.
			fresh, err := client.Register(ctx, *name, clientIdentifier(), nil)
			if err != nil {
				fmt.Fprintln(stderr, "jevlin: register:", err)
				return exitTransport
			}
			pending = pendingRegistrationFromPlatform(fresh, cfg)
			if err := journal.Save(pending); err != nil {
				fmt.Fprintln(stderr, "jevlin: persist pending registration:", err)
				return exitTransport
			}
			reg, err = publishPendingRegistration(store, cfg.Search, pending, registrationPublicationOptions{
				force:                *force,
				allowCorruptPreserve: true,
				binding:              pendingBinding(pending),
			})
			if err != nil {
				fmt.Fprintln(stderr, "jevlin: publish registration:", err)
				return exitTransport
			}
			if err := journal.Clear(); err != nil {
				fmt.Fprintln(stderr, "jevlin: clear pending registration:", err)
				return exitTransport
			}
			if !*resume {
				interval = fresh.PollInterval
			}
		}
	}

	// Platform calls use the stored agent credential; the search environment
	// override does not apply to this installation's registration.
	key, err = platformKey(cfg, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitTransport
	}
	if !*resume {
		// A key an older client stored records no origins; it is bound to
		// the installation's config until they are written down here.
		if err := recordKeyBinding(cfg.Search, cfgSource, getenv); err != nil {
			fmt.Fprintln(stderr, "jevlin: could not record where the stored key may be sent:", err)
		}
	}

	// WP2-adversarial-review finding 20: the claim URL/code are a
	// one-time bootstrap artifact, not something to keep echoing back —
	// once reg.Status has ever reached "claimed" they are cleared from
	// the stored record (pollOnce does the clearing) and this print is
	// skipped from then on.
	if !*resume && reg.Status == "unclaimed" {
		// B.3: a registration recovered without a claim link is durable —
		// this never falls back into printing the bare (empty) URL, on
		// this run or any later one, until the platform reports claimed
		// or expired, -force replaces it, or the mint below (B.4) hands
		// this foreground run a fresh link.
		// A stored link that fails invariant 12 (agent.json is sandbox-
		// writable) is handled exactly like a missing one: re-minted, which
		// also overwrites the bad copy on disk, or not printed at all.
		claim, _ := claimFor(cfg.Search, reg.AgentID, stderr)
		claimURL, claimCode := printableClaimURL(claim.ClaimURL, cfg.Platform.BaseURL, stderr), claim.ClaimCode
		if claimURL == "" {
			if fresh, ok := remintClaimLink(ctx, client, store, cfg.Search, &reg, key, stderr); ok {
				claimURL, claimCode = fresh.ClaimURL, fresh.ClaimCode
			}
		}
		if claimURL == "" {
			fmt.Fprintln(stdout, unclaimedNoLinkMessage)
		} else {
			fmt.Fprintln(stdout, claimBlock(claimURL, claimCode, cfg.Platform.BaseURL))
		}
	}

	if *resume {
		callCtx, callCancel := context.WithTimeout(ctx, roundTrip)
		_, code := pollOnce(callCtx, stdout, stderr, client, store, &reg, key)
		callCancel()
		return code
	}

	deadline := time.Now().Add(connectPollBudget)
	for {
		callCtx, callCancel := context.WithTimeout(ctx, roundTrip)
		done, code := pollOnce(callCtx, stdout, stderr, client, store, &reg, key)
		callCancel()
		if done {
			return code
		}
		if time.Now().After(deadline) {
			// B.3: this must never say "the URL above" when nothing was
			// ever printed above it. pollOnce takes reg by pointer and
			// re-reads the store on every call (see its own doc comment),
			// so this local reg is already current — a registration
			// recovered without a claim link stays durable through the
			// timeout narration too, not just the initial print.
			if claim, _ := claimFor(cfg.Search, reg.AgentID, nil); reg.Status == "unclaimed" && printableClaimURL(claim.ClaimURL, cfg.Platform.BaseURL, nil) == "" {
				fmt.Fprintln(stdout, "\n"+unclaimedNoLinkMessage)
			} else {
				fmt.Fprintln(stdout, "\n"+claimTimeoutMessage)
			}
			return exitOK
		}
		// WP2-adversarial-review finding 11: the platform's advertised
		// interval is already floor-and-ceiling clamped (pkg/platform),
		// but nothing previously stopped it from parking THIS loop past
		// its own foreground budget — a ceiling on the interval alone
		// does not bound the sleep if the remaining budget is shorter
		// than even the clamped interval. Sleep for whichever is
		// smaller.
		sleep := interval
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		if sleep < 0 {
			sleep = 0
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "jevlin: connect: interrupted")
			return exitTransport
		case <-time.After(sleep):
		}
	}
}

// pollOnce checks status once and acts on it: it advances the stored
// registration and says what the platform says. Shared by the foreground
// loop and the detached resume, which calls this exactly once
// (TestDetachedResumePollsOnceAndExits).
//
// done=true means there is nothing further for THIS process to do:
// claimed, or expired. done=false means "keep polling" (still unclaimed,
// or a transient error worth retrying in the foreground — a resume does not
// retry, it simply exits and lets the next resume try again).
func pollOnce(ctx context.Context, stdout, stderr io.Writer, client *platform.Client, store *auth.Store, reg *auth.AgentRegistration, key string) (done bool, code int) {
	// Re-read the on-disk record before mutating it — reg may be stale
	// relative to whatever the most recent write actually persisted (this
	// function is called repeatedly across a foreground loop's iterations,
	// and separately by every detached resume's own single call;
	// connect.lock keeps those from interleaving, but nothing here should
	// assume that as the ONLY reason the in-memory copy might be stale).
	if fresh, ok, ferr := store.LoadAgentRegistration(); ferr == nil && ok && fresh.AgentID == reg.AgentID {
		*reg = fresh
	}

	st, err := client.Status(ctx, reg.AgentID, key)
	if errors.Is(err, platform.ErrAgentNotFound) {
		fmt.Fprintln(stderr, "jevlin: this agent is no longer known to the platform (revoked, or lost server-side)")
		return true, exitTransport
	}
	if err != nil {
		fmt.Fprintln(stderr, "jevlin: status:", err)
		return false, exitTransport
	}

	reg.Status = st.Status
	reg.Scopes = st.Scopes
	reg.ClaimExpiresAt = st.ClaimExpiresAt
	if serr := store.SaveAgentRegistration(*reg); serr != nil {
		// Fail loudly. Continuing as if this succeeded would mean the next
		// call re-derives status from a stale copy again.
		fmt.Fprintln(stderr, "jevlin:", serr)
		return true, exitTransport
	}

	switch st.Status {
	case "expired":
		fmt.Fprintln(stdout, "this registration expired before being claimed; run `jevlin connect` again for a new one")
		return true, exitOK
	case "unclaimed":
		return false, exitOK
	}
	// claimed: search was already working on the stored key, and the claim
	// has tied the agent to an account. Nothing further for connect to do.
	fmt.Fprintf(stdout, "claimed (scopes: %s)\n", strings.Join(st.Scopes, ", "))
	return true, exitOK
}

// ── detached resume ─────────────────────────────────────────────────────

// shouldResume reports whether a stored registration is worth spawning a
// detached connect -resume for. A pure disk read, no network — search.go
// calls this after every served search.
//
// The spawn must happen only while a resume can actually do something:
// an unclaimed registration, whose claim a person may have approved since.
// A claimed or expired one is settled, and a resume for it would poll the
// platform and exit, forever, on every single search. Paced by
// resumeCooldown so a burst of searches cannot approve more spawns than
// connect.lock could ever let run concurrently anyway. A registration
// record that fails to load (corrupt, empty) resumes nothing; recovery is
// a manual connect, not a silent background retry.
//
// Deliberately NOT auth.OpenStore(stateDir) first: that creates the
// state directory (MkdirAll) if it does not exist, which would give
// every plain search a filesystem side effect on a machine that has
// never run connect and never will. The os.Stat below is read-only and
// costs nothing when there is nothing to resume — the overwhelmingly
// common case.
func shouldResume(cfg *config.Config, getenv func(string) string) bool {
	stateDir := cfg.Search.StateDir
	if stateDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent.json")); err != nil {
		return false
	}
	if st := readResumeStamp(resumeStampPath(stateDir)); time.Since(st.LastAttempt) < resumeCooldown {
		return false
	}
	store, err := auth.OpenStoreExisting(stateDir)
	if err != nil {
		return false
	}
	reg, ok, err := store.LoadAgentRegistration()
	if err != nil || !ok {
		return false // corrupt or absent — nothing to resume
	}
	return reg.Status == "unclaimed"
}

// startConnectResume launches `connect -resume` as a detached child:
// best effort, output goes nowhere, the next search or resume tries again
// if this one could not even start.
func startConnectResume(cfgPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"connect", "-resume"}
	if cfgPath != "" {
		args = append(args, "-config", cfgPath)
	}
	return spawnDetached(exe, args)
}
