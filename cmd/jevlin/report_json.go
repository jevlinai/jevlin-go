package main

// JSON for the three report commands: doctor, status and connect.
//
// Presentation only. None of these changes a state machine, adds a network
// call, or decides anything the text report did not already decide — each
// is a second renderer over the same gathered facts. That is the rule that
// matters here: an SDK must not have to scrape prose, and a renderer must
// not have to parse its own prose back into fields, because then every
// wording change is a silent data change.

import (
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// ── doctor ──────────────────────────────────────────────────────────────

type doctorReport struct {
	// ConfigSource is the file describeConfigSource resolved (ruling
	// D-R1); ConfigDefaulted is true when resolution found none and this
	// report is against built-in defaults — told apart explicitly so an
	// empty ConfigSource is never read as "no config" by accident.
	ConfigSource    string            `json:"config_source,omitempty"`
	ConfigDefaulted bool              `json:"config_defaulted"`
	StateDir        string            `json:"state_dir,omitempty"`
	Checks          []doctorCheckJSON `json:"checks"`
	ChecksCannotRun []string          `json:"checks_that_could_not_run"`
}

type doctorCheckJSON struct {
	Name string `json:"name"`
	// Verdict is OK, NO or UNKNOWN. UNKNOWN is the absence of a fact and
	// is deliberately not a polite spelling of NO — a check that could not
	// run must never read as a check that answered no.
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

func doctorEnvelope(f doctorFacts, checks []doctorCheck, exitCode int) commandEnvelope {
	report := doctorReport{
		ConfigSource:    f.ConfigSource,
		ConfigDefaulted: f.ConfigSource == "",
		StateDir:        f.StateDir,
		Checks:          make([]doctorCheckJSON, 0, len(checks)),
		// Never nil: an empty list is "every check ran", which is a
		// different statement from "this field was omitted".
		ChecksCannotRun: []string{},
	}
	for _, c := range checks {
		report.Checks = append(report.Checks, doctorCheckJSON{
			Name:    c.Name,
			Verdict: string(c.Verdict),
			Detail:  c.Detail,
			Fix:     c.Fix,
		})
		if c.Verdict == verdictUnknown {
			report.ChecksCannotRun = append(report.ChecksCannotRun, c.Name)
		}
	}

	code, retryable, action := "ok", false, actionNone
	if exitCode != exitOK {
		// The exit status reports whether the DIAGNOSIS succeeded, not
		// whether the news is good — NO is a successful run. Only a report
		// that could not be produced at all is a failure.
		code, retryable, action = "diagnosis_unavailable", true, actionRetry
	}
	return commandEnvelope{
		machineHeader: newMachineHeader("doctor", exitCode, code, retryable, action),
		Data:          report,
	}
}

// ── status ──────────────────────────────────────────────────────────────

type statusReport struct {
	// ConfigSource/ConfigDefaulted/StateDir: see doctorReport's fields of
	// the same name (ruling D-R1).
	ConfigSource    string           `json:"config_source,omitempty"`
	ConfigDefaulted bool             `json:"config_defaulted"`
	StateDir        string           `json:"state_dir,omitempty"`
	Agent           *statusAgentJSON `json:"agent,omitempty"`
	AgentErr        string           `json:"agent_error,omitempty"`
	Update          statusUpdateJSON `json:"update"`
}

// statusUpdateJSON is the daily release check (update_check.go): the mode
// in force, and the stamp's tokens when one is on file. Error is the
// client's own sentence for a stamp it refused, never the stamp's text.
type statusUpdateJSON struct {
	Mode        string `json:"mode"`
	UnknownMode bool   `json:"unknown_mode,omitempty"`
	// Checked is whether a check has run for this copy: a stamp about another
	// binary or version says nothing here, as in the text report.
	Checked   bool     `json:"checked"`
	CheckedAt string   `json:"checked_at,omitempty"`
	Running   string   `json:"running,omitempty"`
	Latest    string   `json:"latest,omitempty"`
	Held      string   `json:"held,omitempty"`
	Outcome   string   `json:"outcome,omitempty"`
	Failure   string   `json:"failure,omitempty"`
	Refresh   string   `json:"refresh,omitempty"`
	Recovery  []string `json:"recovery,omitempty"`
	Fix       string   `json:"fix,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func statusUpdateJSONOf(f updateFacts) statusUpdateJSON {
	out := statusUpdateJSON{Mode: f.Mode, UnknownMode: f.UnknownMode != ""}
	if f.Err != nil {
		out.Error = boundMessage(f.Err.Error())
		return out
	}
	if !f.thisCopy() {
		return out
	}
	st := f.Stamp
	out.Checked = true
	out.CheckedAt = st.CheckedAt.UTC().Format(time.RFC3339)
	out.Running, out.Latest, out.Held, out.Outcome, out.Failure, out.Refresh = st.Running, st.Latest, st.Held, string(st.Outcome), st.Failure, st.Refresh
	switch {
	case stampNeedsAPerson(st):
		out.Recovery, out.Fix = f.Recovery, manualInterventionFix
	case st.Outcome == updateAvailable:
		out.Fix = f.Fix
	case st.Outcome == updateUpgraded && st.Refresh == refreshFailed:
		out.Fix = "jevlin agents install"
	}
	return out
}

// statusAgentJSON is the stored registration. Every field in it is already
// printed by the text report or shown to the participant at claim time;
// the agent API key is held elsewhere and is not read here.
type statusAgentJSON struct {
	AgentID        string   `json:"agent_id,omitempty"`
	Status         string   `json:"status"`
	Claimed        bool     `json:"claimed"`
	Scopes         []string `json:"scopes,omitempty"`
	ClaimURL       string   `json:"claim_url,omitempty"`
	ClaimCode      string   `json:"claim_code,omitempty"`
	ClaimExpiresAt string   `json:"claim_expires_at,omitempty"`
}

func statusEnvelope(f agentIdentityFacts) commandEnvelope {
	report := statusReport{
		ConfigSource:    f.ConfigSource,
		ConfigDefaulted: f.ConfigSource == "",
		StateDir:        f.StateDir,
		Update:          statusUpdateJSONOf(f.Update),
	}
	switch {
	case f.StoreErr != nil:
		report.AgentErr = boundMessage(f.StoreErr.Error())
	case f.RegistrationErr != nil:
		report.AgentErr = boundMessage(f.RegistrationErr.Error())
	case f.HasRegistration:
		reg := f.Registration
		claimURL, claimCode := printableClaimArtifacts(f.Claim, f.PlatformBaseURL)
		report.Agent = &statusAgentJSON{
			AgentID:        reg.AgentID,
			Status:         reg.Status,
			Claimed:        reg.Status == "claimed",
			Scopes:         reg.Scopes,
			ClaimURL:       claimURL,
			ClaimCode:      claimCode,
			ClaimExpiresAt: reg.ClaimExpiresAt,
		}
	}
	return commandEnvelope{
		machineHeader: newMachineHeader("status", exitOK, "ok", false, actionNone),
		Data:          report,
	}
}

// ── connect ─────────────────────────────────────────────────────────────

type connectReport struct {
	AgentID        string   `json:"agent_id,omitempty"`
	Status         string   `json:"status,omitempty"`
	Claimed        bool     `json:"claimed"`
	Scopes         []string `json:"scopes,omitempty"`
	ClaimURL       string   `json:"claim_url,omitempty"`
	ClaimCode      string   `json:"claim_code,omitempty"`
	ClaimExpiresAt string   `json:"claim_expires_at,omitempty"`
}

// connectEnvelope reads back what the run persisted rather than narrating
// what it did. The store is the authority on the registration — that is
// the whole reason connect writes it before printing anything — so an
// envelope built from it says what is true now, including after a run that
// failed partway.
//
// Nothing here reads or exposes the agent API key, and nothing opens the
// claim URL: it is printed for a person, exactly as the text path prints
// it.
func connectEnvelope(cfgPath string, getenv func(string) string, exitCode int, narration string) commandEnvelope {
	report := connectReport{}
	cfg, _, err := loadConfig(cfgPath, getenv)
	if err == nil {
		if store, serr := auth.OpenStoreExisting(cfg.Search.StateDir); serr == nil {
			if reg, ok, rerr := store.LoadAgentRegistration(); rerr == nil && ok {
				report.AgentID = reg.AgentID
				report.Status = reg.Status
				report.Claimed = reg.Status == "claimed"
				report.Scopes = reg.Scopes
				// A run that did not finish carries no claim link: it may have
				// withheld the link (the platform could not say whose the
				// stored key is), and the envelope must not show what the run
				// said it would not.
				if exitCode == exitOK {
					claim, _ := claimFor(cfg.Search, reg.AgentID, nil)
					report.ClaimURL, report.ClaimCode = printableClaimArtifacts(claim, cfg.Platform.BaseURL)
				}
				report.ClaimExpiresAt = reg.ClaimExpiresAt
			}
		}
	}

	code, retryable, action := connectOutcome(exitCode, report.Status)
	env := commandEnvelope{
		machineHeader: newMachineHeader("connect", exitCode, code, retryable, action),
		Data:          report,
	}
	if exitCode != exitOK {
		// The run's own diagnostics, bounded and redacted. Diagnostic
		// only: the header above is what a caller branches on.
		if msg := strings.TrimSpace(narration); msg != "" {
			env.Error = &machineError{Message: boundMessage(msg), Source: "client"}
		}
	}
	return env
}

func connectOutcome(exitCode int, status string) (code string, retryable bool, action string) {
	switch {
	case exitCode == exitTransport:
		return "connect_failed", true, actionRetry
	case exitCode != exitOK:
		return "connect_failed", false, actionReport
	case status == "claimed":
		return "ok", false, actionNone
	case status == "expired":
		// A new registration is needed, and only a person can start one.
		return "registration_expired", false, actionConnect
	case status == "unclaimed":
		// The registration exists and search already works; what is
		// outstanding is a human visiting the claim URL. Still action
		// connect: that action names the registration/claim workflow, not
		// the absence of a registration.
		return "unclaimed", false, actionConnect
	default:
		return "ok", false, actionNone
	}
}

// printableClaimArtifacts is the claim URL and code on file beside
// credentials.json as a report may show them: both dropped together when
// the URL fails invariant 12, since a code is only meaningful beside the
// link it was issued with.
func printableClaimArtifacts(claim auth.ClaimBootstrap, baseURL string) (claimURL, claimCode string) {
	if printableClaimURL(claim.ClaimURL, baseURL, nil) == "" {
		return "", ""
	}
	return claim.ClaimURL, claim.ClaimCode
}
