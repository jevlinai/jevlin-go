package main

// jevlin limits — the spend caps and today's spend, read from the search
// router (GET /v1/limits; the router's skill file, "Spend limits").
//
// Read-only on purpose. The POST half (setting caps) waits on a wire
// question the skill file leaves open — it says both that an omitted
// window on POST means "no cap" and that POST is a partial update, and a
// client built on the wrong reading silently rewrites a participant's
// money controls. Reading spends nothing and works even when the account
// is at its ceiling or out of credit, which is exactly when a participant
// runs this.
//
// The request carries the participant's sr- key to router_url — the same
// exposure as a search, so the same shape: the origin configured, never
// discovered (invariant 5), same-origin redirects only (invariant 3), the
// body read bounded. The response is the router's product API and is
// decoded permissively (invariant 6); every cap is a pointer because an
// explicit 0 means "no cap" while an absent field means "the router did
// not say", and the machine envelope must not invent one from the other.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

const limitsTimeout = 30 * time.Second

// limitsReport is as much of GET /v1/limits as this client reads. The
// three caps and the three today fields are integer micro-dollars
// (1_000_000 = $1).
type limitsReport struct {
	DailyMicros   *int64       `json:"daily_micros,omitempty"`
	WeeklyMicros  *int64       `json:"weekly_micros,omitempty"`
	MonthlyMicros *int64       `json:"monthly_micros,omitempty"`
	Today         *limitsToday `json:"today,omitempty"`
}

type limitsToday struct {
	SpentMicros     *int64 `json:"spent_micros,omitempty"`
	CeilingMicros   *int64 `json:"ceiling_micros,omitempty"`
	RemainingMicros *int64 `json:"remaining_micros,omitempty"`
}

func cmdLimits(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	machine := jsonRequested(args)
	if machine {
		stderr = io.Discard
	}
	fs := newFlagSet("limits", stderr)
	cfgPath := fs.String("config", "", "path to TOML config file")
	fs.Bool("json", false, "emit one machine envelope instead of the table")
	if err := fs.Parse(args); err != nil {
		if machine {
			return failLimits(stdout, exitUsage, codeInvalidFlags, actionFixInput, errors.New("the flags could not be parsed"))
		}
		return exitUsage
	}
	if fs.NArg() != 0 {
		if machine {
			return failLimits(stdout, exitUsage, codeUnexpectedArgument, actionFixInput, errors.New("limits takes no arguments"))
		}
		fmt.Fprintln(stderr, "jevlin limits: no arguments; it reads this key's caps and today's spend")
		return exitUsage
	}

	cfg, cfgSource, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		if machine {
			return failLimits(stdout, exitTransport, "config_unreadable", actionFixInput, err)
		}
		fmt.Fprintf(stderr, "jevlin: config (%s): %v\n", orDefaults(cfgSource), err)
		return exitTransport
	}
	if cfg.Miner.RouterURL == nil {
		if machine {
			return failLimits(stdout, exitTransport, "no_router_configured", actionFixInput,
				errors.New("no router configured (miner.router_url or a [[provider]] upstream)"))
		}
		fmt.Fprintln(stderr, "jevlin: no router configured (miner.router_url or a [[provider]] upstream)")
		return exitTransport
	}
	key, keySrc, err := resolveAPIKey(getenv, cfg.Miner)
	if err != nil {
		if machine {
			return failLimits(stdout, exitClientErr, "credential_unreadable", actionConnect, err)
		}
		fmt.Fprintln(stderr, "jevlin:", err)
		return exitClientErr
	}
	if key == "" {
		if machine {
			return failLimits(stdout, exitClientErr, "no_credential", actionConnect,
				errors.New("this installation holds no search credential; run jevlin connect"))
		}
		fmt.Fprintln(stderr, "jevlin: no API key; run jevlin connect, or store one with jevlin login")
		return exitClientErr
	}

	endpoint := strings.TrimRight(cfg.Miner.RouterURL.String(), "/") + "/v1/limits"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		if machine {
			return failLimits(stdout, exitTransport, "transport_error", actionRetry, err)
		}
		fmt.Fprintln(stderr, "jevlin: router:", err)
		return exitTransport
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", searchUserAgent+"/"+strings.TrimPrefix(buildVersion(), "v"))
	req.Header.Set("Authorization", "Bearer "+key)
	// The same client shape as the login probe, for the same reason: this
	// request carries the sr- key in Authorization, so redirects are
	// same-origin bounded, never net/http's follow-10 default.
	resp, err := (&http.Client{Timeout: limitsTimeout, CheckRedirect: auth.SameOriginRedirects, Transport: loginProbeTransport}).Do(req)
	if err != nil {
		if machine {
			return failLimits(stdout, exitTransport, "transport_error", actionRetry, err)
		}
		fmt.Fprintln(stderr, "jevlin: router:", err)
		return exitTransport
	}
	defer func() { _ = resp.Body.Close() }()
	raw, bodyErr := readBoundedBody(resp.Body, searchMaxBody)
	if bodyErr != nil {
		if machine {
			return failLimits(stdout, exitServerErr, "invalid_router_response", actionReport, bodyErr)
		}
		fmt.Fprintln(stderr, "jevlin: router answer unusable:", bodyErr)
		return exitServerErr
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code := "router_rejected"
		if e, ok := decodeSearchHostError(raw); ok {
			if c, isCode := machineCodeLike(e.Code); isCode {
				code = c
			}
		}
		switch {
		case resp.StatusCode >= 500:
			if machine {
				return failLimits(stdout, exitServerErr, code, actionRetry, fmt.Errorf("the router answered HTTP %d", resp.StatusCode))
			}
			fmt.Fprintf(stderr, "jevlin: HTTP %d\n", resp.StatusCode)
			return exitServerErr
		case resp.StatusCode == http.StatusUnauthorized:
			if machine {
				return failLimits(stdout, exitClientErr, code, actionLogin, errors.New("the router refused the key"))
			}
			fmt.Fprintf(stderr, "jevlin: HTTP %d — the router refused the key (from %s); store a valid one with: jevlin login\n", resp.StatusCode, keySrc)
			return exitClientErr
		default:
			if machine {
				return failLimits(stdout, exitClientErr, code, actionReport, fmt.Errorf("the router answered HTTP %d", resp.StatusCode))
			}
			fmt.Fprintf(stderr, "jevlin: HTTP %d\n", resp.StatusCode)
			return exitClientErr
		}
	}

	var report limitsReport
	if err := json.Unmarshal(raw, &report); err != nil {
		if machine {
			return failLimits(stdout, exitServerErr, "invalid_router_response", actionReport, errors.New("the limits response is not JSON"))
		}
		fmt.Fprintln(stderr, "jevlin: the limits response is not JSON:", err)
		return exitServerErr
	}

	if machine {
		emitMachine(stdout, commandEnvelope{
			machineHeader: newMachineHeader("limits", exitOK, "ok", false, actionNone),
			Data:          report,
		})
		return exitOK
	}
	printLimits(stdout, report)
	return exitOK
}

