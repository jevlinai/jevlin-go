// Command jevlin is the supersearch client for coding agents.
//
// It is not a daemon, not a proxy, and not a tool server. It is one
// binary that a coding agent runs to search the web through the Jevlin
// search router, plus the hooks that thread each search into the agent's
// trajectory. Between tool calls nothing runs. Mining moved off the client
// to the platform: nothing here records, spools or submits anything about
// a search.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// version is set at release time via:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

// usageText is rendered once, at init, from usageTemplate: the agents
// section's host labels and -client ids come from targetsByKind(targetHost)
// rather than being hand-typed a second time, so a host can never be
// implemented and left out of top-level help the way usageText and
// agentsUsage's -client list once could drift from each other independently.
var usageText = renderUsageText()

// hostLabelList and hostIDList reproduce the exact wrapping the hand-written
// help text used for six hosts — a line break after the second entry — so
// the registry-derived render is byte-identical to what came before it.
// They are not a general word-wrapper: the wrap point is fixed, the way the
// original hand-wrapped text was, and a host added or removed changes it
// only by changing what is being wrapped, which is exactly the point.
func hostLabelList(labels []string) string {
	return labels[0] + ", " + labels[1] + ",\n             " +
		strings.Join(labels[2:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

func hostIDList(ids []string) string {
	return ids[0] + ", " + ids[1] + ",\n             " + strings.Join(ids[2:], ", ")
}

func renderUsageText() string {
	hosts := targetsByKind(targetHost)
	labels := make([]string, len(hosts))
	ids := make([]string, len(hosts))
	for i, h := range hosts {
		labels[i] = h.Label()
		ids[i] = h.ID()
	}
	r := strings.NewReplacer(
		"{{HOST_LABELS}}", hostLabelList(labels),
		"{{HOST_IDS}}", hostIDList(ids),
	)
	return r.Replace(usageTemplate)
}

const usageTemplate = `usage: jevlin <command> [flags]

the tool (what an agent runs):
  search     one supersearch through the router (every engine in the set
             in one call, each result attributed), two forms: jevlin
             search [-tier fast] [-format json|model] [-view merged]
             <query words> for a person, or jevlin search --stdin for an
             agent (one version-1 JSON request object on stdin, one JSON
             envelope on stdout; -format and -view are ignored, the
             envelope is always JSON and the request's own view field
             decides). The --stdin request may also carry tier, recency,
             domain_filter, max_results, providers, view ("full" or
             "merged"), objective and search_queries; a malformed value
             answers fix_input before any router call. --stdin always asks
             the router for its merged view, which is result.merged; view
             "merged" leaves the candidates out of the envelope. The
             human form's -view merged adds the router's merged and
             indexes to the raw response.
             -timeout bounds the whole search in either form, default
             1m0s. Exit: 0=valid search response, 1=transport/timeout/
             cancel, 2=usage, 3=HTTP 4xx, 4=HTTP 5xx or invalid server response.
  fetch      jevlin fetch --stdin: the full text of pages a search
             returned, for an agent. One version-1 JSON request on stdin:
             pages (1 to 5, each a source_id from a search's result or a
             url), and optionally max_chars (1000 to 50000), offset and
             query; one JSON envelope on stdout with result.pages, each
             with its status, content and next_offset. The router serves
             only pages from this account's recent searches, and only to an
             account that can fetch. -timeout as for search. Exit codes as
             for search.
  agents     agents install|status|uninstall — find {{HOST_LABELS}} on this machine and give each
             the supersearch skill and the hooks it supports. -dry-run previews,
             -client <name> picks one ({{HOST_IDS}}). -yes skips the Proceed?
             prompt, never a question about a Codex setting of yours.
             agents prefer on|off — whether this search or the agent's own
             is the default (off keeps this one for when you name it);
             in the agent, /jevlin off and /jevlin on do the same
             agents tier fast|balanced|max|default — the tier every search
             carries unless the request names one; in the agent,
             /jevlin tier <tier> does the same
  hook       internal: the hook runner the agents call around a search;
             JEVLIN_TRACE=off turns traces off entirely

onboarding (one-time, per agent): register with the search platform, claim
it at one URL, done. search works the moment connect stores the key,
before the claim.
  setup      everything after the binary, asked as it goes — reuse a
             previous installation, write or migrate the config, connect,
             the shell profile (PATH and JEVLIN_CONFIG; Windows: the user
             PATH and JEVLIN_CONFIG) and the coding agents found here.
             -yes answers the profile and agents questions, terminal or not,
             never a question about a Codex setting of yours
             -yes may come from automated callers
             -yes never adopts a set-aside installation without a terminal
             -no-profile leaves the shell profile (Windows: user environment)
             alone; -yes does not override it
             -no-agents skips coding-agent detection; -with still sets up
             what it names
             -dry-run changes nothing
             -with <id> sets up one target whether or not it was found,
             repeatable
  uninstall  take out what setup put here for one installation (-home dir):
             the agents' skills, hooks and plugins that run its binary, and
             the shell-profile block (Windows: the user PATH entry and
             JEVLIN_CONFIG). The registration, stored key and config stay;
             nothing is revoked.
             -binary also removes the installation's own binary and its
             .previous (an npm copy is npm's to remove; on Windows the
             running binary is moved aside, not deleted). -purge-state
             also destroys the participant state after you type the
             installation path at a terminal; -yes never answers it.
             -dry-run changes nothing
  upgrade    replace this native binary with the latest canonical release
             (-version X.Y.Z: exactly that one, never older), verified by
             checksum and by running it before and after it is installed;
             the replaced binary is kept as <binary>.previous. -rollback
             puts that one back, with no network. An npm copy is updated
             with npm install -g @jevlinsearch/jevlin@latest
             -auto [-config file] is internal: the daily release check the
             hooks start; jevlin upgrade -h says what it does
  connect    register [-name …]; prints the claim URL and code and polls
             (bounded) until claimed. A second run resumes; so does the
             next search, automatically. -json emits one JSON envelope
             instead of narration
  login      store your sr- key for searches: reads it from stdin (or
             -key-env VAR), checks it against the router without spending,
             writes ~/.jevlin/credentials.json owner-only. -show says
             where a search would get its key; -forget removes the file
  status     report what this installation has and has not completed.
             -json reports as one JSON object instead of text

is it working:
  doctor     checks in a participant's terms — the stored search key, the
             registration, the trace key, whether this copy is the latest
             release, the binary on PATH and, on Windows, state access —
             each saying what to do. Exits non-zero only when every check
             came back UNKNOWN: a NO is a successful diagnosis. "state
             access" says whose the state directory and its credentials
             are and names anyone else its access list admits, which an
             agent's sandbox is expected to be; it reports and changes
             nothing. -json reports as one JSON object instead of text
  limits     this key's spend caps and today's spend against the
             effective ceiling, read from the router; reading spends
             nothing and works even at the ceiling. Setting caps is not
             built yet. -json emits one machine envelope

A command that reads the config takes -config <file> (setup, upgrade and
uninstall take -home instead), falling back to JEVLIN_CONFIG, then the
installation's own config ($JEVLIN_HOME/jevlin.toml, else
~/.jevlin/jevlin.toml, when that file exists). A jevlin.toml in the
working directory is never read unless named.
status and doctor name the file they resolved, or say plainly that none was
found and built-in defaults are in use; connect refuses outright when
resolution finds no config file at all, and says to run jevlin setup.
The [miner] block names the router and the directories this installation
keeps; [platform] names two hosts — base_url, the portal a printed claim
URL is checked against and nothing dials (platform.jevlin.ai), and
agents_api_url, where connect actually sends register/status
(agents-v1.jevlin.ai). Searches take your sr- key from JEVLIN_API_KEY if it
is set, else from the file login (or connect) wrote. Otherwise run connect
or login to set up search.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	os.Exit(dispatch(os.Args[1], os.Args[2:]))
}

func dispatch(name string, args []string) int {
	switch name {
	case "search":
		return cmdSearch(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "fetch":
		return cmdFetch(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "agents":
		return cmdAgents(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "hook":
		return cmdHook(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "turn-end":
		// Started by a hook, detached; not a command a person runs.
		return cmdTurnEnd(args, os.Getenv)
	case "login":
		return cmdLogin(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "setup":
		return cmdSetup(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "uninstall":
		return cmdUninstall(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "upgrade":
		return cmdUpgrade(args, os.Stdout, os.Stderr, os.Getenv)
	case "connect":
		return cmdConnect(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "status":
		return cmdStatus(args)
	case "doctor":
		return cmdDoctor(args, os.Stdout, os.Stderr)
	case "limits":
		return cmdLimits(args, os.Stdout, os.Stderr, os.Getenv)
	case "version", "-version", "--version":
		fmt.Fprintln(os.Stdout, "jevlin", buildVersion())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usageText)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "jevlin: unknown command %q\n\n%s", name, usageText)
		return 2
	}
}

// clientIdentifier is the one spelling of "which build is speaking" sent
// upstream: the User-Agent on every router call, and register's client
// field. "jevlin/0.1.0" for a release, "jevlin/dev (abc123def456)" for a
// local build — inside the router's 128-byte bound either way.
func clientIdentifier() string {
	return searchUserAgent + "/" + strings.TrimPrefix(buildVersion(), "v")
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	rev, dirty := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return version + " (" + rev + dirty + ")"
}
