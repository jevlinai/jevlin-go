package main

import (
	"fmt"
	"io"
	"strings"
)

// The help text, in one shape for every command: a usage line, a short
// description, the flags as a table, and examples; and for the commands an
// agent calls, the request and the exit status. The top-level help is an
// index of one line per command; the detail lives in each command's own help
// and in `jevlin help config`, because a person who asked for the index was
// looking for a command, not for every field of a JSON request. Every line
// fits in 80 columns, and help_test.go holds each text to this shape.

// usageText is what `jevlin help` and `jevlin -h` print.
const usageText = `jevlin: supersearch for coding agents

usage: jevlin <command> [flags]
       jevlin help <command>    the help for one command
       jevlin help config       which config a command reads, and what it holds

search:
  search     search every engine at once; each result names its engines
  fetch      read in full the pages a search returned (agents, --stdin)
  decide     ask a decision model typed questions (agents, --stdin)
  mcp        serve search, fetch and decide as MCP tools

set up:
  setup      the config, the registration and your coding agents, in one go
  connect    register this machine and print the claim link
  login      store an sr- key you already have
  agents     add, check or remove the skill and hooks in each coding agent
  upgrade    replace this binary with the latest release, or roll back
  uninstall  remove what setup added; -purge-state destroys the key too

check:
  status     what this installation has set up, and whether it is claimed
  doctor     checks in plain language, each saying what to do
  limits     this key's spend caps and today's spend
  version    print this binary's version

examples:
  jevlin setup
  jevlin search how to rotate a postgres password
  jevlin help search

Searches work as soon as setup (or connect) stores the key, before the claim.
`

// helpCommands is the index's commands, in its order: each has a help of
// its own, and help_test.go holds the two to each other.
var helpCommands = []string{
	"search", "fetch", "decide", "mcp",
	"setup", "connect", "login", "agents", "upgrade", "uninstall",
	"status", "doctor", "limits", "version",
}

// commandHelp is the help `jevlin help <name>` and `jevlin <name> -h` print.
// "config" is a topic rather than a command, and "hook" is internal: it is
// answered here but left out of the index.
func commandHelp(name string) (string, bool) {
	switch name {
	case "search":
		return searchUsage, true
	case "fetch":
		return fetchUsage, true
	case "decide":
		return decideUsage, true
	case "mcp":
		return mcpUsage, true
	case "setup":
		return renderSetupUsage(), true
	case "connect":
		return connectUsage, true
	case "login":
		return loginUsage, true
	case "agents":
		return agentsUsage, true
	case "upgrade":
		return upgradeUsage, true
	case "uninstall":
		return uninstallUsage, true
	case "status":
		return statusHelp, true
	case "doctor":
		return doctorUsage, true
	case "limits":
		return limitsUsage, true
	case "version":
		return versionUsage, true
	case "config":
		return configHelp, true
	case "hook":
		return hookUsage, true
	}
	return "", false
}

// isHelpFlag is the spelling of a request for help that every command takes.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// helpRequested answers `jevlin help [name]` and `jevlin <name> -h`: help
// asked for goes to stdout and exits 0. It reports false for anything else,
// which the command then runs as usual. The hook is left to its own runner
// when it is called with -h, because a coding agent is what runs it; only
// `jevlin help hook` describes it.
func helpRequested(name string, args []string, stdout, stderr io.Writer) (int, bool) {
	if name == "help" || isHelpFlag(name) {
		if len(args) == 0 {
			fmt.Fprint(stdout, usageText)
			return exitOK, true
		}
		if h, ok := commandHelp(args[0]); ok && len(args) == 1 {
			fmt.Fprint(stdout, h)
			return exitOK, true
		}
		fmt.Fprintf(stderr, "jevlin help: no help for %q; jevlin help lists the commands\n", strings.Join(args, " "))
		return exitUsage, true
	}
	if name == "agents" && len(args) == 2 && isHelpFlag(args[1]) {
		// agents install -h and the rest: the subcommands share one help.
		args = args[1:]
	}
	if name == "hook" || len(args) != 1 || !isHelpFlag(args[0]) {
		return 0, false
	}
	h, ok := commandHelp(name)
	if !ok {
		return 0, false
	}
	fmt.Fprint(stdout, h)
	return exitOK, true
}

