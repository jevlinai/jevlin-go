package main

// The skill, rendered for the shells its host actually runs.
//
// One host can run tool calls in more than one shell on one OS — Claude Code
// on Windows runs its Bash tool through Git Bash and its PowerShell tool
// through PowerShell, and which one a call uses is the model's choice, not
// ours (H-R5). So the skill teaches one runnable form per declared shell,
// labeled, rather than one form that is wrong for half the calls.
//
// A host whose shell is not established gets the POSIX form and the install
// says so in its plan: refusing would take away a host that works today
// (Codex on Windows).

import (
	"fmt"
	"slices"
	"strings"
)

// exampleRequest is the request body the skill shows.
const exampleRequest = `{"version":1,"query":"exact query text"}`

// skillTeachesQueryForms is the switch for the query forms (Jevlin-core
// #55). The client sends objective and search_queries whenever a caller
// does, and a router that predates them costs that search one refused
// request and a retry without them (forms_dropped). A skill that tells
// every agent to send them would make every search pay that, so the
// paragraph that teaches them, and the reading of the fields #55 adds to
// the merged view, render only while this is true.
//
// It is on: #55 is on the live router. It shipped off, and went on in the
// release that followed #55 reaching the router, because a skill is
// rewritten on a participant's machine by the upgrade and cannot be
// taken back from there. Turn it off only if the router stops taking the
// forms. A test that renders with it set sets it and restores it, and is
// never parallel.
var skillTeachesQueryForms = true

// formsSection is the Request options paragraph on the query forms, and
// formsReadingSection the Reading the answer paragraph on what #55 adds to
// the merged view. Each begins with its own paragraph break, because each
// replaces a marker at the end of a line: with the gate off they render as
// nothing, and the skill is exactly what it was without them.
func formsSection() string {
	if !skillTeachesQueryForms {
		return ""
	}
	return `

Two more help the router find what one wording of the question misses:

- ` + "`objective`" + ` — one sentence, the user's goal in your own words. Send it
  on every search.
  ` + "`" + `{"version":1,"query":"pgx pool exhausted","objective":"find why a Go service's pgx connection pool runs out under load"}` + "`" + `
- ` + "`search_queries`" + ` — up to 4 more wordings, each searched beside
  ` + "`query`" + `. Each costs about as much as another search, so send two or
  three when the first search came back thin, or when the question has a
  name you are not sure of: the identifier as the user wrote it, the
  vendor's name for it, and the plain-words version.
  ` + "`" + `{"version":1,"query":"ERR_OSSL_EVP_UNSUPPORTED","search_queries":["Node.js 17 OpenSSL 3 error","digital envelope routines unsupported"]}` + "`" + `

If ` + "`result.forms_dropped`" + ` is ` + "`true`" + `, the router did not take them and the
search ran on ` + "`query`" + ` alone. That is not a failure.`
}

func formsReadingSection() string {
	if !skillTeachesQueryForms {
		return ""
	}
	return `

Each entry's ` + "`votes`" + ` counts the index groups and wordings that listed
the page, and ` + "`result.coverage`" + ` sums the search the same way: the
fan-out's own evidence, stronger than ` + "`found_by`" + ` alone. A page's
` + "`snippet`" + ` is every provider's text for it, joined. The list is the
router's window: ` + "`result.merged_total`" + ` says how many pages there were.`
}

// shellLabel names a shell the way a participant's host does.
func shellLabel(sh shellKind) string {
	switch sh {
	case shellPOSIX:
		return "Bash (on Windows, Git Bash)"
	case shellPowerShell:
		return "PowerShell"
	case shellCmd:
		return "cmd"
	}
	return string(sh)
}

// skillShells is everything the skill needs to know about a host's tool
// shells on one OS: which shells to render for, who picks between them, and
// the OS itself — which is what lets the label name a shell the way the
// participant's own setting does rather than the way a renderer would.
// The three travel together because they came from one declaration, and a
// caller holding them separately could pair a label with the wrong OS.
type skillShells struct {
	kinds  []shellKind
	choice shellChoice
	goos   string
}

// condition is the sentence above one block of a multi-shell host's skill:
// the test the reader applies to decide whether that block is theirs.
//
// Who picks decides the words entirely. Claude Code runs two tools and the
// model chooses one per call, so the condition is about the call being made.
// Cursor runs one shell that the participant configured once, so a condition
// about "the tool you are calling" would be unanswerable — there is only one
// tool — and the participant has to be asked about their terminal instead
// (dropin-miner#96).
func (s skillShells) condition(sh shellKind) string {
	if s.choice == chosenByParticipant {
		return "If your terminal is " + s.terminalLabel(sh)
	}
	return "If the tool you are calling runs " + shellLabel(sh)
}

// terminalLabel names a shell as a terminal-profile list does. On Windows
// that is "Git Bash", which is what Cursor's own profile picker calls it and
// so what the participant will recognize; elsewhere the shell's plain name.
func (s skillShells) terminalLabel(sh shellKind) string {
	if sh == shellPOSIX {
		if s.goos == "windows" {
			return "Git Bash"
		}
		return "Bash"
	}
	return shellLabel(sh)
}

// lead is the sentence introducing a multi-shell host's blocks, and it says
// why there is more than one — a reader who knows which of the two facts
// applies to them can pick without reading both blocks.
func (s skillShells) lead() string {
	if s.choice == chosenByParticipant {
		return "This host runs tool calls in the terminal this machine is configured to use, which this client cannot know. Use the form that matches it; both reach the same search."
	}
	return "This host runs tool calls in more than one shell. Use the form that matches the tool you are calling with; both reach the same search."
}

