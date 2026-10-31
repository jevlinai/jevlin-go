package main

// Integration tests for connect (agent onboarding design,
// search-platform-agent-onboarding-design.md §5.5, §8 WP2 acceptance):
// the §2.2 cases a search-only client still has, idempotency, search
// working before the claim, status reporting, the detached resume doing
// exactly one poll, the rebuild of a lost registration from the platform,
// the replacement of an expired one, and the registration journal.
//
// One stub stands in for the platform: stubPlatform (register, status,
// me, claim-code — the §5.1-§5.2 contract plus the two routes the router
// team added for the rebuild and the re-mint). Nothing here reaches a
// real host; TestMain's network fence refuses it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
	platformapi "github.com/jevlinai/jevlin-go/pkg/platform"
)

// ── stubs ──────────────────────────────────────────────────────────────

type stubPlatform struct {
	srv *httptest.Server

	mu     sync.Mutex
	status string // "unclaimed" | "claimed" | "expired"
	scopes []string

	registerCalls          int
	statusCalls            int
	meCalls                int
	dropNextRegisterBodies int
	statusNotFound         bool
	statusError            bool
	// statusRequiresOwner makes GET /v1/agents/{id} answer 404 unless the
	// bearer key is the one this stub minted for that id, as the real
	// router does: it resolves the key to its own agent first.
	statusRequiresOwner bool
	statusByAgent       map[string]string
	scopesByAgent       map[string][]string
	agentByKey          map[string]string // bearer key -> agent id, for /v1/agents/me
	claimCodeByAgent    map[string]string
	meError             bool // every /v1/agents/me call answers 500
	meOmitClaimFields   bool // /v1/agents/me never sends claim_url/claim_code, modeling B.1's known gap
	claimCodeCalls      int  // POST /v1/agents/{id}/claim-code attempts, succeeded or not
	claimCodeMints      int  // fresh codes actually minted
	claimCodeDisabled   bool // the route answers 404, modeling a platform without it
	// meHook, when set, runs synchronously right before a successful
	// /v1/agents/me answer is written — a deterministic seam for timing a
	// filesystem side effect (review correction §4) exactly between "the
	// platform answered" and "the client persists what it learned",
	// without a POSIX permission-bit assumption that behaves differently
	// under a root test runner. Runs on the httptest handler's own
	// goroutine: must not call testing.T methods directly.
	meHook func()
	// registerAgentID, when set, is the agent_id every register answers
	// with, so a test can send one the client must refuse.
	registerAgentID string
}

func newStubPlatform(t *testing.T) *stubPlatform {
	t.Helper()
	f := &stubPlatform{
		status:           "unclaimed",
		statusByAgent:    make(map[string]string),
		scopesByAgent:    make(map[string][]string),
		agentByKey:       make(map[string]string),
		claimCodeByAgent: make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agents/register", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.registerCalls++
		registerNumber := f.registerCalls
		agentID := fmt.Sprintf("agent-%d", registerNumber)
		if f.registerAgentID != "" {
			agentID = f.registerAgentID
		}
		key := "sr-stubkey"
		claimCode := "AB12-CD34"
		if registerNumber > 1 {
			key = fmt.Sprintf("sr-stubkey-%d", registerNumber)
			claimCode = fmt.Sprintf("EF56-GH%02d", registerNumber)
		}
		registrationStatus := f.status
		registrationScopes := append([]string(nil), f.scopes...)
		if registrationStatus == "expired" {
			// An expired identity's replacement starts a fresh claim. A
			// test that forced "expired" before the first registration still
			// gets a registration that starts unclaimed.
			registrationStatus = "unclaimed"
			registrationScopes = nil
		}
		f.statusByAgent[agentID] = registrationStatus
		f.scopesByAgent[agentID] = registrationScopes
		f.agentByKey[key] = agentID
		f.claimCodeByAgent[agentID] = claimCode
		drop := f.dropNextRegisterBodies > 0
		if drop {
			f.dropNextRegisterBodies--
		}
		f.mu.Unlock()
		if drop {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		writeStubJSON(w, http.StatusCreated, map[string]any{
			"agent_id": agentID, "key": key,
			"claim_url": f.srv.URL + "/claim/" + claimCode, "claim_code": claimCode,
			"claim_expires_at": "2026-09-16T00:00:00Z",
			"poll":             map[string]any{"interval_s": 1},
			"tier":             "unclaimed",
		})
	})
	mux.HandleFunc("GET /v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.statusCalls++
		id := strings.TrimPrefix(r.URL.Path, "/v1/agents/")
		st, scopes := f.status, f.scopes
		notFound := f.statusNotFound
		if f.statusRequiresOwner {
			owner, known := f.agentByKey[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			notFound = notFound || !known || owner != id
		}
		statusError := f.statusError
		if agentStatus, ok := f.statusByAgent[id]; ok {
			st = agentStatus
		}
		if agentScopes, ok := f.scopesByAgent[id]; ok {
			scopes = agentScopes
		}
		f.mu.Unlock()
		if notFound {
			writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
			return
		}
		if statusError {
			writeStubJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "temporary"}})
			return
		}
		writeStubJSON(w, http.StatusOK, map[string]any{"status": st, "scopes": scopes})
	})
	mux.HandleFunc("POST /v1/agents/{id}/claim-code", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.claimCodeCalls++
		disabled := f.claimCodeDisabled
		id := r.PathValue("id")
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		owner, known := f.agentByKey[key]
		ok := !disabled && known && owner == id && f.statusByAgent[id] == "unclaimed"
		var fresh string
		if ok {
			f.claimCodeMints++
			fresh = fmt.Sprintf("MINT-%02d", f.claimCodeMints)
			f.claimCodeByAgent[id] = fresh
		}
		f.mu.Unlock()
		if !ok {
			// A platform without the route, a claimed agent and an unknown
			// key all answer the same no-oracle 404 the real one gives.
			writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
			return
		}
		writeStubJSON(w, http.StatusOK, map[string]any{
			"claim_url":        f.srv.URL + "/claim/" + fresh,
			"claim_code":       fresh,
			"claim_expires_at": "2026-12-31T00:00:00Z",
		})
	})
	// GET /v1/agents/me (B.2): a by-key self-lookup, keyed on the bearer
	// the register handler above minted for that agent. 404 for a key
	// this stub never issued (or one it does not recognize as current —
	// modeling a revoked key, which the design gives the same answer as
	// an unknown one).
	mux.HandleFunc("GET /v1/agents/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.meCalls++
		meError := f.meError
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, known := f.agentByKey[key]
		var st string
		var scopes []string
		var claimCode string
		if known {
			st = f.statusByAgent[id]
			scopes = f.scopesByAgent[id]
			claimCode = f.claimCodeByAgent[id]
		}
		omitClaim := f.meOmitClaimFields
		hook := f.meHook
		f.mu.Unlock()
		if meError {
			writeStubJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "internal"}})
			return
		}
		if !known {
			writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
			return
		}
		body := map[string]any{
			"agent_id": id,
			"status":   st, "scopes": scopes,
		}
		if st == "unclaimed" && !omitClaim {
			body["claim_url"] = f.srv.URL + "/claim/" + claimCode
			body["claim_code"] = claimCode
		}
		if hook != nil {
			hook()
		}
		writeStubJSON(w, http.StatusOK, body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *stubPlatform) claim(scopes ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = "claimed"
	f.scopes = scopes
	for id := range f.statusByAgent {
		f.statusByAgent[id] = "claimed"
		f.scopesByAgent[id] = append([]string(nil), scopes...)
	}
}

// setStatus forces a status the ordinary lifecycle (claim) doesn't reach
// on its own — namely "expired".
func (f *stubPlatform) setStatus(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	for id := range f.statusByAgent {
		f.statusByAgent[id] = status
	}
}

func (f *stubPlatform) setStatusNotFound(notFound bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusNotFound = notFound
}

func (f *stubPlatform) setStatusRequiresOwner(requires bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusRequiresOwner = requires
}

func (f *stubPlatform) setStatusError(statusError bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusError = statusError
}

// counts is how often each of the stub's routes a connect run may call
// was called: register, the status poll, and the by-key self-lookup
// /v1/agents/me (also meCallCount, for a test that asks only that).
func (f *stubPlatform) counts() (register, status, me int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registerCalls, f.statusCalls, f.meCalls
}

