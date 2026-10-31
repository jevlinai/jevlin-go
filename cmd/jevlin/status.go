package main

// `status` — what this installation has and has not completed, read from
// disk alone: the config it resolved, the daily release check's stamp, and
// the platform registration (agent onboarding design §5.5: "status —
// unclaimed | claimed (scopes)"). Nothing here asks the platform or the
// router; the one network-facing report is `doctor`'s.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
	"github.com/jevlinai/jevlin-go/pkg/config"
)

func cmdStatus(args []string) int { return statusMain(args, os.Stdout, os.Stderr, os.Getenv) }

func statusMain(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to TOML config file")
	asJSON := fs.Bool("json", false, "report as one JSON object instead of text")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, src, err := loadConfig(*cfgPath, getenv)
	if err != nil {
		if *asJSON {
			emitMachine(stdout, commandEnvelope{
				machineHeader: newMachineHeader("status", exitTransport, "config_unreadable", false, actionFixInput),
				Error:         clientMessage(err),
			})
			return exitTransport
		}
		fmt.Fprintln(stderr, "jevlin: "+configProblem(src, err))
		return exitTransport
	}

	local := gatherAgentIdentity(cfg, src, getenv)
	if *asJSON {
		emitMachine(stdout, statusEnvelope(local))
		return exitOK
	}
	renderAgentIdentity(local, stdout, stderr)
	return exitOK
}

// agentIdentityFacts is everything `status` reads, with no interpretation
// applied yet.
//
// Gathering and rendering are separate so the JSON report is built from
// the same values the human report prints, rather than from the human
// report itself. A renderer that parsed its own prose back into fields
// would make every wording change a silent data change.
//
// It stays disk-only by design: nothing here asks the platform.
type agentIdentityFacts struct {
	// ConfigSource is the file describeConfigSource resolved (ruling
	// D-R1), empty when resolution found none and built-in defaults are in
	// use — the two are told apart explicitly rather than by an empty
	// string reading as "no config", since printing it is the whole point.
	ConfigSource string
	StateDir     string

	// PlatformBaseURL is what a stored claim URL must match before it is
	// printed (invariant 12): agent.json is sandbox-writable.
	PlatformBaseURL string

	// StoreMissing is the ordinary "nothing has registered here" state;
	// StoreErr is a state directory that exists and cannot be read.
	StoreMissing bool
	StoreErr     error

	Registration    auth.AgentRegistration
	HasRegistration bool
	RegistrationErr error

	// Claim is the claim link on file beside credentials.json for the
	// registration's agent; agent.json carries none (auth.ClaimRecord).
	Claim    auth.ClaimBootstrap
	HasClaim bool

	// Update is the daily release check's stamp and mode (update_check.go),
	// read whenever the config is known: it is about the binary, so it is
	// reported before the store is opened.
	Update updateFacts
	// Now is sampled once by the gatherer, so the age the renderers print
	// for the stamp is one instant.
	Now time.Time
}

func gatherAgentIdentity(cfg *config.Config, src string, getenv func(string) string) agentIdentityFacts {
	f := agentIdentityFacts{ConfigSource: src, StateDir: cfg.Search.StateDir, PlatformBaseURL: cfg.Platform.BaseURL}
	f.Update, f.Now = gatherUpdateFacts(cfg, getenv), time.Now()
	store, err := auth.OpenStoreExisting(cfg.Search.StateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			f.StoreMissing = true
		} else {
			f.StoreErr = err
		}
		return f
	}
	f.Registration, f.HasRegistration, f.RegistrationErr = store.LoadAgentRegistration()
	if f.RegistrationErr != nil {
		return f
	}
	if f.HasRegistration {
		f.Claim, f.HasClaim = claimFor(cfg.Search, f.Registration.AgentID, nil)
	}
	return f
}

func renderAgentIdentity(f agentIdentityFacts, stdout, stderr io.Writer) {
	printConfigSource(stdout, f.ConfigSource, f.StateDir)
	fmt.Fprintf(stdout, "update:  %s\n", updateLineText(f.Update, f.Now))
	if f.StoreMissing {
		fmt.Fprintln(stdout, "agent:   not registered — run `jevlin connect`")
		return
	}
	if f.StoreErr != nil {
		fmt.Fprintf(stderr, "agent:  the state directory could not be read: %v\n", f.StoreErr)
		return
	}
	if f.RegistrationErr != nil {
		// An undecodable agent.json must not read as "never ran connect" —
		// status is exactly where a participant would go looking to
		// understand why connect started refusing. Only a record that is
		// corrupt is what connect rebuilds: one that is a link, not a
		// regular file or readable by others is refused by connect as well,
		// and the advice would send the participant round in a circle.
		if errors.Is(f.RegistrationErr, auth.ErrAgentRegistrationCorrupt) {
			fmt.Fprintf(stderr, "agent:  registration on file could not be read: %v; run `jevlin connect` to rebuild it from the platform\n", f.RegistrationErr)
		} else {
			fmt.Fprintf(stderr, "agent:  registration on file could not be read: %v\n", f.RegistrationErr)
		}
		return
	}
	if !f.HasRegistration {
		fmt.Fprintln(stdout, "agent:   not registered — run `jevlin connect`")
		return
	}

	reg := f.Registration
	switch reg.Status {
	case "unclaimed":
		if !f.HasClaim {
			// No claim link is on file beside credentials.json for this
			// agent: a registration rebuilt from GET /v1/agents/me before
			// the platform served the claim bootstrap fields, or one an
			// older version kept the link for in agent.json, which is not
			// read. Status never falls back to printing a bare URL; a
			// foreground connect mints a fresh one.
			fmt.Fprintln(stdout, "agent:  unclaimed — no claim link is on file for it here. Run `jevlin connect` for a fresh one.")
		} else if claimURL := printableClaimURL(f.Claim.ClaimURL, f.PlatformBaseURL, stderr); claimURL != "" {
			fmt.Fprintf(stdout, "agent:  unclaimed — claim at %s (open it in a browser and sign in)\n", claimURL)
		} else {
			fmt.Fprintln(stdout, "agent:  unclaimed — the stored claim link failed validation and is not shown. "+
				"Run `jevlin connect` for a fresh one.")
		}
	case "expired":
		fmt.Fprintln(stdout, "agent:  expired — run `jevlin connect` again for a new registration")
	case "claimed":
		fmt.Fprintf(stdout, "agent:  claimed (scopes: %s)\n", strings.Join(reg.Scopes, ", "))
	default:
		fmt.Fprintf(stderr, "agent:  unrecognized status %q from a previous poll\n", reg.Status)
	}
}