// toolShellsForSkill is the shells a host's skill renders for on this OS,
// and the note the install plan prints when nothing established them.
//
// The fallback is the POSIX form, deliberately: a host nobody has watched
// keeps working exactly as it does today, and the participant is
// told which host and OS that applies to instead of finding out from a
// search that never runs.
func toolShellsForSkill(t installTarget, goos string) (shells skillShells, note string) {
	fallback := skillShells{kinds: []shellKind{shellPOSIX}, goos: goos}
	declared, err := declaredShells(t, goos, channelTool)
	if err != nil {
		return fallback, fmt.Sprintf(
			"%s: which shell runs its tool calls on %s is not established, so the skill keeps the Bash form; a search that does not run here is why",
			t.Label(), goos)
	}
	if len(declared) == 0 {
		return fallback, ""
	}
	return skillShells{kinds: declared, choice: toolShellChoice(t, goos), goos: goos}, ""
}

// fenced wraps a rendered script in its fence.
func fenced(lang, script string) string {
	return "```" + lang + "\n" + script + "\n```"
}

// perShellSection renders one block per shell, labeled only when the host
// runs more than one: a single-shell host's skill reads exactly as it did.
// withLead is false for the sections that are one command each, where the
// conditions alone say everything the lead would.
func perShellSection(shells skillShells, withLead bool, render func(shellKind) (string, error)) (string, error) {
	var b strings.Builder
	multiple := len(shells.kinds) > 1
	if multiple && withLead {
		b.WriteString("\n" + shells.lead() + "\n")
	}
	for _, sh := range shells.kinds {
		block, err := render(sh)
		if err != nil {
			return "", err
		}
		if multiple {
			fmt.Fprintf(&b, "\n%s:\n\n%s\n", shells.condition(sh), block)
			continue
		}
		b.WriteString("\n" + block + "\n")
	}
	return b.String(), nil
}

// callSection is the skill's "How to call it": the search command, and the
// sentence about the quoting that carries the request body.
func callSection(entry binEntry, shells skillShells) (string, error) {
	body, err := perShellSection(shells, true,
		func(sh shellKind) (string, error) {
			lang, script, err := searchBlockForShell(sh, entry, exampleRequest)
			if err != nil {
				return "", err
			}
			note := quotingNote(sh)
			if sh == shellPowerShell && shells.choice == chosenByParticipant && slices.Contains(shells.kinds, shellPOSIX) {
				note += apostropheNote
			}
			return fenced(lang, script) + "\n" + note, nil
		})
	if err != nil {
		return "", err
	}
	return body, nil
}

// quotingNote explains, per shell, what keeps the request intact — the part
// an agent has to preserve when it substitutes its own query.
func quotingNote(sh shellKind) string {
	switch sh {
	case shellPOSIX:
		return `
The heredoc delimiter is quoted (` + "`<<'JSON'`" + `) so the shell expands
nothing inside it, and every path is single-quoted for the same reason. Keep
both when you substitute your own query.`
	case shellPowerShell:
		return `
The here-string is single-quoted (` + "`@'` … `'@`" + `) so PowerShell expands
nothing inside it; only a line that begins with ` + "`'@`" + ` ends it. The first line
sets this shell's output encoding to UTF-8 with no byte-order mark, which is
what makes a query carrying an apostrophe, a quotation mark or any non-ASCII
character arrive exactly as written on Windows PowerShell 5.1. Keep both lines
when you substitute your own query.`
	}
	return ""
}

// apostropheNote follows the PowerShell form where a Bash terminal may read
// it too: Cursor allows that form without asking only when its body holds no
// apostrophe (inertUnderEveryShell).
const apostropheNote = `
Write an apostrophe in the query as ` + "`\\u0027`" + `, the JSON escape for
it, so the body holds no ` + "`'`" + ` at all: the same text in a Git Bash terminal
would end its quoting there. The query arrives unchanged.`

// preferSection is the skill's on/off command, per shell.
func preferSection(entry binEntry, shells skillShells) (string, error) {
	return perShellSection(shells, false, func(sh shellKind) (string, error) {
		cmd, err := entry.preferCommandForShell(sh)
		if err != nil {
			return "", err
		}
		lang := "bash"
		if sh == shellPowerShell {
			lang = "powershell"
		}
		return fenced(lang, cmd+" <argument>"), nil
	})
}

// tierSection is the skill's tier command, per shell.
func tierSection(entry binEntry, shells skillShells) (string, error) {
	return perShellSection(shells, false, func(sh shellKind) (string, error) {
		cmd, err := entry.tierCommandForShell(sh)
		if err != nil {
			return "", err
		}
		lang := "bash"
		if sh == shellPowerShell {
			lang = "powershell"
		}
		return fenced(lang, cmd+" <band>"), nil
	})
}

// humanSection is the human terminal form, per shell. It keeps the query in
// argv, which H-R2 leaves alone: it is the form a person types, and it is
// never what an agent or a machine caller uses.
func humanSection(entry binEntry, shells skillShells) (string, error) {
	return perShellSection(shells, false, func(sh shellKind) (string, error) {
		cmd, err := entry.searchCommandForShell(sh)
		if err != nil {
			return "", err
		}
		lang := "bash"
		if sh == shellPowerShell {
			lang = "powershell"
		}
		return fenced(lang, cmd+` "<query>"`), nil
	})
}
