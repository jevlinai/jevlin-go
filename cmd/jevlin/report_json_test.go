package main

// doctor, status and connect as JSON.
//
// The property under test is not that JSON comes out. It is that the JSON
// and the text report are two renderings of one set of gathered facts:
// same checks, same verdicts, same counts, same decisions, no network
// call either does not make, and nothing derived by reading the other's
// prose.

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

func decodeCommandEnvelope(t *testing.T, stdout, command string) map[string]any {
	t.Helper()
	env := decodeEnvelope(t, stdout)
	if env["command"] != command {
		t.Fatalf("command %v, want %q", env["command"], command)
	}
	return env
}

func dataOf(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	d, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope has no data object: %v", env)
	}
	return d
}

// ── doctor ──────────────────────────────────────────────────────────────

func TestDoctorJSONRendersTheSameChecksTheTextReportDoes(t *testing.T) {
	f := healthyFacts()
	checks := assembleDoctor(f)

	var buf bytes.Buffer
	emitMachine(&buf, doctorEnvelope(f, checks, doctorExit(checks)))
	env := decodeCommandEnvelope(t, buf.String(), "doctor")
	data := dataOf(t, env)

	// One check object per check the text renderer would print, in order,
	// with the same verdict — not a re-derivation.
	got, _ := data["checks"].([]any)
	if len(got) != len(checks) {
		t.Fatalf("%d checks in JSON, %d assembled", len(got), len(checks))
	}
	for i, raw := range got {
		c, _ := raw.(map[string]any)
		if c["name"] != checks[i].Name || c["verdict"] != string(checks[i].Verdict) {
			t.Errorf("check %d = %v, want %s/%s", i, c, checks[i].Name, checks[i].Verdict)
		}
		if checks[i].Detail != "" && c["detail"] != checks[i].Detail {
			t.Errorf("check %d detail = %v, want %q", i, c["detail"], checks[i].Detail)
		}
	}
	// Ruling D-R1: the config the report is against, and that it is a file
	// rather than built-in defaults, are explicit fields.
	if data["config_source"] != f.ConfigSource || data["config_defaulted"] != false || data["state_dir"] != f.StateDir {
		t.Errorf("config fields: %v", data)
	}
	// Every check ran, which is an empty list, not an omitted key.
	listed, ok := data["checks_that_could_not_run"].([]any)
	if !ok || len(listed) != 0 {
		t.Errorf("checks_that_could_not_run = %v (%T), want an empty list", data["checks_that_could_not_run"], data["checks_that_could_not_run"])
	}
}

// UNKNOWN is the absence of a fact. It must survive into JSON as UNKNOWN
// and must never arrive as false.
func TestDoctorJSONKeepsUnknownChecksUnknown(t *testing.T) {
	f := healthyFacts()
	// An unreadable update stamp is the one input that leaves a check
	// unable to answer rather than answering no.
	f.Update.Err = errors.New("stamp: not JSON")
	checks := assembleDoctor(f)

	var buf bytes.Buffer
	emitMachine(&buf, doctorEnvelope(f, checks, doctorExit(checks)))
	data := dataOf(t, decodeCommandEnvelope(t, buf.String(), "doctor"))

	unknown := map[string]bool{}
	for _, raw := range data["checks"].([]any) {
		c, _ := raw.(map[string]any)
		if c["verdict"] == string(verdictUnknown) {
			unknown[c["name"].(string)] = true
		}
		if c["verdict"] == false || c["verdict"] == true {
			t.Errorf("a verdict became a boolean: %v", c)
		}
	}
	if len(unknown) == 0 {
		t.Fatal("no check came back UNKNOWN with the update stamp unreadable")
	}
	listed, _ := data["checks_that_could_not_run"].([]any)
	if len(listed) != len(unknown) {
		t.Errorf("checks_that_could_not_run %v, unknown verdicts %v", listed, unknown)
	}
	for _, name := range listed {
		if !unknown[name.(string)] {
			t.Errorf("%q is listed as unable to run but its verdict is not UNKNOWN", name)
		}
	}
}

