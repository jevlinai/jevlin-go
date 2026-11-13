<p align="center"><img src="https://raw.githubusercontent.com/jevlinai/jevlin-go/734aba4f98c8d484a0bcb26c4f2b25ffccca41a1/docs/assets/jevlin-logo.png" alt="jevlin" width="220"></p>
<p align="center">
  <a href="https://www.npmjs.com/package/@jevlinsearch/jevlin"><img src="https://img.shields.io/npm/v/@jevlinsearch/jevlin?label=npm&color=cb3837" alt="npm"></a>
  <a href="https://github.com/jevlinai/jevlin-go/releases"><img src="https://img.shields.io/github/v/release/jevlinai/jevlin-go?label=release" alt="release"></a>
  <a href="https://github.com/jevlinai/jevlin-go/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/jevlinai/jevlin-go/ci.yml?branch=main&label=ci" alt="ci"></a>
  <a href="https://github.com/jevlinai/jevlin-go/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="license"></a>
  <a href="https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen" alt="PRs welcome"></a>
  <a href="https://jevlin.ai"><img src="https://img.shields.io/badge/jevlin.ai-site-111" alt="jevlin.ai"></a>
</p>

**Supersearch for coding agents.** jevlin gives your coding agent one web search that asks
several search engines at once and returns a single merged list, with every result naming the
engines that found it.

On the hosted service one call can ask multiple engines such as Exa, Brave, Parallel, Perplexity,
Serper and TinyFish among them, depending on the tier you choose. One account and one key cover
all of them: the router holds the engine keys, so you need none of your own.