func (f *stubPlatform) meCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meCalls
}

// setMeError makes every /v1/agents/me call answer 500, modeling a
// transient platform failure during a rebuild attempt.
func (f *stubPlatform) setMeError(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meError = fail
}

// setMeOmitsClaimFields models B.1's known gap: /v1/agents/me answering
// for a still-unclaimed agent without claim_url/claim_code.
func (f *stubPlatform) setMeOmitsClaimFields(omit bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meOmitClaimFields = omit
}

// setClaimCodeRouteDisabled models a platform without the claim-code
// re-mint route: every POST /v1/agents/{id}/claim-code answers 404.
func (f *stubPlatform) setClaimCodeRouteDisabled(disabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCodeDisabled = disabled
}

func (f *stubPlatform) claimCodeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimCodeCalls
}

// setMeHook installs (or, with nil, removes) the synchronous hook run
// right before a successful /v1/agents/me answer is written. See the
// field's own doc comment: it executes on the handler's goroutine, so it
// must not call testing.T methods directly.
func (f *stubPlatform) setMeHook(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meHook = hook
}

func (f *stubPlatform) dropNextRegisterResponse() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropNextRegisterBodies++
}

func writeStubJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ── config helper ─────────────────────────────────────────────────────

// withShortConnectTimings shrinks the foreground poll's budget and
// interval for the duration of a test, so a case that legitimately never
// gets claimed (or claims after a couple of iterations) does not wait
// out the real 3-minute bound. Restored via t.Cleanup regardless of how
// the test ends.
func withShortConnectTimings(t *testing.T) {
	t.Helper()
	origBudget, origInterval := connectPollBudget, resumePollInterval
	connectPollBudget = 300 * time.Millisecond
	resumePollInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		connectPollBudget = origBudget
		resumePollInterval = origInterval
	})
}

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jevlin.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { // #nosec G703 -- fixed filename in the test's own t.TempDir()
		t.Fatal(err)
	}
	return path
}

// connectConfig builds a format-1 config for connect: [platform] and
// [agents] pointed at platformURL, and a [search] table whose state_dir and
// sessions_dir sit under the test's own directory, because the default
// (os.UserConfigDir()/jevlin/state) is not test-isolated and the
// installation's own files — credentials.json, the registration journal,
// the claim record — live in state_dir's parent. routerURL, when not
// empty, is written as [router] url, for a test that also searches; ""
// leaves the router at the compiled default, which nothing here dials.
func connectConfig(t *testing.T, platformURL, routerURL string) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	var b strings.Builder
	fmt.Fprintf(&b, "schema = 1\n\n[platform]\nurl = %q\n\n[agents]\nurl = %q\n", platformURL, platformURL)
	if routerURL != "" {
		fmt.Fprintf(&b, "\n[router]\nurl = %q\n", routerURL)
	}
	fmt.Fprintf(&b, "\n[search]\nstate_dir = %q\nsessions_dir = %q\n", stateDir, filepath.Join(dir, "sessions"))
	return writeTOML(t, b.String()), stateDir
}

// testJournal is the registration journal connect uses for cfgPath: beside
// credentials.json, not in the state directory.
func testJournal(t *testing.T, cfgPath string) *auth.RegistrationJournal {
	t.Helper()
	j, err := registrationJournal(mustLoadConfig(t, cfgPath).Search)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// seedClaim writes the claim record connect keeps beside credentials.json,
// as a publication, a rebuild or a re-mint would have.
func seedClaim(t *testing.T, cfgPath, agentID, claimURL, claimCode string) {
	t.Helper()
	if err := saveClaim(mustLoadConfig(t, cfgPath).Search, auth.ClaimBootstrap{AgentID: agentID, ClaimURL: claimURL, ClaimCode: claimCode}); err != nil {
		t.Fatal(err)
	}
}

// storedClaim is the claim record on file for agentID, zero when none is.
func storedClaim(t *testing.T, cfgPath, agentID string) auth.ClaimBootstrap {
	t.Helper()
	claim, _ := claimFor(mustLoadConfig(t, cfgPath).Search, agentID, nil)
	return claim
}

func testJournalPath(t *testing.T, cfgPath string) string {
	t.Helper()
	return filepath.Join(installRoot(mustLoadConfig(t, cfgPath).Search), registrationJournalFile)
}

func mustLoadConfig(t *testing.T, cfgPath string) *config.Config {
	t.Helper()
	cfg, _, err := loadConfig(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runConnect(t *testing.T, cfgPath string, stdin *bytes.Buffer, extra ...string) (code int, stdout, stderr string) {
	t.Helper()
	if stdin == nil {
		stdin = &bytes.Buffer{}
	}
	var out, errOut bytes.Buffer
	args := append([]string{"-config", cfgPath}, extra...)
	code = cmdConnect(args, stdin, &out, &errOut, installedAs(noEnv, args))
	return code, out.String(), errOut.String()
}

func loadAgent(t *testing.T, stateDir string) (auth.AgentRegistration, bool) {
	t.Helper()
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reg, ok, err := store.LoadAgentRegistration()
	if err != nil {
		t.Fatal(err)
	}
	return reg, ok
}

// ── §2.2 cases ────────────────────────────────────────────────────────

// Case: no account, agent registers first. search works unclaimed at
// once (proven by a separate test); the one visit later claims it.
func TestConnectCaseNoAccountRegistersFirst(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")

	code, out, _ := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d", code)
	}
	if !strings.Contains(out, "AB12-CD34") || !strings.Contains(out, platform.srv.URL) {
		t.Fatalf("claim URL/code not printed: %q", out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-1" || reg.Status != "unclaimed" {
		t.Fatalf("got %+v ok=%v", reg, ok)
	}
	if creds, err := os.ReadFile(filepath.Join(filepath.Dir(stateDir), "credentials.json")); err != nil || !strings.Contains(string(creds), "sr-stubkey") {
		t.Fatalf("credentials.json not written with the platform key: %v %q", err, creds)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("register called %d times, want 1", registerCalls)
	}
}

// Case: account exists — the claim grants the scopes the claim page chose,
// and the stored registration carries exactly those.
func TestConnectCaseAccountExistsSearchOnly(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatal("first connect failed")
	}
	platform.claim("credits")

	// First persist the claimed state through the detached poll path.
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("claimed resume exited %d: %s", code, errOut)
	}
	// A later foreground connect picks up the stored claimed registration
	// and polls rather than re-registering.
	code, out, _ := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("resumed connect exited %d: %s", code, out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.Status != "claimed" || !slices.Equal(reg.Scopes, []string{"credits"}) {
		t.Fatalf("got %+v ok=%v", reg, ok)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("register called %d times across two runs, want 1 (resumable)", registerCalls)
	}
}

// ── idempotency, search-before-claim, status ────────────────────────

func TestConnectIsIdempotentOnSecondRun(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	for i := 0; i < 3; i++ {
		if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
			t.Fatalf("run %d failed", i)
		}
	}
	if registerCalls, statusCalls, _ := platform.counts(); registerCalls != 1 || statusCalls < 3 {
		t.Fatalf("register=%d status=%d, want register=1 status>=3", registerCalls, statusCalls)
	}
}

// search works from the moment connect stores the key, before any
// claim — the property that makes the unclaimed tier useful at all.
func TestConnectStoresKeyAndSearchWorksBeforeClaim(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sr-stubkey" {
			t.Errorf("search sent Authorization %q, want the key connect stored", got)
		}
		writeStubJSON(w, http.StatusOK, map[string]any{"request_id": "req-1"})
	}))
	defer router.Close()
	// One config for both: connect records the router it names as where
	// the stored key may go, and search sends the key there.
	cfgPath, _ := connectConfig(t, platform.srv.URL, router.URL)
	if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatal("connect failed")
	}

	hook := realHookOps()
	hook.getenv = noEnv
	var out, errOut bytes.Buffer
	code := searchMain(searchOps{
		getppid: func() int { return 1 }, hostname: func() (string, error) { return "h", nil },
		getwd: func() (string, error) { return t.TempDir(), nil }, now: time.Now, hook: hook,
	}, []string{"-config", cfgPath, "hello"}, strings.NewReader(""), &out, &errOut, installedAs(noEnv, []string{"-config", cfgPath}))
	if code != exitOK {
		t.Fatalf("search exited %d: %s", code, errOut.String())
	}
}