// countingConnections wraps an httptest server so a test can count the
// connections dialed to it — the observable, rather than handler hits.
func countingConnections(t *testing.T, h http.Handler) (*httptest.Server, func() int64) {
	t.Helper()
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns.Load
}

// Every check is local (doctor.go): the report reads the config, the
// credential, the store and the update stamp, and asks nobody. The JSON
// report must not buy its structure with a round trip the text report does
// not make — and here neither makes one, to the router or the platform,
// with both configured and the stored key bound to them.
func TestDoctorDialsNothingInEitherMode(t *testing.T) {
	srv, conns := countingConnections(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	root := t.TempDir()
	cfgPath := filepath.Join(root, "jevlin.toml")
	toml := `[platform]
base_url = "` + srv.URL + `"
agents_api_url = "` + srv.URL + `"

[miner]
router_url = "` + srv.URL + `"
state_dir = "` + filepath.ToSlash(filepath.Join(root, "state")) + `"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(filepath.Join(root, "credentials.json"), credentials{APIKey: "sr-fictional", Router: srv.URL, AgentsAPI: srv.URL}); err != nil {
		t.Fatal(err)
	}

	var text, jsonOut bytes.Buffer
	cmdDoctor([]string{"-config", cfgPath}, &text, io.Discard)
	cmdDoctor([]string{"-config", cfgPath, "-json"}, &jsonOut, io.Discard)

	if n := conns(); n != 0 {
		t.Errorf("doctor dialed the configured host %d time(s)", n)
	}
	// Ruling D-R1: doctor names the config it resolved before anything
	// else, so the report starts with that line, then the checks.
	if !strings.HasPrefix(text.String(), "config:  "+cfgPath+"\n") {
		t.Errorf("the text report changed shape: %q", text.String())
	}
	if !strings.Contains(text.String(), "\n  search key") {
		t.Errorf("the text report is missing the search key line: %q", text.String())
	}
	data := dataOf(t, decodeCommandEnvelope(t, jsonOut.String(), "doctor"))
	if data["config_source"] != cfgPath {
		t.Errorf("config_source = %v, want %q", data["config_source"], cfgPath)
	}
}

// A check's fix renders through the existing check shape — no new
// top-level field, so an SDK that already walks `checks` sees it without
// changing.
func TestDoctorJSONCarriesACheckFixThroughTheCheckShape(t *testing.T) {
	f := healthyFacts()
	// A release this copy is not, so `fix` is populated on one check and
	// the serializer's handling of it is exercised rather than assumed.
	f.Update.Stamp.Latest, f.Update.Stamp.Outcome, f.Update.Fix = "v0.8.0", updateAvailable, "jevlin upgrade"
	checks := assembleDoctor(f)

	var buf bytes.Buffer
	emitMachine(&buf, doctorEnvelope(f, checks, doctorExit(checks)))
	data := dataOf(t, decodeCommandEnvelope(t, buf.String(), "doctor"))

	got := map[string]map[string]any{}
	for _, raw := range data["checks"].([]any) {
		c, _ := raw.(map[string]any)
		name, _ := c["name"].(string)
		got[name] = c
	}
	c, ok := got["up to date"]
	if !ok {
		t.Fatalf("%q is missing from doctor -json: %s", "up to date", buf.String())
	}
	if c["verdict"] != string(verdictNo) {
		t.Errorf("up to date verdict = %v, want NO", c["verdict"])
	}
	detail, _ := c["detail"].(string)
	if !strings.Contains(detail, "v0.8.0 is available") {
		t.Errorf("detail = %q", detail)
	}
	if c["fix"] != "jevlin upgrade" {
		t.Errorf("the check's fix did not reach JSON: %v", c["fix"])
	}
	for _, name := range []string{"search key", "registration", "trace key"} {
		if _, present := got[name]["fix"]; present {
			t.Errorf("%q carries a fix with nothing to fix: %v", name, got[name])
		}
	}
}

// ── status ──────────────────────────────────────────────────────────────

// statusFixture writes a config and opens its state directory, with no
// registration in it yet.
func statusFixture(t *testing.T) (cfgPath, root string, store *auth.Store) {
	t.Helper()
	root = t.TempDir()
	cfgPath = filepath.Join(root, "jevlin.toml")
	toml := `[miner]
state_dir = "` + filepath.ToSlash(filepath.Join(root, "state")) + `"
`
	if err := os.WriteFile(cfgPath, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	store, err = auth.OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath, root, store
}

func TestStatusJSONAndTextComeFromTheSameFacts(t *testing.T) {
	cfgPath, root, store := statusFixture(t)
	reg := auth.AgentRegistration{
		AgentID: "agent-fictional-1", Status: "claimed", Scopes: []string{"search", "fetch"},
	}
	if err := store.SaveAgentRegistration(reg); err != nil {
		t.Fatal(err)
	}

	args := []string{"-config", cfgPath}
	var text, textErr bytes.Buffer
	if code := statusMain(args, &text, &textErr, noEnv); code != exitOK {
		t.Fatalf("text status exit %d", code)
	}
	var jsonOut, jsonErr bytes.Buffer
	if code := statusMain(append(args, "-json"), &jsonOut, &jsonErr, noEnv); code != exitOK {
		t.Fatalf("json status exit %d", code)
	}
	data := dataOf(t, decodeCommandEnvelope(t, jsonOut.String(), "status"))

	// Ruling D-R1: the same config line the text report opens with.
	if data["config_source"] != cfgPath || data["config_defaulted"] != false ||
		data["state_dir"] != filepath.Join(root, "state") {
		t.Errorf("config fields: %v", data)
	}
	if !strings.HasPrefix(text.String(), "config:  "+cfgPath+"\n") {
		t.Errorf("the text report no longer opens with the config: %q", text.String())
	}

	agent, _ := data["agent"].(map[string]any)
	scopes, _ := agent["scopes"].([]any)
	if agent["agent_id"] != "agent-fictional-1" || agent["status"] != "claimed" ||
		agent["claimed"] != true || len(scopes) != 2 || scopes[0] != "search" || scopes[1] != "fetch" {
		t.Errorf("agent: %v", agent)
	}
	// The same registration the text renderer printed, scopes included.
	if !strings.Contains(text.String(), "agent:  claimed (scopes: search, fetch)") {
		t.Errorf("the text report no longer says claimed with the scopes: %q", text.String())
	}
	// The update object is always present: no stamp is "checked: false",
	// not an omitted key, with the mode in force.
	update, _ := data["update"].(map[string]any)
	if update == nil || update["checked"] != false || update["mode"] != config.UpdateApply {
		t.Errorf("update: %v", data["update"])
	}
	if !strings.Contains(text.String(), "update:  ") {
		t.Errorf("the text report dropped the update line the JSON report carries: %q", text.String())
	}
	if jsonErr.String() != "" {
		t.Errorf("status -json wrote to stderr: %q", jsonErr.String())
	}
}

// Whatever the store says, both renderers must say the same thing about
// it. This walks every registration status rather than asserting one.
func TestStatusRenderersAgreeAcrossEveryRegistrationStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*auth.Store) error
		status string // the JSON agent.status; "" means no agent object
		text   string
	}{
		{"unregistered", func(*auth.Store) error { return nil }, "", "agent:   not registered"},
		{"unclaimed", func(s *auth.Store) error {
			return s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-2", Status: "unclaimed"})
		}, "unclaimed", "agent:  unclaimed"},
		{"expired", func(s *auth.Store) error {
			return s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-3", Status: "expired"})
		}, "expired", "agent:  expired"},
		{"claimed", func(s *auth.Store) error {
			return s.SaveAgentRegistration(auth.AgentRegistration{AgentID: "agent-fictional-4", Status: "claimed", Scopes: []string{"search"}})
		}, "claimed", "agent:  claimed (scopes: search)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, _, store := statusFixture(t)
			if err := tc.setup(store); err != nil {
				t.Fatal(err)
			}
			args := []string{"-config", cfgPath}
			var text bytes.Buffer
			statusMain(args, &text, io.Discard, noEnv)
			var jsonOut bytes.Buffer
			statusMain(append(args, "-json"), &jsonOut, io.Discard, noEnv)

			data := dataOf(t, decodeCommandEnvelope(t, jsonOut.String(), "status"))
			agent, hasAgent := data["agent"].(map[string]any)
			switch {
			case tc.status == "" && hasAgent:
				t.Errorf("JSON reports an agent where none is registered: %v", agent)
			case tc.status != "" && !hasAgent:
				t.Errorf("JSON reports no agent: %v", data)
			case tc.status != "" && (agent["status"] != tc.status || agent["claimed"] != (tc.status == "claimed")):
				t.Errorf("JSON agent %v, want status %q", agent, tc.status)
			}
			if !strings.Contains(text.String(), tc.text) {
				t.Errorf("text report %q does not contain %q", text.String(), tc.text)
			}
		})
	}
}

// ── connect ─────────────────────────────────────────────────────────────

func TestConnectJSONExposesClaimArtifactsAndNoCredential(t *testing.T) {
	const key = "sr-connect-canary-secret"
	cfgPath, root, store := statusFixture(t)
	reg := auth.AgentRegistration{
		AgentID:        "agent-fictional-9",
		Status:         "unclaimed",
		ClaimExpiresAt: "2026-09-13T00:00:00Z",
	}
	if err := store.SaveAgentRegistration(reg); err != nil {
		t.Fatal(err)
	}
	claim := auth.ClaimBootstrap{AgentID: reg.AgentID, ClaimURL: config.DefaultPlatformBaseURL + "/claim/abc123", ClaimCode: "ABC-123"}
	seedClaim(t, cfgPath, claim.AgentID, claim.ClaimURL, claim.ClaimCode)
	if err := writeCredentials(filepath.Join(root, "credentials.json"), credentials{APIKey: key}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	emitMachine(&buf, connectEnvelope(cfgPath, noEnv, exitOK, ""))
	env := decodeCommandEnvelope(t, buf.String(), "connect")
	data := dataOf(t, env)

	if data["claim_url"] != claim.ClaimURL || data["claim_code"] != claim.ClaimCode {
		t.Errorf("claim artifacts are not explicit fields: %v", data)
	}
	if data["agent_id"] != reg.AgentID || data["status"] != "unclaimed" || data["claimed"] != false {
		t.Errorf("registration: %v", data)
	}
	// Unclaimed is not a failure: search already works. What is
	// outstanding is a person visiting the URL.
	if env["ok"] != true || env["code"] != "unclaimed" || env["action"] != actionConnect {
		t.Errorf("header: %v", env)
	}
	if strings.Contains(buf.String(), key) {
		t.Fatalf("the platform credential reached the JSON report:\n%s", buf.String())
	}
	for _, forbidden := range []string{"api_key", "apiKey", "refresh", "dpop", "private_key", "mnemonic", "passphrase"} {
		if strings.Contains(strings.ToLower(buf.String()), forbidden) {
			t.Errorf("the JSON report carries a %q field: %s", forbidden, buf.String())
		}
	}
}

// A claim link is never shown unless it is on file beside credentials.json
// and still on the configured platform.base_url origin (invariant 12): not
// by status's text or JSON report, and not by connect's JSON envelope. Each
// bad link is planted twice, the way a sandboxed command would plant it in
// agent.json (which no longer holds a link at all) and as a claim record
// whose link fails the check, written raw because the record's own Save
// refuses a control character.
func TestAStoredClaimURLOffThePlatformOriginIsNeverReported(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example/claim/abc123",
		"http://platform.jevlin.ai/claim/abc123",
		"https://platform.jevlin.ai.evil.example/claim/abc123",
		"https://platform.jevlin.ai/claim/abc123 https://evil.example/claim",
		"https://platform.jevlin.ai/claim/\x1b]8;;https://evil.example\x07x",
	} {
		cfgPath, root, _ := statusFixture(t)
		writeAgentRecordRaw(t, filepath.Join(root, "state"), map[string]any{
			"agent_id": "agent-fictional-9", "status": "unclaimed", "claim_url": bad, "claim_code": "ABC-123",
		})
		writeClaimRecordRaw(t, root, map[string]any{"agent_id": "agent-fictional-9", "claim_url": bad, "claim_code": "ABC-123"})

		var text, textErr bytes.Buffer
		if code := statusMain([]string{"-config", cfgPath}, &text, &textErr, noEnv); code != exitOK {
			t.Fatalf("status exited %d: %s", code, textErr.String())
		}
		var statusJSON, statusJSONErr bytes.Buffer
		if code := statusMain([]string{"-json", "-config", cfgPath}, &statusJSON, &statusJSONErr, noEnv); code != exitOK {
			t.Fatalf("status -json exited %d: %s", code, statusJSONErr.String())
		}
		var connectJSON bytes.Buffer
		emitMachine(&connectJSON, connectEnvelope(cfgPath, noEnv, exitOK, ""))

		if !strings.Contains(text.String(), "agent:  unclaimed") {
			t.Errorf("status no longer reports the unclaimed agent:\n%s", text.String())
		}
		requireNoPlantedEscape(t, "status", text.String()+textErr.String()+statusJSON.String()+statusJSONErr.String()+connectJSON.String())
		for name, out := range map[string]string{
			"status":        text.String() + textErr.String(),
			"status -json":  statusJSON.String() + statusJSONErr.String(),
			"connect -json": connectJSON.String(),
		} {
			if strings.Contains(out, "evil.example") || strings.Contains(out, "http://platform") || strings.Contains(out, "ABC-123") {
				t.Errorf("%s reported the stored claim artifacts for %q:\n%s", name, bad, out)
			}
		}
	}
}

func TestConnectJSONOutcomeFollowsTheStoredStatus(t *testing.T) {
	for _, tc := range []struct {
		exit          int
		status        string
		wantCode      string
		wantRetryable bool
		wantAction    string
	}{
		{exitOK, "claimed", "ok", false, actionNone},
		{exitOK, "unclaimed", "unclaimed", false, actionConnect},
		{exitOK, "expired", "registration_expired", false, actionConnect},
		{exitTransport, "unclaimed", "connect_failed", true, actionRetry},
		{exitUsage, "", "connect_failed", false, actionReport},
	} {
		code, retryable, action := connectOutcome(tc.exit, tc.status)
		if code != tc.wantCode || retryable != tc.wantRetryable || action != tc.wantAction {
			t.Errorf("connectOutcome(%d, %q) = (%q, %v, %q), want (%q, %v, %q)",
				tc.exit, tc.status, code, retryable, action, tc.wantCode, tc.wantRetryable, tc.wantAction)
		}
	}
}

// Nothing in this client opens a URL. The claim URL is printed for a
// person, in both renderers.
func TestNothingOpensAClaimURL(t *testing.T) {
	for _, dir := range []string{".", "../../pkg/platform"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- this package's own source
			if err != nil {
				t.Fatal(err)
			}
			for _, opener := range []string{`"xdg-open"`, "rundll32", "browser.OpenURL", "exec.Command(\"open\""} {
				if bytes.Contains(raw, []byte(opener)) {
					t.Errorf("%s/%s looks like it opens a URL (%s)", dir, e.Name(), opener)
				}
			}
		}
	}
}