// flagSetUsage is what a command's flag set prints when its flags do not
// parse: the command's own help, never the flag package's listing of
// defaults, which no participant can read a command from.
func flagSetUsage(name string, w io.Writer) func() {
	return func() {
		if h, ok := commandHelp(name); ok {
			fmt.Fprint(w, h)
			return
		}
		fmt.Fprintln(w, "jevlin help lists the commands")
	}
}

const searchUsage = `usage: jevlin search [flags] <query words>
       jevlin search --stdin [-config file] [-timeout duration]

Searches every engine in the set in one call and answers with the results,
each naming the engines that found it. The first form is for a person and
prints the router's answer. The --stdin form is for an agent: one version-1
JSON request on stdin, one JSON envelope on stdout.

flags:
  -tier name          fast, balanced or max; balanced and max need a claimed
                      agent (default: the tier jevlin agents tier saved, else
                      [router] tier, else the router's own)
  -format json|model  json prints the router's bytes as they came; model
                      prints compact text for an agent (default json)
  -view full|merged   merged adds the router's merged list and per-engine
                      indexes to the answer (default full, which adds neither)
  -fresh              search live instead of serving a result the router
                      cached in the last 15 minutes; costs the same as any
                      live search
  -timeout duration   the deadline for the whole search (default 1m0s)
  -config file        the config to read (jevlin help config)
  --stdin             read one JSON request on stdin; -format, -view and
                      -fresh do not apply

the --stdin request:
  query (required), and optionally objective, search_queries, tier, recency,
  domain_filter, max_results, providers, view ("merged" or "full"),
  merged_limit, max_chars, page_chars and cache ("refresh"). A value that is
  not valid answers fix_input before the router is called. The envelope's
  result.merged is the router's merged view, and view "merged" leaves the
  candidates out. docs/reference.md describes every field.

exit status:
  0  the router answered with a valid search response
  1  transport failure, timeout or cancel
  2  usage error
  3  HTTP 4xx, or no usable key
  4  HTTP 5xx, or an invalid server response

examples:
  jevlin search rust async http client
  jevlin search -format model -tier balanced postgres 17 release notes
  echo '{"version":1,"query":"go 1.27 release notes"}' | jevlin search --stdin
`

const mcpUsage = `usage: jevlin mcp [-log] [-config file]

Serves search, fetch and decide as MCP tools, and decide --models as
decide_models, over stdio: newline-delimited JSON-RPC on stdin and stdout.
A host that registers it starts it and holds it open; it is not a command
to run by hand. Each tool answers with the envelope --stdin prints.

flags:
  -log          write one line per message to stderr: the method, the tool
                and the outcome, never what a request carried
  -config file  the config to read (jevlin help config)

example, registering it with Claude Code:
  claude mcp add jevlin -- jevlin mcp
`

const connectUsage = `usage: jevlin connect [-name name] [-force] [-json] [-config file]

Registers this machine with the search platform, stores the search key, and
prints the claim link and code; then it waits, up to three minutes, for you
to claim it. Searches work as soon as the key is stored, before the claim.
Run connect again to pick up where it left off; the next search does the
same on its own. jevlin setup runs connect for you.

flags:
  -name name    a name for this agent (optional)
  -force        replace a stored key that belongs to another registration
  -json         print one JSON envelope instead of narration
  -config file  the config to read; connect needs one (jevlin help config)

examples:
  jevlin connect
  jevlin connect -json
`

const loginUsage = `usage: jevlin login [-key-env VAR] [-no-verify] [-config file]
       jevlin login -show | -forget [-config file]

Stores an sr- key you already have, for searches. The key is read from
stdin, or from the variable -key-env names, never from an argument; it is
checked against the router without spending, and written to a file only
you can read. jevlin connect stores a key for you; login is for a key you
were given.

flags:
  -key-env VAR  read the key from this environment variable, not stdin
  -no-verify    store the key without checking it against the router
  -show         say where a search would get its key, masked
  -forget       remove the stored key file
  -config file  the config to read (jevlin help config)

examples:
  jevlin login < key.txt
  jevlin login -show
`

