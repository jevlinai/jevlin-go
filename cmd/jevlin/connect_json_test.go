package main

// Corrections 4 and 5: what `action: connect` means, and what `connect
// -json` says about a command it cannot run — an invalid command is a
// usage error about the command, never a report about some installation.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// ── 4. the connect action names a workflow, not an absence ──────────────

// All three of these are the registration/claim workflow needing
// attention. Narrowing the action to "there is no registration" would
// leave the other two with nothing honest to say.
func TestTheConnectActionCoversTheWholeRegistrationWorkflow(t *testing.T) {
	for _, status := range []string{"unclaimed", "expired"} {
		code, retryable, action := connectOutcome(exitOK, status)
		if action != actionConnect {
			t.Errorf("connectOutcome(ok, %q) action = %q, want %q", status, action, actionConnect)
		}
		if retryable {
			t.Errorf("connectOutcome(ok, %q) is retryable; a human step is not a retry", status)
		}
		if code == "" {
			t.Errorf("connectOutcome(ok, %q) has no code", status)
		}
	}
	// And a search with no local credential is the same workflow.
	if c := classifySearch(searchOutcome{Fault: faultNoCredential}); c.Action != actionConnect {
		t.Errorf("a missing search credential gives action %q, want %q", c.Action, actionConnect)
	}
}

// The guidance must not tell an agent that action connect proves there is
// no registration — two of the three states it covers have one.
func TestGuidanceDoesNotClaimConnectMeansNoRegistrationExists(t *testing.T) {
	entry := guidanceEntry()
	narrow := regexp.MustCompile(`(?i)connect[^.\n]{0,60}(no registration here at all|is not registered|never registered here)`)
	for _, s := range targetsByKind(targetHost) {
		text := instructionTextFor(t, s.ID(), entry)
		if m := narrow.FindString(text); m != "" {
			t.Errorf("%s is told the narrow connect contract (%q):\n%s", s.Label(), m, text)
		}
	}
}

// And the 401 rule survives the broadening: it still maps to login.
func TestGuidanceStillSendsA401ToLoginNotConnect(t *testing.T) {
	entry := guidanceEntry()
	for _, s := range targetsByKind(targetHost) {
		if s.ID() == "opencode" {
			continue // the snippet is short by design; the skill carries the 401 rule
		}
		flat := strings.Join(strings.Fields(instructionTextFor(t, s.ID(), entry)), " ")
		if !strings.Contains(flat, "401 maps to `login`") {
			t.Errorf("%s is not told that a 401 maps to login:\n%s", s.Label(), flat)
		}
		if !strings.Contains(flat, "does not mean this installation has never registered") {
			t.Errorf("%s is not told that a 401 does not prove absence of registration", s.Label())
		}
	}
	// The classifier agrees.
	if c := classifySearch(routerOutcome(401, "unauthorized", "no")); c.Action != actionLogin {
		t.Errorf("a router 401 gives action %q, want %q", c.Action, actionLogin)
	}
}

// ── 5. -json reports the command, or the run, never a guess ─────────────

