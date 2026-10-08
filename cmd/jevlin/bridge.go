package main

// The trace bridge, put on a command in the syntax of the shell that runs it.
//
// A lineage adapter hands the host back the same command with one thing
// added: the envelope, in an environment variable the binary reads. v0.2.9
// wrote one syntax for every host — `JEVLIN_TRACE_BRIDGE=<b> <cmd>`, which
// is POSIX — so on Windows, where opencode runs PowerShell, the prefix was
// looked up as a program name and the search did not run at all (dropin-miner#68). The
// syntax now comes from the host's declared tool shell, and for Claude Code
// from the tool the payload names, because that host runs two.
//
// H-R4, provenance. Only a bridge THIS adapter generated for THIS call may
// carry this adapter's harness, so an adapter never stands down because a
// bridge is already on the command: a model can write one itself, and dropin-miner#68 is
// what that looks like at the router — a trace the model assembled, credited
// to us. Every assignment the adapter RECOGNIZES is removed and its own is
// prepended.
//
// "Recognized" is a syntactically standalone assignment statement in a
// declared shell's syntax and nothing else: a leading NAME=value word in
// POSIX, a standalone `$env:NAME = …` statement in PowerShell, `set NAME=…`
// as its own command in cmd. Never a substring inside quoted text, inside
// another argument, or inside the JSON request body — a query that happens to
// mention the variable is a query, not a bridge. A command still carrying one
// this cannot prove standalone is left exactly as it was found, with no
// lineage claimed for it.
//
// The binary cannot authenticate an environment variable; the client trace
// stays unauthenticated metadata. What this protects is the meaning of the
// harness field.

import (
	"regexp"
	"strings"
)

// bridgeValue is the only value a recognized assignment may carry: the
// base64url alphabet the bridge is encoded in (encodeTraceBridge), with no
// quote, escape or shell metacharacter. A quote-unaware cut through any other
// value can end inside a string the shell would have read as data, and turn
// that data into code. Case-sensitive even under (?i), which in Go would fold
// k and s into U+212A and U+017F where JavaScript's i does not.
const bridgeValue = `(?-i:[A-Za-z0-9_-]*)`

// bridgeAssignmentRe matches one standalone bridge assignment at the START of
// a command, in each declared shell's syntax. It is pinned to the JavaScript
// copy in agent_trace_common.js by TestBridgeGuardsAgree.
var bridgeAssignmentRe = regexp.MustCompile(
	`(?i)^(?:` +
		// POSIX: NAME=value as a leading word.
		bridgeEnv + `=` + bridgeValue + `[ \t\n]+` +
		`|` +
		// PowerShell: $env:NAME = … as its own statement.
		`\$env:` + bridgeEnv + `\s*=\s*(?:'` + bridgeValue + `'|"` + bridgeValue + `"|` + bridgeValue + `)\s*[;\n]\s*` +
		`|` +
		// cmd: set NAME=value as its own command.
		`set\s+` + bridgeEnv + `=` + bridgeValue + `[ \t]*(?:&+|\n)\s*` +
		`)`)