func TestStatusReportsAgentIdentity(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatal("connect failed")
	}
	if out := statusText(t, cfgPath); !strings.Contains(out, "unclaimed") {
		t.Fatalf("status did not report unclaimed: %q", out)
	}

	platform.claim("credits")
	if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatal("resumed connect failed")
	}
	if out := statusText(t, cfgPath); !strings.Contains(out, "claimed") || !strings.Contains(out, "credits") {
		t.Fatalf("status did not report the claim and its scopes: %q", out)
	}
}

// ── detached resume ──────────────────────────────────────────────────

func TestDetachedResumePollsOnceAndExits(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, _ := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatal("first connect failed")
	}
	_, before, _ := platform.counts()

	code, _, _ := runConnect(t, cfgPath, nil, "-resume")
	if code != exitOK {
		t.Fatalf("resume exited %d", code)
	}
	_, after, _ := platform.counts()
	if after-before != 1 {
		t.Fatalf("resume made %d status calls, want exactly 1", after-before)
	}
	if _, ok := loadAgent(t, stateDir); !ok {
		t.Fatal("resume did not read the stored registration")
	}
}

func TestShouldResumeIsALocalCheckWithNoStoredRegistration(t *testing.T) {
	if shouldResume(&config.Config{Search: config.Search{StateDir: filepath.Join(t.TempDir(), "state")}}, noEnv) {
		t.Fatal("shouldResume true with nothing on disk")
	}
	if shouldResume(&config.Config{}, noEnv) {
		t.Fatal("shouldResume true with an empty state dir")
	}
}

// shouldResumeFixture stores a registration in a fresh state directory and
// lets each subtest vary exactly one thing about it.
func shouldResumeFixture(t *testing.T, status string, scopes []string) (stateDir string) {
	t.Helper()
	stateDir = filepath.Join(t.TempDir(), "state")
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-1", Status: status, Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

// WP2-review defect 3 / coverage gap A: the spawn must stop once there is
// nothing a resume can do, and — the reviewer's specific finding — every
// condition that decides that needs a test that fails if THAT condition
// alone is deleted, not just a return value it happens to share with a
// neighbor. The one state a resume can still act on is an unclaimed
// registration, whose claim a person may have approved since; a claimed or
// expired one is settled, and a resume for it would poll the platform and
// exit, forever, on every single search.
func TestShouldResumeOnlyForAnUnclaimedRegistration(t *testing.T) {
	t.Run("unclaimed: still actionable", func(t *testing.T) {
		stateDir := shouldResumeFixture(t, "unclaimed", nil)
		if !shouldResume(&config.Config{Search: config.Search{StateDir: stateDir}}, noEnv) {
			t.Fatal("shouldResume false for an unclaimed registration still worth polling")
		}
	})

	t.Run("claimed: settled", func(t *testing.T) {
		stateDir := shouldResumeFixture(t, "claimed", []string{"credits"})
		if shouldResume(&config.Config{Search: config.Search{StateDir: stateDir}}, noEnv) {
			t.Fatal("shouldResume true for a claimed registration")
		}
	})

	t.Run("expired: settled", func(t *testing.T) {
		stateDir := shouldResumeFixture(t, "expired", nil)
		if shouldResume(&config.Config{Search: config.Search{StateDir: stateDir}}, noEnv) {
			t.Fatal("shouldResume true for an expired registration")
		}
	})
}

// ── WP2-adversarial-review coverage ─────────────────────────────────────

// WP2-adversarial-review finding 14: an existing credentials.json that
// already looks like a platform key must not be silently replaced by a
// fresh registration's key.
func TestConnectRefusesToOverwriteAnExistingCredentialsFileUnlessForced(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg, _, err := loadConfig(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	credPath := credentialsPath(cfg.Search)
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credPath, credentials{APIKey: "sr-existing-real-key"}); err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code == exitOK {
		t.Fatalf("connect overwrote an existing credentials.json without -force; stderr=%q", errOut)
	}
	if raw, err := os.ReadFile(credPath); err != nil || !strings.Contains(string(raw), "sr-existing-real-key") { // #nosec G304 -- the test's own credentialsPath output, not an external path
		t.Fatalf("credentials.json was modified despite the refusal: %v %q", err, raw)
	}
	if _, ok := loadAgent(t, stateDir); ok {
		t.Fatal("a registration was persisted despite the refusal")
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("Register calls = %d, want 0", registerCalls)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil, "-force"); code != exitOK {
		t.Fatalf("connect -force still refused: %d %s", code, errOut)
	}
	if raw, err := os.ReadFile(credPath); err != nil || !strings.Contains(string(raw), "sr-stubkey") { // #nosec G304 -- the test's own credentialsPath output, not an external path
		t.Fatalf("-force did not overwrite: %v %q", err, raw)
	}
}

// WP2-adversarial-review finding 16: status is not a failure for the two
// most ordinary agent-onboarding states.
func TestStatusExitsZeroForUnclaimedAndSearchOnly(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("connect failed: %s", errOut)
	}
	if code := statusMain([]string{"-config", cfgPath}, &bytes.Buffer{}, &bytes.Buffer{}, noEnv); code != exitOK {
		t.Fatalf("status exited %d for an unclaimed registration, want %d", code, exitOK)
	}

	platform.claim("credits")
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("resume failed: %s", errOut)
	}
	if code := statusMain([]string{"-config", cfgPath}, &bytes.Buffer{}, &bytes.Buffer{}, noEnv); code != exitOK {
		t.Fatalf("status exited %d for a claimed registration, want %d", code, exitOK)
	}
}