func failLimits(stdout io.Writer, exitCode int, code, action string, err error) int {
	emitMachine(stdout, commandEnvelope{
		machineHeader: newMachineHeader("limits", exitCode, code, action == actionRetry, action),
		Error:         clientMessage(err),
	})
	return exitCode
}

// printLimits renders the caps and the day's ledger for a person. A cap
// of 0 and an unset cap are both "none" — 0 is the wire's spelling of
// "no cap on this window" — while a field the router omitted entirely is
// "not reported", because inventing a number the router did not send is
// how a participant budgets against a fiction.
func printLimits(stdout io.Writer, r limitsReport) {
	capOf := func(v *int64) string {
		if v == nil {
			return "not reported"
		}
		if *v == 0 {
			return "none"
		}
		return dollars(*v)
	}
	fmt.Fprintln(stdout, "spend caps (calendar UTC windows):")
	fmt.Fprintln(stdout, "  daily    "+capOf(r.DailyMicros))
	fmt.Fprintln(stdout, "  weekly   "+capOf(r.WeeklyMicros))
	fmt.Fprintln(stdout, "  monthly  "+capOf(r.MonthlyMicros))
	if t := r.Today; t != nil && (t.SpentMicros != nil || t.CeilingMicros != nil || t.RemainingMicros != nil) {
		val := func(v *int64) string {
			if v == nil {
				return "not reported"
			}
			return dollars(*v)
		}
		fmt.Fprintf(stdout, "today: spent %s — effective ceiling %s — remaining %s\n",
			val(t.SpentMicros), val(t.CeilingMicros), val(t.RemainingMicros))
	} else {
		fmt.Fprintln(stdout, "today: not reported")
	}
	fmt.Fprintln(stdout, "(the effective ceiling is the lowest of the credit balance and the caps; day resets at UTC midnight, week on Monday, month on the 1st)")
}

// dollars renders integer micro-dollars exactly: no floats, at least two
// decimals, trailing zeros beyond them trimmed. 1_000_000 = $1.
func dollars(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	whole, frac := micros/1_000_000, micros%1_000_000
	s := fmt.Sprintf("%06d", frac)
	for len(s) > 2 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	return fmt.Sprintf("%s$%d.%s", sign, whole, s)
}