// posixSearchLeadsRe proves an assignment prefix would reach the search:
// after any leading plain NAME=value words, the first simple command of the
// line is the search invocation itself. A value carrying a quote or a
// backslash is not chased — the miss stands the bridge down, which is the
// safe direction, because a declined rewrite only costs threading while a
// wrong one breaks the command. Pinned to the JavaScript copy
// (SEARCH_LEADS_RE in agent_trace_common.js) by TestBridgeGuardsAgree and
// TestTheLeadsGuardsAreOneRegex.
var posixSearchLeadsRe = regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*=[^\s"'\\]*\s+)*` + searchInvocationPattern)

// powerShellWrapperRe matches the try/finally wrapper this adapter writes
// around a PowerShell command, so a command it has already rewritten is
// unwrapped rather than wrapped twice.
var powerShellWrapperRe = regexp.MustCompile(`(?s)^try \{ (.*)\n\} finally \{ Remove-Item Env:` + bridgeEnv + `[^}]*\}$`)

// stripBridgeAssignments removes every bridge assignment provably standalone
// at the front of the command, and unwraps this adapter's own PowerShell
// wrapper when it finds one.
func stripBridgeAssignments(cmd string) string {
	for {
		if loc := bridgeAssignmentRe.FindStringIndex(cmd); loc != nil {
			cmd = cmd[loc[1]:]
			continue
		}
		if m := powerShellWrapperRe.FindStringSubmatch(cmd); m != nil {
			cmd = m[1]
			continue
		}
		return cmd
	}
}

// bridgeMentionRe is the variable's name anywhere, in any ASCII case. The
// name alone, not an assignment shape: PowerShell and cmd read an
// environment variable's name without regard to case, and PowerShell puts
// any run of blanks, a tab, a backtick line continuation or a <# #> comment
// between `$env:NAME` and its `=`, and reaches the same variable through
// ${env:NAME}, Set-Item Env:NAME and [Environment]::SetEnvironmentVariable.
// An assignment shape would have to follow PowerShell's tokenizer to keep up;
// the name does not. The cost is a search whose own text names the variable,
// which runs without lineage. (?i) folds no non-ASCII rune into this name —
// it has no K or S — so it agrees with the JavaScript copy's i flag.
var bridgeMentionRe = regexp.MustCompile(`(?i)` + bridgeEnv)

// carriesUnremovableBridge reports whether the command still mentions the
// bridge somewhere this cannot prove is a standalone assignment.
func carriesUnremovableBridge(cmd string) bool {
	return bridgeMentionRe.MatchString(cmd)
}

// withTraceBridge returns cmd carrying bridge in sh's syntax, and whether the
// command may be rewritten at all. false means: leave it exactly as it is.
func withTraceBridge(sh shellKind, bridge, cmd string) (string, bool) {
	stripped := stripBridgeAssignments(cmd)
	if carriesUnremovableBridge(stripped) {
		return cmd, false
	}
	switch sh {
	case shellPowerShell:
		// Assignment, then removal in a finally — not `& { … }`.
		//
		// A block looks like it would scope the variable and does not:
		// measured on both editions, `& { $env:X='v'; … }` leaves X set in the
		// shell afterwards, because $env: is the process environment and a
		// PowerShell scope does not cover it. A host that reuses one shell for
		// the next command would hand that command our envelope, and the
		// search it belongs to would be credited with another call's lineage.
		// The removal runs in a finally so it happens even when the search
		// fails, and the whole thing is one statement, which is what a host
		// that sets a command string can carry.
		return "$env:" + bridgeEnv + " = '" + bridge + "'; try { " + stripped +
			"\n} finally { Remove-Item Env:" + bridgeEnv + " -ErrorAction SilentlyContinue }", true
	case shellPOSIX:
		// One assignment in front of one command: POSIX scopes it to that
		// command and nothing else, which is the behavior PowerShell needs
		// the finally for.
		//
		// The prefix is grammar, not decoration. POSIX binds NAME=value to
		// the FIRST simple command and rejects it outright before a compound
		// one — `V=x for …` is a syntax error that kills the participant's
		// whole line, and `V=x cd … && jevlin search …` hands the bridge to
		// cd, observed live as a reformulation pair split across two
		// fallback sessions. So the prefix is written only when the first
		// simple command is provably the search; any other shape is left
		// byte-identical, because a search that loses its bridge still runs
		// on its local fallback identity, and a command broken by a prefix
		// does not run at all.
		if !posixSearchLeadsRe.MatchString(stripped) {
			return cmd, false
		}
		return bridgeEnv + "=" + bridge + " " + stripped, true
	default:
		return cmd, false
	}
}

// bridgeShellForTool is Claude Code's per-call answer. Its PreToolUse payload
// names the tool, and on Windows that is the difference between Git Bash and
// PowerShell — the one host where the shell is not settled at install time
// (dropin-miner#77). An unnamed or unknown tool is not guessed at: the Bash tool is the
// one whose name this client has always matched, and a tool it does not know
// gets no rewrite.
func bridgeShellForTool(toolName string) (shellKind, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "":
		// Absent means the payload comes from a host that does not send the
		// field. The Bash tool is the one this hook has always matched, so
		// it keeps the POSIX prefix: a refusal here would cost lineage for
		// every such call.
		return shellPOSIX, true
	case "powershell":
		return shellPowerShell, true
	}
	// Named, and not a name this client knows. Its shell is exactly what is
	// unknown, so nothing is rewritten.
	return "", false
}
