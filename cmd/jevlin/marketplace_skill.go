package main

// The marketplace skill: skills/jevlin/SKILL.md, the one copy of the skill
// that is not rendered on a participant's machine.
//
// A marketplace (skills.sh, Claude Code's plugin directory, Codex's and
// Cursor's) hands an agent a file from the repository, so that file has to
// be right for a machine nobody has seen: `jevlin` found on PATH rather
// than by its full path, both shells Claude Code on Windows may call, and
// two sections the installed skill does not need — how to get the binary,
// and the fact that `/jevlin off` cannot rewrite this copy. Everything
// else is the installed skill, byte for byte, which is what
// TestMarketplaceSkillIsTheTemplateRendered holds it to: a change to
// skill.md that is not regenerated here is a change marketplace users
// never get (#134 review).

import "strings"

// marketplaceSkillShells is Claude Code on Windows: both shells, the
// model choosing per call. The condition it renders ("If the tool you are
// calling runs …") reads correctly on every other host too, and a host
// with one shell simply never meets the other block.
var marketplaceSkillShells = skillShells{kinds: []shellKind{shellPOSIX, shellPowerShell}, choice: chosenPerCall, goos: "windows"}

// marketplaceSkillPreamble follows the title: the binary, and the
// preference this copy cannot be rewritten to carry.
const marketplaceSkillPreamble = `
## Before the first search

This skill runs the ` + "`jevlin`" + ` command, which comes with the Jevlin client. If
` + "`jevlin`" + ` is not on PATH, or ` + "`jevlin status`" + ` says the installation is not set
up, install the client once and run its setup. It registers the agent and
gets its own key, with no browser step:

` + "```bash" + `
npm install -g @jevlinsearch/jevlin && jevlin setup
` + "```" + `

Without Node, follow the native install on https://docs.jevlin.ai/quickstart:
a release archive verified against its ` + "`checksums.txt`" + `, put on PATH, then
` + "`jevlin setup`" + `. Setup also writes this agent's own copy of the skill and its
hooks, which carry each search into the session's trajectory and let it run
without an approval prompt. Keep that copy; this one is the marketplace's and
says the same thing.

This copy is not rewritten when the user runs ` + "`/jevlin off`" + ` or ` + "`/jevlin on`" + `,
so once per session, before the first search, run

` + "```bash" + `
'jevlin' agents prefer status
` + "```" + `

It prints ` + "`search default: on`" + ` or ` + "`search default: off`" + `. On ` + "`off`" + `, the user
made the host's built-in web search the default: use that for lookups, and
this one only when the user names jevlin or the router. Do not suggest
switching back; the user knows the command.
`

// marketplaceSkillRuleOn replaces the first rule of rulesOn: the same
// preference, conditioned on the check the preamble asks for.
const (
	marketplaceSkillRuleFrom = `- Prefer this for public-web lookups: current information, documentation, research,
  fact-checking, finding sources. One focused query per call.`
	marketplaceSkillRuleTo = `- Unless ` + "`agents prefer status`" + ` said ` + "`off`" + ` (see Before the first search),
  prefer this for public-web lookups: current information, documentation,
  research, fact-checking, finding sources. One focused query per call.`
)

// marketplaceSkill renders skills/jevlin/SKILL.md from the same template
// and the same renderer as every installed copy.
func marketplaceSkill() ([]byte, error) {
	out, err := renderSkill(binEntry{command: "jevlin"}, preferOn, "", marketplaceSkillShells)
	if err != nil {
		return nil, err
	}
	s := string(out)
	// json.Marshal escapes < and > in the frontmatter description; a
	// marketplace index shows the description as text, so spell them out.
	s = strings.ReplaceAll(s, `\u003c`, "<")
	s = strings.ReplaceAll(s, `\u003e`, ">")
	const title = "\n# Web search (jevlin)\n"
	s = strings.Replace(s, title, title+marketplaceSkillPreamble, 1)
	s = strings.Replace(s, marketplaceSkillRuleFrom, marketplaceSkillRuleTo, 1)
	return []byte(s), nil
}
