// Command jevlin is the supersearch client for coding agents.
//
// It is not a daemon and not a proxy. It is one binary that a coding agent
// runs to search the web through the Jevlin search router, plus the hooks
// that thread each search into the agent's trajectory. `jevlin mcp` serves
// the same searches as MCP tools, and runs only while a host holds it open.
// Between tool calls nothing else runs, and nothing here records, spools or
// submits anything about a search.
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	os.Exit(dispatch(os.Args[1], os.Args[2:]))
}

func dispatch(name string, args []string) int {
	if code, ok := helpRequested(name, args, os.Stdout, os.Stderr); ok {
		return code
	}
	switch name {
	case "search":
		return cmdSearch(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "fetch":
		return cmdFetch(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "decide":
		return cmdDecide(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	case "mcp":
		return cmdMCP(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv)
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