It works in Claude Code, Cursor, Codex, opencode, Pi and Hermes. It is one binary your agent
calls, a skill that teaches the agent the call, and hooks where the agent has them. No daemon runs
between searches: a hook may start the daily release check, and a search may start a short check
that finishes your claim. Plans and prices are at [jevlin.ai](https://jevlin.ai).

## How do I install it?
```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```
Install it globally, not with `npx`: setup writes the binary's path into your agents' skills and
hooks, and npm throws an `npx` cache away.

```bash
curl -fsSL https://jevlin.ai/install.sh | sh     # macOS, Linux and WSL

irm https://jevlin.ai/install.ps1 | iex          # Windows PowerShell
```
This installs the latest release at `~/.jevlin/bin/jevlin` (on Windows,
`%USERPROFILE%\.jevlin\bin\jevlin.exe`), checked against the release's checksums and, where a tool
on the machine can, its signature, and runs setup when you ran it at a terminal. From CMD,
`install.cmd` runs the PowerShell installer. `JEVLIN_HOME` installs it elsewhere and
`JEVLIN_NO_SETUP=1` stops before setup ([options](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#installing-without-npm)).

It runs on macOS 13 or later, Linux and Windows, each on amd64 and arm64.

### Can I install it another way?

- **From GitHub Releases:** download the archive for your platform and check it against its
  `checksums.txt` ([steps](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#installing-without-npm))

- **From an agent marketplace:** Claude Code's and Codex's plugin marketplaces, and skills.sh,
  carry a setup skill that installs and sets up jevlin. With skills.sh that is
  `npx skills add jevlinai/jevlin-go` ([the others](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#let-your-agent-install-it))

- **From source:** `go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`, or `make build`
  in a clone. It reports its version as `dev`, so `jevlin upgrade` and the daily check leave it
  alone ([details](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#building-from-source))

**`jevlin: command not found`?** Run setup by its full path. [The guide shows how](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#jevlin-command-not-found).

## What does setup do, and what is the claim?

**Setup** sets up the coding agents it finds and adds `jevlin` to your PATH, asking before
each. It also registers jevlin on this machine with the Jevlin platform, stores its key (nothing
to copy or paste) and writes its config to `~/.jevlin`. Restart any agent that was already open.

**Claiming** links jevlin on this machine to your Jevlin account. Setup prints a link: open it, sign in at
`platform.jevlin.ai` (or create an account), and confirm the name. You can also type the
code setup printed at `platform.jevlin.ai/claim`. You claim once per machine,
not once per coding agent: Claude Code, Codex and the rest all search through the same
registration. (The platform calls it an "agent".)

Until you claim it, you can search on the `fast` tier, up to a daily limit. Claiming unlocks
`balanced` and `max`.

The link is good for a week. If it expires before you claim, run `jevlin connect` to register
again and get a new link.

Details: [Setup](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#setup) ·
[The claim link](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#the-claim-link).

## How do I check it works?

```bash
jevlin search -format model "what is proof of authority consensus"
jevlin status          # what this installation has and has not completed
jevlin agents status   # which agents are set up
jevlin doctor          # whether it is ready to search, check by check
```

It works when the search prints results and `agents status` lists your agents.

## How does my agent use it?

Setup installs a `jevlin` skill that tells the agent to use this search for anything that needs
the web, instead of its built-in one; opencode gets a rules block you paste into `AGENTS.md`
instead. Naming jevlin in a request sends that search here. In Claude Code and Cursor you can also
type these (in Codex, `$jevlin`):

| in the agent | what it does |
|---|---|
| `/jevlin <question>` | One search; the agent answers from the merged list. |
| `/jevlin tier fast\|balanced\|max` | Sets the tier every search uses. `/jevlin tier default` unsets it. |
| `/jevlin off`, `/jevlin on` | Makes the agent's own web search the default again, or this one. |

[Making it the default, or not](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#making-it-the-default-or-not) covers the rest.

The skill also teaches the agent to read a page from its results in full with `jevlin fetch --stdin`
([reference, Reading a page in full](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#reading-a-page-in-full)).
`jevlin decide --stdin` asks a decision model typed questions about a state you give it, where the
router offers it ([reference, Asking a decision model](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#asking-a-decision-model));
the skill does not teach it yet.

An agent that takes MCP servers can call search, fetch and decide (where the router offers it) as
tools instead: register
`jevlin mcp` with it, as [reference, MCP server](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#mcp-server)
shows for Claude Code, Codex and Cursor. Setup does not register it, and it runs only while the
host holds it open.

## Which tier should I use?

| tier | what it asks | choose it |
|---|---|---|
| `fast` | The fast indexes of a preset: `economy` asks two, `standard` three. The quickest. | For routine lookups. It is the only tier before you claim. |
| `balanced` | Four of the hosted service's core engines. | When you want several independent sources, or a fast search came back thin. |
| `max` | All seven core engines, with more results from each. | When agreement between independent engines matters. |

You choose the tier; the skill tells your agent never to set one. With none set, the router
applies your key's default. `jevlin limits` shows your key's caps and today's spend, and every
response reports what its search cost. [Choosing a tier](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#choosing-a-tier) has the presets.

## Which agents are supported?

| agent | after setup |
|---|---|
| [Claude Code](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#claude-code) | Ready. jevlin's hook approves exactly the search and fetch commands the skill renders. |
| [Cursor](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#cursor) | Ready. |
| [Codex](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#codex) | Approve jevlin's hooks in Codex once. Setup makes jevlin's permission profile Codex's default. On Windows there are neither, and Codex asks before a search reaches the network. Needs Codex 0.158.0 or newer. |
| [opencode](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#opencode) | Paste the rules block setup prints into your `AGENTS.md`. |
| [Pi](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#pi) | Ready. |
| [Hermes](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#hermes) | Approve the hook once; it loads next session. |

For any other agent, [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#any-other-agent) says what to paste.

## What leaves my machine?

Each search sends your query and a small trace to the search router at `api.jevlin.ai`. The trace
carries hashed session ids and the assistant's text that led to the search, scrubbed of anything
shaped like a secret: from Claude Code an earlier message of the turn, from Cursor its latest
thought or reply, from opencode and Pi the text before the search, and from Codex and Hermes none.
With `[search] turn_end = true`, a turn that searched also sends what you asked, the assistant's
text along the way and its final answer, its tool calls by name, duration and outcome (never their
input or output), the model and the token counts (less from Cursor and Codex, nothing from Hermes;
[Privacy](https://github.com/jevlinai/jevlin-go/blob/main/docs/privacy.md) has the table per agent).
`export JEVLIN_TRACE=off` sends neither.

The query is not among jevlin's arguments: your agent hands it over on standard input, from a
here-string in the shell command it runs (`<<<` in bash, `@'…'@` in PowerShell), or in Claude Code from
a file when the request holds a `'` or a `$`. A search you type, `jevlin search "<query words>"`,
puts it on jevlin's command line. [Privacy](https://github.com/jevlinai/jevlin-go/blob/main/docs/privacy.md) has every field and scrub rule;
what the service keeps is in its [privacy policy](https://platform.jevlin.ai/privacy).

## How do I update or remove it?

```bash
jevlin upgrade                         # a native install: the latest, verified before it runs
jevlin uninstall                       # remove the skills, hooks and profile lines; keep your registration and key
jevlin uninstall -binary               # also remove this installation's binary
jevlin uninstall -purge-state          # also delete the registration, key and config, confirmed at a terminal
npm uninstall -g @jevlinsearch/jevlin  # then remove an npm install
```

An npm install is updated with `npm install -g @jevlinsearch/jevlin@latest`. Once a day, when
Claude Code, Cursor, Codex, opencode or Pi starts a session or, in all but Pi, finishes a turn,
jevlin checks for a newer release. A native install (the install script or a release archive)
installs a patch release (0.8.0 to 0.8.1) itself and leaves a new minor release for
`jevlin upgrade`; for an npm install the release is recorded, for `jevlin status` and
`jevlin doctor` to report. `[update] mode = "notify"` or `"off"` in the config changes that.
[Upgrading](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#upgrading) and
[removing it and coming back](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#removing-it-and-coming-back) have the details.

## Where is the rest?

- [Guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md): install, setup, the claim, tiers, upgrading, removing, troubleshooting.
- [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md): what setup writes into each coding agent, and its known limits.
- [Privacy](https://github.com/jevlinai/jevlin-go/blob/main/docs/privacy.md): what jevlin sends, per agent, and how to switch it off.
- [Reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md): every command and flag, tiers and providers, the `--stdin`
  request and its envelope, config, files.
- [Web search router vs Supersearch](https://jevlin.ai/web-search-router/): what asking every engine finds that a web search
  router sending each query to one engine does not, measured on 485 questions.
- [CHANGELOG.md](https://github.com/jevlinai/jevlin-go/blob/main/CHANGELOG.md): what changed in each release.
- [SECURITY.md](https://github.com/jevlinai/jevlin-go/blob/main/SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.

## The npm wrapper

This package downloads the release binary for your platform on install, checks the release's signature and verifies the binary against its checksums, and forwards every argument to it. The package version is the release tag it fetches.

- `JEVLIN_BINARY=/path` use a binary already on the machine
- `JEVLIN_SKIP_DOWNLOAD=1` install the wrapper without fetching