// countingPlatformStub is a local platform that refuses everything and
// counts what it was asked for. Zero calls is then an assertion rather
// than an absence of evidence: a test that merely did not crash proves
// nothing about whether a registration was attempted.
func countingPlatformStub(t *testing.T) (url string, calls *atomic.Int64) {
	t.Helper()
	calls = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Logf("the platform stub was called: %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"error":"this test must never reach the platform"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

// connectFixture writes a config whose state directory is fresh unless the
// caller seeds it, against a platform stub that refuses everything.
//
// Pinning the platform at a local stub matters: without it the config
// falls back to the real platform.jevlin.ai, and the pre-parser under test
// would be the only thing standing between this test and a live
// registration attempt — so the moment a mutation disables it, the test
// dials a real host. A fixture whose safety depends on the code under test
// being correct is not a fixture. It also makes the mutation's red arrive
// in milliseconds instead of three minutes.
func connectFixture(t *testing.T) (cfgPath, stateDir string, calls *atomic.Int64) {
	t.Helper()
	root := t.TempDir()
	stateDir = filepath.Join(root, "state")
	cfgPath = filepath.Join(root, "jevlin.toml")
	platform, calls := countingPlatformStub(t)

	toml := `schema = 1

[search]
state_dir = "` + filepath.ToSlash(stateDir) + `"
sessions_dir = "` + filepath.ToSlash(filepath.Join(root, "sessions")) + `"

[platform]
url = "` + platform + `"

[agents]
url = "` + platform + `"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir, calls
}

// ── the corrupt-registration recovery under a refusing platform ─────────

const corruptRegistrationBytes = "{ this is not valid json"

func writeCorruptRegistration(t *testing.T, stateDir string) {
	t.Helper()
	// Through the store, so the file lands where the store looks for it
	// with the mode the store uses, then overwritten with bytes it cannot
	// decode.
	store, err := auth.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRegistration(auth.AgentRegistration{AgentID: "placeholder", Status: "unclaimed"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "agent.json")
	if err := os.WriteFile(path, []byte(corruptRegistrationBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadAgentRegistration(); !errors.Is(err, auth.ErrAgentRegistrationCorrupt) {
		t.Fatalf("the fixture did not produce a corrupt registration: %v", err)
	}
}

// A corrupt agent.json beside a stored platform credential, against a
// platform whose /v1/agents/me fails outright (not the no-oracle 404 the
// stub in agent_onboarding_test.go gives, which
// TestConnectRefusesCorruptRegistrationWithExistingPlatformCredential
// covers): the recovery asks the platform exactly once, reports the
// corrupt record, refuses to mint a second agent, and rewrites nothing.
func TestOrdinaryConnectKeepsItsCorruptRegistrationRecovery(t *testing.T) {
	cfgPath, stateDir, calls := connectFixture(t)
	writeCorruptRegistration(t, stateDir)
	// A platform credential beside it, in the installation's own directory
	// (the state directory's parent): the recovery refuses to mint a second
	// agent, and that refusal is what must survive.
	if err := writeCredentials(filepath.Join(filepath.Dir(stateDir), "credentials.json"),
		credentials{APIKey: "sr-existing-platform-key"}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := cmdConnect([]string{"-config", cfgPath}, strings.NewReader(""), &out, &errOut, installedAs(noEnv, []string{"-config", cfgPath}))

	if code == exitOK {
		t.Fatalf("ordinary connect succeeded despite a credential conflict: %s %s", out.String(), errOut.String())
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the platform was called %d time(s), want exactly 1 (the /v1/agents/me rebuild attempt, no Register)", n)
	}
	if !strings.Contains(errOut.String(), "could not be decoded") {
		t.Errorf("the pre-existing corrupt-registration report is gone: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "-force") {
		t.Errorf("the refusal does not name -force: %q", errOut.String())
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "agent.json")) // #nosec G304 -- this test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != corruptRegistrationBytes {
		t.Errorf("ordinary connect rewrote the corrupt registration: %q", raw)
	}
}

// ── an invalid command is not a report about an installation ────────────
//
// connectCommand re-parses the flags before connectRun does, to know which
// -config the envelope is about. With a parse failure there is no -config,
// and an envelope built anyway would be about the DEFAULT config and state
// directory — on a fresh machine, "nothing registered", a shape that reads
// as a true statement about an installation nobody asked about. A mistyped
// flag must come back as a usage error that says nothing about any
// installation, and must reach no platform.
func TestMalformedMachineFlagsAreAUsageErrorNotALifecycleState(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"-not-a-real-flag"}},
		{"config with no value", []string{"-config"}},
		{"malformed boolean", []string{"-force=notbool"}},
		{"malformed json boolean", []string{"-json=maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stateDir, calls := connectFixture(t)

			args := append([]string{"-json"}, tc.args...)
			var out, errOut bytes.Buffer
			code := cmdConnect(args, strings.NewReader(""), &out, &errOut, installedAs(noEnv, args))

			if code != exitUsage {
				t.Fatalf("exit %d, want %d: %s", code, exitUsage, out.String())
			}
			env := decodeCommandEnvelope(t, out.String(), "connect")
			if env["code"] != "invalid_flags" {
				t.Errorf("code %v, want invalid_flags", env["code"])
			}
			if env["action"] != actionFixInput {
				t.Errorf("action %v, want %q", env["action"], actionFixInput)
			}
			if env["ok"] != false || env["retryable"] != false {
				t.Errorf("header: %v", env)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("the platform was called %d time(s) for an invalid command", n)
			}
			// An invalid command says nothing about this installation, so
			// nothing about it is reported, and nothing of it is written.
			if _, ok := env["data"]; ok {
				t.Errorf("the envelope carries installation data for an invalid command: %s", out.String())
			}
			if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("an invalid command touched the state directory: err=%v", err)
			}
		})
	}
}

// The other half: a well-formed command reaches the run, and the envelope
// reports what the run did. Against a platform that refuses everything the
// run fails at Register, and the header says so in the connect action set
// (report_json.go's connectOutcome): connect_failed, retryable, retry.
func TestValidMachineFlagsReachTheRun(t *testing.T) {
	for _, args := range [][]string{
		{"-config", "", "-json"},
		{"-json", "-config", ""},
		{"-json", "-config", "", "-name", "my agent"},
		{"-json", "-config", "", "-force"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cfgPath, _, calls := connectFixture(t)
			for i, a := range args {
				if a == "" {
					args[i] = cfgPath
				}
			}
			var out, errOut bytes.Buffer
			code := cmdConnect(args, strings.NewReader(""), &out, &errOut, installedAs(noEnv, args))
			if code != exitTransport {
				t.Fatalf("exit %d, want %d: %s", code, exitTransport, out.String())
			}
			env := decodeCommandEnvelope(t, out.String(), "connect")
			if env["code"] != "connect_failed" || env["action"] != actionRetry || env["retryable"] != true || env["ok"] != false {
				t.Errorf("header: %v", env)
			}
			if n := calls.Load(); n == 0 {
				t.Error("the run never reached the platform, so the command did not run")
			}
			errObj, ok := env["error"].(map[string]any)
			if !ok || !strings.Contains(errObj["message"].(string), "register") {
				t.Errorf("the envelope does not carry the run's own register failure: %v", env["error"])
			}
		})
	}
}

// The pre-parser and connectRun must accept the same grammar, or a command
// one of them rejects is handled by the other's rules.
func TestThePreParserAcceptsExactlyWhatConnectRunDoes(t *testing.T) {
	cfgPath, _, _ := connectFixture(t)
	for _, args := range [][]string{
		{"-config", cfgPath},
		{"-config", cfgPath, "-json"},
		{"-config", cfgPath, "-resume"},
		{"-config", cfgPath, "-force", "-name", "x"},
		{"-not-a-real-flag"},
		{"-force=notbool"},
		{"-config"},
	} {
		_, preErr := connectMachineFlags(args)
		// connectRun's own parse, observed through its exit code: it
		// returns exitUsage for exactly the arguments it cannot parse.
		var narration bytes.Buffer
		runCode := connectRun(args, strings.NewReader(""), io.Discard, &narration, installedAs(noEnv, args), false, false)
		runRejected := runCode == exitUsage
		if (preErr != nil) != runRejected {
			t.Errorf("%v: pre-parser err=%v, connectRun rejected=%v", args, preErr, runRejected)
		}
	}
}