const statusHelp = `usage: jevlin status [-json] [-config file]

Says what this installation has set up: the config and state directory it
uses, whether a newer release is out, and whether the agent is registered
and claimed. It reads only what is stored here: after you claim the agent
on the website, it shows the claim once a search or jevlin connect has run.

flags:
  -json         print one JSON object instead of text
  -config file  the config to read (jevlin help config)

examples:
  jevlin status
  jevlin status -json
`

const doctorUsage = `usage: jevlin doctor [-json] [-config file]

Runs plain-language checks, each saying what to do: the stored search key,
the registration, the trace key, whether this copy is the latest release,
the binary on PATH and, on Windows, state access. It changes nothing.

State access says whose the state directory and its credentials are, and
names anyone else its access list admits, as an agent's sandbox is expected
to be.

flags:
  -json         print one JSON object instead of text
  -config file  the config to read (jevlin help config)

exit status:
  non-zero only when every check came back UNKNOWN; a NO is a successful
  diagnosis

examples:
  jevlin doctor
  jevlin doctor -json
`

const limitsUsage = `usage: jevlin limits [-json] [-config file]

Prints this key's daily, weekly and monthly spend caps, as the router
reports them, and today's spend against the effective ceiling: the lowest
of the credit balance and the caps. Reading spends nothing and works even
at the ceiling. A day resets at UTC midnight, a week on Monday, a month on
the 1st. Setting caps is not built yet.

flags:
  -json         print one JSON envelope instead of the table
  -config file  the config to read (jevlin help config)

examples:
  jevlin limits
  jevlin limits -json
`

const versionUsage = `usage: jevlin version

Prints this binary's version: a release prints its number, and a build from
source prints dev and the commit it was built from.

example:
  jevlin version
`

const hookUsage = `usage: jevlin hook <event> ...

Internal: the hook the coding agents run around a search, written into
their settings by jevlin agents install. It is not a command to run by hand,
and it prints nothing and exits 0 when anything goes wrong.
JEVLIN_TRACE=off turns traces off entirely.
`

const configHelp = `usage: jevlin help config

Which config a command reads, the first that applies:
  1. -config file, when given (setup, upgrade and uninstall take -home dir)
  2. JEVLIN_CONFIG
  3. the installation's own: $JEVLIN_HOME/jevlin.toml, else
     ~/.jevlin/jevlin.toml, when that file exists
  4. the built-in defaults
A jevlin.toml in the working directory is never read unless named. status
and doctor print the file they used, or say that none was found and the
built-in defaults are in use. connect refuses to run with no config file,
and says to run jevlin setup.

What it holds, as configuration format 1 (every key is optional):
  schema = 1
  [router]    url    the router every search goes to (api.jevlin.ai)
              tier   the tier a search carries when jevlin agents tier
                     saved none
  [platform]  url    the portal a printed claim link must be on; it is
                     never contacted (platform.jevlin.ai)
  [agents]    url    where connect registers and checks the claim
                     (agents-v1.jevlin.ai)
  [search]    state_dir, sessions_dir and turn_end
  [update]    mode   "apply" (the default), "notify" or "off"
A config with no schema line is from an older jevlin, and every command
says to run jevlin setup, which sets it aside and writes a new one.

environment:
  JEVLIN_CONFIG          the config to read, as above
  JEVLIN_HOME            the installation directory (default ~/.jevlin)
  JEVLIN_ROUTER_URL      replaces [router] url for one run
  JEVLIN_PLATFORM_URL    replaces [platform] url for one run
  JEVLIN_AGENTS_API_URL  replaces [agents] url for one run
  JEVLIN_API_KEY         the sr- key a search sends, instead of the stored
                         one; only to this installation's router
  JEVLIN_TRACE=off       send no trace with any search

Searches use JEVLIN_API_KEY when it is set, else the key connect or login
stored; jevlin login -show says which. docs/reference.md#config has more.
`