// A corrupt registration with no platform credential can proceed, but the
// unreadable evidence must be preserved rather than silently overwritten.
func TestConnectPreservesCorruptRegistrationWithoutPlatformCredential(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	corrupt := []byte("{not json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect did not recover with no platform credential: exit %d, stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "could not be decoded") {
		t.Fatalf("no acknowledgment of the corrupt record on stderr: %q", errOut)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-1" || reg.Status != "unclaimed" {
		t.Fatalf("did not persist the fresh registration: %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir, "agent.json.corrupt")); err != nil || string(got) != string(corrupt) { // #nosec G304 -- test controls its temporary state directory
		t.Fatalf("corrupt registration evidence was not preserved: err=%v contents=%q", err, got)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("Register calls = %d, want 1", registerCalls)
	}
}

// A corrupt registration beside a platform credential the platform does not
// recognize (revoked, or never actually registered — /v1/agents/me answers
// 404 either way, §5.2's deliberate no-oracle answer) is refused exactly as
// a plain fresh-registration attempt would refuse it, naming -force. B.3
// added the /me rebuild attempt in between, but a 404 leaves this test's
// outcome unchanged: still zero Register calls, still the credential
// conflict message, still the corrupt bytes and the existing credential
// both untouched until -force authorizes a replacement.
func TestConnectRefusesCorruptRegistrationWithExistingPlatformCredential(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg, _, err := loadConfig(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{not json")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	credPath := credentialsPath(cfg.Search)
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credPath, credentials{APIKey: "sr-existing-key"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK {
		t.Fatalf("connect succeeded despite corrupt registration and existing credential")
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("Register calls = %d, want 0", registerCalls)
	}
	if platform.meCallCount() != 1 {
		t.Fatalf("/v1/agents/me calls = %d, want exactly 1 (the rebuild attempt, which came back 404)", platform.meCallCount())
	}
	if !strings.Contains(errOut, "credentials.json already holds a platform key") {
		t.Fatalf("missing credential-conflict diagnostic: %q", errOut)
	}
	if !strings.Contains(errOut, "-force") {
		t.Fatalf("missing -force guidance: %q", errOut)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir, "agent.json")); err != nil || string(got) != string(corrupt) { // #nosec G304 -- test controls its temporary state directory
		t.Fatalf("corrupt registration was modified: err=%v contents=%q", err, got)
	}
	if got, err := os.ReadFile(credPath); err != nil || !strings.Contains(string(got), "sr-existing-key") { // #nosec G304 -- test controls its temporary credential path
		t.Fatalf("existing credential was modified: err=%v contents=%q", err, got)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil, "-force"); code != exitOK {
		t.Fatalf("connect -force did not authorize replacement: code=%d stderr=%s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("Register calls after -force = %d, want 1", registerCalls)
	}
	if platform.meCallCount() != 1 {
		t.Fatalf("/v1/agents/me calls after -force = %d, want still exactly 1 (-force bypasses the rebuild entirely)", platform.meCallCount())
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-1" {
		t.Fatalf("fresh registration was not persisted after -force: %+v ok=%v", reg, ok)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent.json.corrupt")); err != nil {
		t.Fatalf("corrupt registration evidence was not preserved after -force: %v", err)
	}
}

// ── B.3: rebuilding a lost or unreadable registration from the platform ──

// registerAgent drives a real Register call against the stub, independent
// of connectRun, so a rebuild test can start from a credential the
// platform genuinely recognizes (agentByKey/statusByAgent both populated)
// rather than one hand-inserted into the stub's maps.
func registerAgent(t *testing.T, platform *stubPlatform) (agentID, key string) {
	t.Helper()
	c := platformapi.New(platform.srv.URL, platform.srv.URL)
	reg, err := c.Register(context.Background(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg.AgentID, reg.Key
}

// setupLostRegistration writes key to credentials.json and, when corrupt
// is true, an undecodable agent.json beside it (returning those bytes for
// the caller to assert against later); when corrupt is false, agent.json
// is simply absent. Either way this models "a platform credential is
// stored, but the local registration is gone."
func setupLostRegistration(t *testing.T, cfg *config.Config, key string, corrupt bool) []byte {
	t.Helper()
	stateDir := cfg.Search.StateDir
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credPath := credentialsPath(cfg.Search)
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credPath, credentials{APIKey: key}); err != nil {
		t.Fatal(err)
	}
	if !corrupt {
		return nil
	}
	corruptBytes := []byte("{not json")
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return corruptBytes
}

// A claimed identity, rebuilt from /v1/agents/me, continues straight into
// the ordinary claimed flow — the platform's scopes are stored and reported,
// no Register — and (for the corrupt case) the undecodable bytes are
// preserved only after /me actually answered, never before.
func TestConnectRebuildsClaimedRegistrationFromThePlatform(t *testing.T) {
	for _, corrupt := range []bool{true, false} {
		corrupt := corrupt
		name := "absent"
		if corrupt {
			name = "corrupt"
		}
		t.Run(name, func(t *testing.T) {
			withShortConnectTimings(t)
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			cfg := mustLoadConfig(t, cfgPath)

			agentID, key := registerAgent(t, platform)
			platform.claim("credits")
			setupLostRegistration(t, cfg, key, corrupt)
			corruptBytes, _ := os.ReadFile(filepath.Join(cfg.Search.StateDir, "agent.json")) // #nosec G304 -- test controls its temporary state directory
			registerCallsBefore, _, _ := platform.counts()

			code, out, errOut := runConnect(t, cfgPath, nil)
			if code != exitOK {
				t.Fatalf("connect did not rebuild: code=%d stderr=%s", code, errOut)
			}
			if !strings.Contains(out, "claimed (scopes: credits)") {
				t.Fatalf("did not continue into the claimed flow: stdout=%q", out)
			}
			reg, ok := loadAgent(t, stateDir)
			if !ok || reg.AgentID != agentID || reg.Status != "claimed" || !slices.Equal(reg.Scopes, []string{"credits"}) {
				t.Fatalf("rebuilt registration not as expected: %+v ok=%v", reg, ok)
			}
			if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
				t.Fatalf("register calls = %d, want unchanged from %d", registerCalls, registerCallsBefore)
			}
			if platform.meCallCount() == 0 {
				t.Fatal("expected a /v1/agents/me call")
			}
			if corrupt {
				got, err := os.ReadFile(filepath.Join(stateDir, "agent.json.corrupt")) // #nosec G304 -- test controls its temporary state directory
				if err != nil || string(got) != string(corruptBytes) {
					t.Fatalf("corrupt registration evidence was not preserved: err=%v contents=%q", err, got)
				}
			}
		})
	}
}

// An unclaimed identity whose claim bootstrap the platform still returns
// rebuilds and continues the ordinary unclaimed flow: it prints the claim
// URL/code exactly as a fresh Register's response would.
func TestConnectRebuildsUnclaimedRegistrationWithClaimLink(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	agentID, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, true)
	registerCallsBefore, _, _ := platform.counts()

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect did not rebuild: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "claim this agent:") || !strings.Contains(out, "AB12-CD34") {
		t.Fatalf("did not print the recovered claim link: stdout=%q", out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != agentID || reg.Status != "unclaimed" || storedClaim(t, cfgPath, agentID).ClaimURL == "" || storedClaim(t, cfgPath, agentID).ClaimCode != "AB12-CD34" {
		t.Fatalf("rebuilt registration not as expected: %+v ok=%v", reg, ok)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
		t.Fatalf("Register calls = %d, want unchanged from %d", registerCalls, registerCallsBefore)
	}
}

// The one documented gap (B.1): /v1/agents/me answering for a still-
// unclaimed agent without the claim bootstrap fields, on a platform that
// ALSO has no claim-code re-mint route (every mint attempt answers 404).
// Rebuild persists the identity anyway, prints the fallback (never a bare
// empty URL), exits 0 without polling, and — on any later run, including
// `status` — the durable state keeps printing the same fallback rather
// than ever falling back into the ordinary print-and-wait narration.
// Against a platform WITH the route, the fallback is replaced by a fresh
// link: TestForegroundConnectMintsAFreshClaimLinkForALostOne below.
func TestConnectRebuildsUnclaimedRegistrationWithoutClaimLink(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.setMeOmitsClaimFields(true)
	platform.setClaimCodeRouteDisabled(true)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	agentID, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, true)
	registerCallsBefore, statusCallsBefore, _ := platform.counts()

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d, stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "no claim link is on file for it") {
		t.Fatalf("missing the recovered-without-link fallback: stdout=%q", out)
	}
	if strings.Contains(out, "claim this agent:") {
		t.Fatalf("printed the ordinary print-and-wait narration despite no claim link: stdout=%q", out)
	}
	if strings.Contains(out, "  \n") {
		t.Fatalf("printed a bare empty claim link: stdout=%q", out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != agentID || reg.Status != "unclaimed" || storedClaim(t, cfgPath, agentID).ClaimURL != "" {
		t.Fatalf("rebuilt registration not as expected: %+v ok=%v", reg, ok)
	}
	if registerCalls, statusCalls, _ := platform.counts(); registerCalls != registerCallsBefore || statusCalls != statusCallsBefore {
		t.Fatalf("register calls = %d (want unchanged from %d), status(poll) calls = %d (want unchanged from %d — no poll on the rebuild run)",
			registerCalls, registerCallsBefore, statusCalls, statusCallsBefore)
	}

	// Second run: an ordinary load (not a rebuild — agent.json now decodes
	// fine) must still never enter the print-and-wait narration, including
	// the poll loop's own timeout message — a poll may still run (to
	// discover claimed/expired), but nothing it prints may assume a claim
	// URL exists.
	code, out, errOut = runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("second connect exited %d, stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "no claim link is on file for it") {
		t.Fatalf("second run dropped the recovered-without-link fallback: stdout=%q", out)
	}
	if strings.Contains(out, "claim this agent:") {
		t.Fatalf("second run printed the ordinary print-and-wait narration despite no claim link: stdout=%q", out)
	}
	if strings.Contains(out, "  \n") {
		t.Fatalf("second run printed a bare empty claim link: stdout=%q", out)
	}
	if strings.Contains(out, "URL above") {
		t.Fatalf("second run's poll-timeout narration assumed a claim URL was printed above it: stdout=%q", out)
	}

	// `status` reports the same durable state the same way — never a bare
	// "claim at " followed by nothing.
	statusOut := statusText(t, cfgPath)
	if !strings.Contains(statusOut, "no claim link is on file for it here") {
		t.Fatalf("status did not report the recovered-without-link state: %q", statusOut)
	}
	if strings.Contains(statusOut, "claim at \n") || strings.Contains(statusOut, "claim at\n") {
		t.Fatalf("status printed a bare empty claim link: %q", statusOut)
	}
}

// B.4: against a platform with the claim-code route, a foreground connect
// whose rebuilt registration is unclaimed with no claim link mints a
// fresh one — persisted before it is printed, never a second register —
// and from then on the installation behaves like any other unclaimed one.
func TestForegroundConnectMintsAFreshClaimLinkForALostOne(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.setMeOmitsClaimFields(true)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	agentID, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, true)
	registerCallsBefore, _, _ := platform.counts()

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d, stderr=%s", code, errOut)
	}
	freshURL := platform.srv.URL + "/claim/MINT-01"
	if !strings.Contains(out, "claim this agent:") || !strings.Contains(out, freshURL) {
		t.Fatalf("the fresh claim link was not printed: stdout=%q", out)
	}
	if !strings.Contains(out, "code: MINT-01") {
		t.Fatalf("the fresh claim code was not printed: stdout=%q", out)
	}
	if strings.Contains(out, "no claim link is on file") {
		t.Fatalf("printed the dead-end fallback despite a successful mint: stdout=%q", out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != agentID || reg.Status != "unclaimed" {
		t.Fatalf("rebuilt registration not as expected: %+v ok=%v", reg, ok)
	}
	if claim := storedClaim(t, cfgPath, agentID); claim.ClaimURL != freshURL || claim.ClaimCode != "MINT-01" {
		t.Fatalf("the fresh link was not persisted: %+v", claim)
	}
	if got := platform.claimCodeCallCount(); got != 1 {
		t.Fatalf("claim-code calls = %d, want exactly 1", got)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
		t.Fatalf("a mint must never become a second register: register calls %d -> %d", registerCallsBefore, registerCalls)
	}
}

// The durable no-link state self-heals on a LATER foreground run too: the
// dead end is re-tried at the print site, not only during the rebuild, so
// a platform that gains the route after the loss still hands the
// participant a working link without -force.
func TestALaterForegroundConnectMintsWhenThePlatformGainsTheRoute(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.setMeOmitsClaimFields(true)
	platform.setClaimCodeRouteDisabled(true)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	agentID, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, true)
	if code, out, errOut := runConnect(t, cfgPath, nil); code != exitOK || !strings.Contains(out, "no claim link is on file for it") {
		t.Fatalf("setup run: code=%d stdout=%q stderr=%s", code, out, errOut)
	}

	platform.setClaimCodeRouteDisabled(false)
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d, stderr=%s", code, errOut)
	}
	freshURL := platform.srv.URL + "/claim/MINT-01"
	if !strings.Contains(out, "claim this agent:") || !strings.Contains(out, freshURL) {
		t.Fatalf("the later run did not mint and print a fresh link: stdout=%q", out)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != agentID || storedClaim(t, cfgPath, agentID).ClaimURL != freshURL {
		t.Fatalf("the fresh link was not persisted: %+v ok=%v", reg, ok)
	}
}

// A stored claim URL off the configured platform origin ([platform] url
// changed since it was stored) is treated like a lost link (invariant 12) —
// never printed, and replaced on disk by a freshly minted one.
func TestForegroundConnectNeverPrintsAStoredClaimURLOffThePlatformOrigin(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("setup connect exited %d, stderr=%s", code, errOut)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.Status != "unclaimed" {
		t.Fatalf("setup registration: %+v ok=%v", reg, ok)
	}
	const evil = "https://evil.example/claim/AB12-CD34"
	seedClaim(t, cfgPath, reg.AgentID, evil, "")

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d, stderr=%s", code, errOut)
	}
	if strings.Contains(out+errOut, "evil.example") {
		t.Fatalf("connect printed the off-origin stored claim URL: stdout=%q stderr=%q", out, errOut)
	}
	freshURL := platform.srv.URL + "/claim/MINT-01"
	if !strings.Contains(out, "claim this agent:") || !strings.Contains(out, freshURL) {
		t.Fatalf("connect did not mint and print a fresh link in its place: stdout=%q", out)
	}
	if reg, _ := loadAgent(t, stateDir); storedClaim(t, cfgPath, reg.AgentID).ClaimURL != freshURL {
		t.Fatalf("the off-origin link was not replaced on disk: %+v", reg)
	}

	// With no way to mint, nothing is printed in its place.
	seedClaim(t, cfgPath, reg.AgentID, evil, "")
	platform.setClaimCodeRouteDisabled(true)
	code, out, errOut = runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d, stderr=%s", code, errOut)
	}
	if strings.Contains(out+errOut, "evil.example") || !strings.Contains(out, unclaimedNoLinkMessage) || strings.Contains(out, "URL above") {
		t.Fatalf("connect without a mint route: stdout=%q stderr=%q", out, errOut)
	}
}

// Minting kills the old code, so only a deliberate foreground connect may
// do it. A detached -resume against the same durable no-link state leaves
// the route untouched even on a platform that has it.
func TestResumeNeverMintsAClaimLink(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.setMeOmitsClaimFields(true)
	platform.setClaimCodeRouteDisabled(true)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	_, key := registerAgent(t, platform)
	setupLostRegistration(t, cfg, key, true)
	// One foreground run against the disabled route persists the durable
	// unclaimed-without-link state (the B.3 dead end) and spends exactly
	// one failed mint attempt.
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("setup connect exited %d, stderr=%s", code, errOut)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || storedClaim(t, cfgPath, reg.AgentID).ClaimURL != "" {
		t.Fatalf("setup did not leave the no-link state: %+v ok=%v", reg, ok)
	}
	attemptsAfterForeground := platform.claimCodeCallCount()

	// The platform gains the route; -resume still must not use it.
	platform.setClaimCodeRouteDisabled(false)
	if code, _, _ := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("connect -resume failed")
	}
	if got := platform.claimCodeCallCount(); got != attemptsAfterForeground {
		t.Fatalf("-resume reached the claim-code route: attempts %d -> %d", attemptsAfterForeground, got)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || storedClaim(t, cfgPath, reg.AgentID).ClaimURL != "" {
		t.Fatalf("-resume changed the stored claim state: %+v ok=%v", reg, ok)
	}
}

// An identity that comes back expired from the rebuild flows into the
// existing expired-replacement path unchanged — the same client.Status
// re-verification and Register-a-replacement flow a normally-loaded
// expired registration already takes.
func TestConnectRebuildExpiredRegistrationFlowsIntoReplacementPath(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	_, key := registerAgent(t, platform)
	platform.setStatus("expired")
	setupLostRegistration(t, cfg, key, true)
	registerCallsBefore, _, _ := platform.counts()

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect did not replace the expired rebuild: code=%d stderr=%s", code, errOut)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-2" || reg.Status != "unclaimed" {
		t.Fatalf("did not register a replacement after the expired rebuild: %+v ok=%v", reg, ok)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore+1 {
		t.Fatalf("Register calls = %d, want %d (exactly one replacement Register)", registerCalls, registerCallsBefore+1)
	}
}

// A transient /v1/agents/me failure (or a 404 for a genuinely unknown
// key — TestConnectRefusesCorruptRegistrationWithExistingPlatformCredential
// covers that case directly) refuses the same way a plain fresh-Register
// attempt would, and never renames the corrupt evidence: preserving it is
// conditioned on /me actually answering, not merely being attempted.
func TestConnectMeErrorRefusesWithoutRenamingCorruptRecord(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	_, key := registerAgent(t, platform)
	platform.setMeError(true)
	corruptBytes := setupLostRegistration(t, cfg, key, true)
	registerCallsBefore, _, _ := platform.counts()

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK {
		t.Fatal("connect succeeded despite a failing /v1/agents/me")
	}
	if !strings.Contains(errOut, "-force") {
		t.Fatalf("missing -force guidance: %q", errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
		t.Fatalf("Register calls = %d, want unchanged from %d", registerCalls, registerCallsBefore)
	}
	got, err := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- test controls its temporary state directory
	if err != nil || string(got) != string(corruptBytes) {
		t.Fatalf("corrupt registration was renamed or modified despite the refusal: err=%v contents=%q", err, got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "agent.json.corrupt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("agent.json.corrupt should not exist after a refusal: err=%v", err)
	}
}

// `-resume` never attempts the rebuild (or a fresh Register) on a lost or
// unreadable registration: it is the unattended background poll, not the
// place to originate a new registration or decide a corrupt one is dead.
func TestConnectResumeNeverRebuildsOrRegistersOnLostRegistration(t *testing.T) {
	for _, corrupt := range []bool{true, false} {
		corrupt := corrupt
		name := "absent"
		if corrupt {
			name = "corrupt"
		}
		t.Run(name, func(t *testing.T) {
			withShortConnectTimings(t)
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			cfg := mustLoadConfig(t, cfgPath)

			_, key := registerAgent(t, platform)
			setupLostRegistration(t, cfg, key, corrupt)
			registerCallsBefore, _, _ := platform.counts()

			code, _, errOut := runConnect(t, cfgPath, nil, "-resume")
			if code != exitOK {
				t.Fatalf("-resume on a lost registration exited %d, stderr=%s", code, errOut)
			}
			if platform.meCallCount() != 0 {
				t.Fatalf("/v1/agents/me calls = %d, want 0 (-resume must never rebuild)", platform.meCallCount())
			}
			if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
				t.Fatalf("Register calls = %d, want unchanged from %d (-resume must never register)", registerCalls, registerCallsBefore)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "agent.json.corrupt")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("-resume must write nothing: agent.json.corrupt err=%v", err)
			}
			// loadAgent itself fails the test on ErrAgentRegistrationCorrupt,
			// so check directly: the point here is "unchanged", not "decodes."
			store, err := auth.OpenStore(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, rerr := store.LoadAgentRegistration(); corrupt {
				if !errors.Is(rerr, auth.ErrAgentRegistrationCorrupt) {
					t.Fatalf("-resume must write nothing: agent.json is no longer corrupt: ok=%v err=%v", ok, rerr)
				}
			} else if ok {
				t.Fatal("-resume must write nothing: a registration now exists")
			}
		})
	}
}

// B.3 review correction §4: a transient local write failure right after
// /v1/agents/me answers must not corrupt state or double-mint an agent.
// The run fails loudly (non-zero, no Register), and once the obstruction
// is gone the next foreground connect's rebuild reconstructs the exact
// same identity from the platform — never a second one.
//
// The obstruction is a deterministic filesystem seam, not a POSIX
// permission-bit assumption (chmod 0444 behaves differently, or not at
// all, under a root test runner) — and, per a real Windows CI failure on
// an earlier version of this test, not "replace the state directory
// itself" either: connect.lock is held open for connectRun's entire run,
// and Windows (unlike POSIX) refuses to remove or replace a directory
// that contains an open file handle, so that approach silently failed to
// obstruct anything there. Instead this only ever ADDS a new sibling
// entry inside the state directory, never touching the directory or
// connect.lock:
//   - corrupt: moves the corrupt agent.json aside (outside the state
//     directory) and puts a directory at its name, so
//     PreserveCorruptAgentRegistration's own check (agent.json must be a
//     regular file) refuses deterministically. An existing
//     agent.json.corrupt no longer obstructs anything: it is replaced.
//   - absent: pre-creates agent.json as a directory, so
//     SaveAgentRegistration's rename-onto-that-path fails deterministically
//     on every OS (renaming a file onto an existing directory is refused
//     by both POSIX rename(2) and Windows MoveFileEx).
func TestConnectRebuildRetriesAfterATransientPersistFailure(t *testing.T) {
	for _, corrupt := range []bool{true, false} {
		corrupt := corrupt
		name := "absent"
		if corrupt {
			name = "corrupt"
		}
		t.Run(name, func(t *testing.T) {
			withShortConnectTimings(t)
			platform := newStubPlatform(t)
			cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
			cfg := mustLoadConfig(t, cfgPath)

			agentID, key := registerAgent(t, platform)
			corruptBytes := setupLostRegistration(t, cfg, key, corrupt)
			registerCallsBefore, _, _ := platform.counts()

			obstructionPath := filepath.Join(stateDir, "agent.json")
			heldAside := filepath.Join(t.TempDir(), "agent.json")

			// Armed for exactly the first successful /v1/agents/me answer.
			armed := true
			platform.setMeHook(func() {
				if !armed {
					return
				}
				armed = false
				if corrupt {
					_ = os.Rename(obstructionPath, heldAside)
				}
				_ = os.Mkdir(obstructionPath, 0o700)
			})

			code, _, errOut := runConnect(t, cfgPath, nil)
			if code == exitOK {
				t.Fatalf("connect succeeded despite an obstructed persist target: %s", errOut)
			}
			if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
				t.Fatalf("Register calls = %d, want unchanged from %d — a failed persist must never fall through to Register", registerCalls, registerCallsBefore)
			}
			if corrupt {
				got, err := os.ReadFile(heldAside) // #nosec G304 -- test controls its temporary directory
				if err != nil || string(got) != string(corruptBytes) {
					t.Fatalf("corrupt starting bytes were disturbed despite Preserve refusing: err=%v contents=%q", err, got)
				}
				if lexists(filepath.Join(stateDir, "agent.json.corrupt")) {
					t.Fatal("a refused Preserve still wrote evidence")
				}
			}

			// Remove the obstruction and, in the corrupt case, put the
			// corrupt starting bytes back where the failed run found them.
			if err := os.Remove(obstructionPath); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if err := os.Rename(heldAside, obstructionPath); err != nil {
					t.Fatal(err)
				}
			}
			platform.setMeHook(nil)

			code, _, errOut = runConnect(t, cfgPath, nil)
			if code != exitOK {
				t.Fatalf("retry after the obstruction was removed failed: %d %s", code, errOut)
			}
			if registerCalls, _, _ := platform.counts(); registerCalls != registerCallsBefore {
				t.Fatalf("the retry minted a Register call: %d, want unchanged from %d — no second agent", registerCalls, registerCallsBefore)
			}
			reg, ok := loadAgent(t, stateDir)
			if !ok || reg.AgentID != agentID {
				t.Fatalf("the retry did not reconstruct the same identity: %+v ok=%v want agent_id=%s", reg, ok, agentID)
			}
			if corrupt {
				got, err := os.ReadFile(filepath.Join(stateDir, "agent.json.corrupt")) // #nosec G304 -- test controls its temporary state directory
				if err != nil || string(got) != string(corruptBytes) {
					t.Fatalf("corrupt evidence was not preserved by the successful retry: err=%v contents=%q", err, got)
				}
			}
		})
	}
}

// A foreground connect that finds its registration expired, and the
// platform confirming it, registers a replacement: the new claim link and
// key are published, the old link is gone from every report, and the
// journal is cleared. The replacement is durable — a second run polls the
// new agent and mints nothing.
func TestForegroundConnectReplacesExpiredRegistrationAndPropagatesNewIdentity(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("initial connect failed: %s", errOut)
	}
	oldURL := platform.srv.URL + "/claim/AB12-CD34"
	platform.setStatus("expired")

	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("expired replacement failed: code=%d stdout=%s stderr=%s", code, out, errOut)
	}
	newURL := platform.srv.URL + "/claim/EF56-GH02"
	if !strings.Contains(out, newURL) || !strings.Contains(out, "EF56-GH02") {
		t.Fatalf("replacement output did not show the new claim target/code: %q", out)
	}
	if strings.Contains(out, oldURL) || strings.Contains(out, "AB12-CD34") {
		t.Fatalf("replacement output retained the expired claim target/code: %q", out)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 2 {
		t.Fatalf("Register calls = %d, want exactly 2", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-2" || storedClaim(t, cfgPath, "agent-2").ClaimURL != newURL || storedClaim(t, cfgPath, "agent-2").ClaimCode != "EF56-GH02" || reg.Status != "unclaimed" {
		t.Fatalf("durable replacement registration = %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(credentialsPath(mustLoadConfig(t, cfgPath).Search)); err != nil || !strings.Contains(string(got), "sr-stubkey-2") {
		t.Fatalf("new platform key was not published: err=%v contents=%q", err, got)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("pending registration journal remains after replacement: ok=%v err=%v", ok, err)
	}

	if statusOut := statusText(t, cfgPath); !strings.Contains(statusOut, newURL) || strings.Contains(statusOut, oldURL) {
		t.Fatalf("status did not report only the replacement identity: %q", statusOut)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("second foreground connect failed: code=%d stderr=%s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 2 {
		t.Fatalf("second foreground connect minted another agent: Register calls=%d", registerCalls)
	}
	reg, ok = loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-2" {
		t.Fatalf("replacement identity did not remain durable: %+v ok=%v", reg, ok)
	}
}

func TestDetachedResumePersistsExpiredWithoutReplacing(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("initial connect failed: %s", errOut)
	}
	platform.setStatus("expired")

	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("detached resume failed: %d %s", code, errOut)
	}
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("detached resume of persisted expiry failed: %d %s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("detached resume replaced the expired identity: Register calls=%d", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-1" || reg.Status != "expired" {
		t.Fatalf("detached resume did not persist the expired identity: %+v ok=%v", reg, ok)
	}
	if shouldResume(mustLoadConfig(t, cfgPath), noEnv) {
		t.Fatal("shouldResume approved a replacement for an expired identity")
	}
}

func TestExpiredRegistrationNotFoundDoesNotTriggerReplacement(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("initial connect failed: %s", errOut)
	}
	platform.setStatus("expired")
	if code, _, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("detached expiry observation failed: %d %s", code, errOut)
	}
	platform.setStatusNotFound(true)

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK {
		t.Fatal("not-found expired identity was replaced as if expiry had been confirmed")
	}
	if !strings.Contains(errOut, "no longer known to the platform") {
		t.Fatalf("not-found result was not kept distinct from expiry: %q", errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("not-found handling minted a replacement: Register calls=%d", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-1" || reg.Status != "expired" {
		t.Fatalf("not-found handling changed the expired registration: %+v ok=%v", reg, ok)
	}
}

func TestExpiredRegistrationWithMissingCredentialRefusesForceReplacement(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(auth.AgentRegistration{
		AgentID: "agent-expired", Status: "expired",
	}); err != nil {
		t.Fatal(err)
	}
	seedClaim(t, cfgPath, "agent-expired", platform.srv.URL+"/claim/OLD", "OLD")
	cfg := mustLoadConfig(t, cfgPath)
	credPath := credentialsPath(cfg.Search)
	if err := os.Remove(credPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	code, _, errOut := runConnect(t, cfgPath, nil, "-force")
	if code == exitOK {
		t.Fatal("connect -force replaced an expired registration without a verifiable credential")
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("Register calls = %d, want 0", registerCalls)
	}
	if !strings.Contains(errOut, "cannot safely verify") || !strings.Contains(errOut, "platform credential") {
		t.Fatalf("missing actionable missing-credential diagnostic: %q", errOut)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-expired" || reg.Status != "expired" {
		t.Fatalf("expired registration changed: %+v ok=%v", reg, ok)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatalf("missing credential was unexpectedly written: %v", err)
	}
	if _, err := os.Stat(testJournalPath(t, cfgPath)); !os.IsNotExist(err) {
		t.Fatalf("replacement journal was unexpectedly created: %v", err)
	}
}

func TestExpiredRegistrationStatusFailureRefusesForceReplacement(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(auth.AgentRegistration{
		AgentID: "agent-expired", Status: "expired",
	}); err != nil {
		t.Fatal(err)
	}
	seedClaim(t, cfgPath, "agent-expired", platform.srv.URL+"/claim/OLD", "OLD")
	cfg := mustLoadConfig(t, cfgPath)
	credPath := credentialsPath(cfg.Search)
	if err := writeCredentials(credPath, credentials{APIKey: "sr-old-key"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}
	platform.setStatusError(true)

	code, _, errOut := runConnect(t, cfgPath, nil, "-force")
	if code == exitOK {
		t.Fatal("connect -force replaced an expired registration after status verification failed")
	}
	if !strings.Contains(errOut, "could not safely verify") || !strings.Contains(errOut, "-force") {
		t.Fatalf("missing actionable status-failure diagnostic: %q", errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("Register calls = %d, want 0", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-expired" || reg.Status != "expired" {
		t.Fatalf("expired registration changed: %+v ok=%v", reg, ok)
	}
	if raw, err := os.ReadFile(credPath); err != nil || !strings.Contains(string(raw), "sr-old-key") { // #nosec G304 -- test controls its credential path
		t.Fatalf("existing credential changed: err=%v contents=%q", err, raw)
	}
	if _, err := os.Stat(testJournalPath(t, cfgPath)); !os.IsNotExist(err) {
		t.Fatalf("replacement journal was unexpectedly created: %v", err)
	}
}

func TestLostRegisterResponseLeavesNoLocalCommitAndNextConnectMayRetry(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.dropNextRegisterResponse()
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK {
		t.Fatal("connect claimed success after the Register response was lost")
	}
	if !strings.Contains(errOut, "register") {
		t.Fatalf("lost response was not reported as a Register transport failure: %q", errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("Register calls after lost response=%d, want 1", registerCalls)
	}
	if _, ok := loadAgent(t, stateDir); ok {
		t.Fatal("an invented agent registration was persisted")
	}
	if _, err := os.Stat(testJournalPath(t, cfgPath)); !os.IsNotExist(err) {
		t.Fatalf("a complete-response journal was created after a lost response: %v", err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("ordinary connect could not retry after ambiguous Register failure: %d %s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 2 {
		t.Fatalf("ordinary retry did not register normally: Register calls=%d", registerCalls)
	}
}

func TestPendingRegistrationRecoveryPublishesWithoutRegister(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	client := platformapi.New(cfg.Platform.AgentsAPIURL, cfg.Platform.BaseURL)
	fresh, err := client.Register(context.Background(), "journal-crash", "", nil)
	if err != nil {
		t.Fatalf("completed Register response: %v", err)
	}
	pending := pendingRegistrationFromPlatform(fresh, cfg)
	if err := testJournal(t, cfgPath).Save(pending); err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("pending registration recovery failed: %d %s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("pending recovery changed the completed Register count: %d", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != pending.AgentID || storedClaim(t, cfgPath, pending.AgentID).ClaimURL != pending.ClaimURL {
		t.Fatalf("journaled registration was not published: %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(credentialsPath(cfg.Search)); err != nil || !strings.Contains(string(got), pending.Key) {
		t.Fatalf("journaled platform key was not published: err=%v contents=%q", err, got)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("journal was not cleared after recovery: ok=%v err=%v", ok, err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("repeated recovery/resume failed: %d %s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("repeated recovery minted another registration: %d", registerCalls)
	}
}

// B.3 review correction §3: the pending journal is checked BEFORE the
// registration-load branch where the rebuild lives (connectRun's
// `if hasPending {...} else {...}` — untouched by B.3), so a valid pending
// journal must win outright even when a corrupt agent.json and a stored
// platform credential are also present — exactly the combination that
// would otherwise route into the /v1/agents/me rebuild.
func TestPendingJournalRecoveryNeverCallsMeEvenWithACorruptRegistrationAndCredentialPresent(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	if _, err := auth.OpenStore(stateDir); err != nil {
		t.Fatal(err)
	}

	client := platformapi.New(cfg.Platform.AgentsAPIURL, cfg.Platform.BaseURL)
	fresh, err := client.Register(context.Background(), "journal-plus-corrupt", "", nil)
	if err != nil {
		t.Fatalf("completed Register response: %v", err)
	}
	pending := pendingRegistrationFromPlatform(fresh, cfg)
	if err := testJournal(t, cfgPath).Save(pending); err != nil {
		t.Fatal(err)
	}

	// A platform credential is on file and agent.json is corrupt — the
	// exact combination that, absent a pending journal, would route into
	// the B.3 rebuild instead of a plain refusal.
	if err := writeCredentials(credentialsPath(cfg.Search), credentials{APIKey: pending.Key}); err != nil {
		t.Fatal(err)
	}
	corruptBytes := []byte("{not json")
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("pending recovery with a corrupt registration and credential present failed: %d %s", code, errOut)
	}
	if platform.meCallCount() != 0 {
		t.Fatalf("/v1/agents/me calls = %d, want 0 — the pending journal must win before any rebuild is considered", platform.meCallCount())
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 1 {
		t.Fatalf("Register calls = %d, want 1 (the journal's own completed Register, no fresh one)", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != pending.AgentID || storedClaim(t, cfgPath, pending.AgentID).ClaimURL != pending.ClaimURL {
		t.Fatalf("journaled registration was not published: %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir, "agent.json.corrupt")); err != nil || string(got) != string(corruptBytes) { // #nosec G304 -- test controls its temporary state directory
		t.Fatalf("the pre-existing corrupt registration evidence was not preserved as PR3 already does: err=%v contents=%q", err, got)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("journal was not cleared after recovery: ok=%v err=%v", ok, err)
	}
}

func TestPendingExpiredReplacementRecoveryCompletesAcrossRestart(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	oldURL := platform.srv.URL + "/claim/OLD"
	if err := store.SaveAgentRegistration(auth.AgentRegistration{
		AgentID: "agent-old", Status: "expired",
	}); err != nil {
		t.Fatal(err)
	}
	seedClaim(t, cfgPath, "agent-old", oldURL, "OLD")
	if err := writeCredentials(credentialsPath(cfg.Search), credentials{APIKey: "sr-old-key"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}
	if err := testJournal(t, cfgPath).Save(auth.PendingRegistration{
		AgentID:         "agent-new",
		Key:             "sr-new-key",
		ClaimURL:        platform.srv.URL + "/claim/NEW",
		ClaimCode:       "NEW",
		ClaimExpiresAt:  "2026-09-16T00:00:00Z",
		Status:          "unclaimed",
		ReplaceExpired:  true,
		PreviousAgentID: "agent-old",
		PreviousKey:     "sr-old-key",
	}); err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code != exitOK {
		t.Fatalf("replacement journal recovery failed: %d %s", code, errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("restart recovery minted another registration: %d", registerCalls)
	}
	reg, ok := loadAgent(t, stateDir)
	if !ok || reg.AgentID != "agent-new" || storedClaim(t, cfgPath, "agent-new").ClaimURL != platform.srv.URL+"/claim/NEW" || reg.Status != "unclaimed" {
		t.Fatalf("replacement journal did not complete publication: %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(credentialsPath(cfg.Search)); err != nil || !strings.Contains(string(got), "sr-new-key") {
		t.Fatalf("replacement key was not published after restart: err=%v contents=%q", err, got)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("replacement journal survived successful recovery: ok=%v err=%v", ok, err)
	}
}

// The state directory is a Codex sandbox writable root. A journal written
// there with replace_expired authority — and an agent.json to match it — must
// not let connect publish its key over credentials.json.
func TestForgedStateDirJournalCannotReplaceCredential(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credentialsPath(cfg.Search), credentials{APIKey: "sr-victim-key"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}
	forged := auth.PendingRegistration{
		V:               1,
		AgentID:         "agent-attacker",
		Key:             "sr-attacker-key",
		ClaimURL:        platform.srv.URL + "/claim/EVIL",
		ClaimCode:       "EVIL",
		ClaimExpiresAt:  "2026-09-16T00:00:00Z",
		Status:          "unclaimed",
		ReplaceExpired:  true,
		PreviousAgentID: "agent-victim",
		PreviousKey:     "sr-victim-key",
	}
	raw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(stateDir, registrationJournalFile)
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(agentRegistrationFromPending(forged)); err != nil {
		t.Fatal(err)
	}

	_, _, errOut := runConnect(t, cfgPath, nil, "-resume")
	if !strings.Contains(errOut, "discarded registration_pending.json") {
		t.Errorf("no diagnostic for the discarded state-dir journal: %q", errOut)
	}
	if got, err := os.ReadFile(credentialsPath(cfg.Search)); err != nil || !strings.Contains(string(got), "sr-victim-key") || strings.Contains(string(got), "sr-attacker-key") {
		t.Fatalf("forged state-dir journal replaced the credential: err=%v contents=%q", err, got)
	}
	if lexists(legacy) {
		t.Error("the state-dir journal was left in place")
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("a journal appeared beside credentials.json: ok=%v err=%v", ok, err)
	}
}

func TestPendingRegistrationConflictDoesNotOverwriteOrRegister(t *testing.T) {
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	pending := auth.PendingRegistration{
		AgentID:        "agent-journal",
		Key:            "sr-journal-key",
		ClaimURL:       platform.srv.URL + "/claim/JOURNAL-1",
		ClaimCode:      "JOURNAL-1",
		ClaimExpiresAt: "2026-09-16T00:00:00Z",
		Status:         "unclaimed",
	}
	if err := testJournal(t, cfgPath).Save(pending); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credentialsPath(cfg.Search), credentials{APIKey: "sr-other-key"}); err != nil { // #nosec G101 -- canned test credential
		t.Fatal(err)
	}

	if code, _, errOut := runConnect(t, cfgPath, nil); code == exitOK {
		t.Fatal("connect overwrote a conflicting credential during journal recovery")
	} else if !strings.Contains(errOut, "different platform key") {
		t.Fatalf("missing journal conflict diagnostic: %q", errOut)
	}
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("journal conflict issued a new Register: %d", registerCalls)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || !ok {
		t.Fatalf("journal was lost after a publication conflict: ok=%v err=%v", ok, err)
	}
}

// The journal and the credential beside it are outside the state
// directory; agent.json is inside it. A record there naming another agent
// used to win: publication refused "a different identity" after the key was
// already published, the journal stayed, and every later run failed the same
// way. The review planted exactly that record. Now the journal's record is
// published and the other is set aside as evidence, with no new Register.
func TestAJournalWinsOverAnAgentRecordNamingAnotherAgent(t *testing.T) {
	platform := newStubPlatform(t)
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(auth.AgentRegistration{
		AgentID: "agent-other", Status: "claimed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := testJournal(t, cfgPath).Save(auth.PendingRegistration{
		AgentID:        "agent-journal",
		Key:            "sr-journal-key",
		ClaimURL:       platform.srv.URL + "/claim/JOURNAL-1",
		ClaimCode:      "JOURNAL-1",
		ClaimExpiresAt: "2026-09-16T00:00:00Z",
		Status:         "unclaimed",
	}); err != nil {
		t.Fatal(err)
	}

	runConnect(t, cfgPath, nil, "-resume")
	if registerCalls, _, _ := platform.counts(); registerCalls != 0 {
		t.Fatalf("journal recovery issued a new Register: %d", registerCalls)
	}
	if reg, ok := loadAgent(t, stateDir); !ok || reg.AgentID != "agent-journal" {
		t.Fatalf("the journal's registration was not published over the other record: %+v ok=%v", reg, ok)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir, "agent.json.replaced")); err != nil || !strings.Contains(string(got), "agent-other") { // #nosec G304 -- the test's own state dir
		t.Fatalf("the other record was not kept as evidence: %q %v", got, err)
	}
	if _, ok, err := testJournal(t, cfgPath).Load(); err != nil || ok {
		t.Fatalf("the journal was not finished: ok=%v err=%v", ok, err)
	}
	if got, err := readCredentials(credentialsPath(cfg.Search)); err != nil || got.APIKey != "sr-journal-key" {
		t.Fatalf("credentials.json = %+v %v", got, err)
	}
}

// WP2-adversarial-review finding 11 (second half): the foreground loop's
// own sleep must be bounded by its remaining budget, not just by the
// platform's (already ceiling-clamped) advertised interval. A platform
// that never claims and advertises a long interval must still see the
// loop give up close to connectPollBudget, not wait out the interval.
func TestForegroundLoopSleepIsBoundedByRemainingBudget(t *testing.T) {
	origBudget, origInterval := connectPollBudget, resumePollInterval
	connectPollBudget = 100 * time.Millisecond
	t.Cleanup(func() { connectPollBudget, resumePollInterval = origBudget, origInterval })

	// newStubPlatform's default register response advertises interval_s: 1,
	// which clampPollInterval floors to 2s (well past the 100ms budget) —
	// exactly the gap that matters: if the loop's sleep used that raw
	// interval instead of min(interval, remaining budget), this test would
	// time out waiting for connect to return at all. The registration
	// stays "unclaimed" throughout (nothing calls platform.claim).
	platform := newStubPlatform(t)
	cfgPath, _ := connectConfig(t, platform.srv.URL, "")

	start := time.Now()
	code, out, _ := runConnect(t, cfgPath, nil)
	elapsed := time.Since(start)
	if code != exitOK {
		t.Fatalf("connect exited %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "not claimed yet") {
		t.Fatalf("expected the timeout message, got: %q", out)
	}
	// Generous slack (10x the budget) for scheduling noise, but nowhere
	// near the multi-second interval a stub's default advertises — this
	// only needs to prove the sleep did NOT wait out the raw interval.
	if elapsed > 10*connectPollBudget {
		t.Fatalf("connect took %v, want close to connectPollBudget (%v) — the sleep was not bounded by the remaining budget", elapsed, connectPollBudget)
	}
}
