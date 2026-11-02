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

It works in Claude Code, Cursor, Codex, opencode, Pi and Hermes. It is one binary your agent
calls, a skill that teaches the agent the call, and hooks where the agent has them. No daemon runs
between searches; a hook or a search may start one short background job, such as the daily
release check. Plans and prices are at [jevlin.ai](https://jevlin.ai).

## How do I install it?

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Install it globally, not with `npx`: setup writes the binary's path into your agents' skills
and hooks, and npm throws an `npx` cache away.

- **Without Node:** `curl -fsSL https://jevlin.ai/install.sh | sh` on macOS and Linux, or
  `irm https://jevlin.ai/install.ps1 | iex` in Windows PowerShell. It installs the latest release
  at `~/.jevlin/bin/jevlin`, checked against the release's checksums and, where a tool on the
  machine can, its signature, and runs setup when you ran it at a terminal.
  [Installing without npm](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#installing-without-npm) has its options.
- **From source:** `go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`, or `make build`
  in a clone. See [building from source](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#building-from-source).

If your shell then cannot find `jevlin`, [the guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#jevlin-command-not-found) shows how to run setup by its full path.

## What does setup do, and what is the claim?

Setup registers this machine's agent with the search platform and stores the key it gets back,
writes its config under `~/.jevlin`, and asks before it adds `jevlin` to your shell's PATH and
before it sets up the coding agents it finds. Restart any agent that was already open.

It then prints a link to claim the agent: open it, sign in at `platform.jevlin.ai` (or create an
account) and confirm the agent's name, or type the printed code at `/claim` there. Until you do,
search runs on the fast tier within a daily cap; claiming opens `balanced` and `max`. The link and
the unclaimed registration last a week; after that, `jevlin connect` registers again and prints a
new link. [Setup](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#setup) and [the claim link](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#the-claim-link) have the details.

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

## Which tier should I use?

| tier | what it asks | choose it |
|---|---|---|
| `fast` | The fast indexes of a preset: `economy` asks two, `standard` three. The quickest. | For routine lookups. It is the only tier before you claim. |
| `balanced` | Four of the hosted service's core engines: Exa, Brave, Parallel search and Perplexity search. | When you want several independent sources, or a fast search came back thin. |
| `max` | All seven core engines, with more results from each. | When agreement between independent engines matters. |

You choose the tier; the skill tells your agent never to set one. With none set, the router
applies your key's default. `jevlin limits` shows your key's caps and today's spend, and every
response reports what its search cost. [Choosing a tier](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#choosing-a-tier) has the presets.

## Which agents are supported?

| agent | after setup |
|---|---|
| [Claude Code](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#claude-code) | Ready. jevlin's hook approves exactly the search the skill renders. |
| [Cursor](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#cursor) | Ready. |
| [Codex](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#codex) | Approve jevlin's hooks in Codex once. Setup makes jevlin's permission profile Codex's default. Needs Codex 0.158.0 or newer. |
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
input or output), the model and the token counts. `export JEVLIN_TRACE=off` sends neither.

The query is not among jevlin's arguments: your agent hands it over on standard input, from a
here-string in the shell command it runs (`jevlin search --stdin <<< '…'`), or in Claude Code from
a file when the request holds a `'` or a `$`. A search you type, `jevlin search "<query words>"`,
puts it on jevlin's command line. [Privacy](https://github.com/jevlinai/jevlin-go/blob/main/docs/privacy.md) has every field and scrub rule;
what the service keeps is in its [privacy policy](https://platform.jevlin.ai/privacy).

## How do I update or remove it?

```bash
jevlin upgrade                         # an install from a release archive: the latest, verified before it runs
jevlin uninstall                       # remove the skills, hooks and profile lines; keep your registration and key
jevlin uninstall -binary               # also remove this installation's binary
jevlin uninstall -purge-state          # also delete the registration, key and config, confirmed at a terminal
npm uninstall -g @jevlinsearch/jevlin  # then remove an npm install
```

An npm install is updated with `npm install -g @jevlinsearch/jevlin@latest`. Once a day, when a
Claude Code, Cursor, Codex, opencode or Pi session starts or ends, jevlin checks for a newer
release. An install from a release archive installs a patch release (0.7.0 to 0.7.1) itself and
leaves a new minor release for `jevlin upgrade`; for an npm install the release is recorded, for
`jevlin status` and `jevlin doctor` to report. [Upgrading](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#upgrading) and
[removing it and coming back](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#removing-it-and-coming-back) have the details.

## Where is the rest?

- [Guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md): install, setup, the claim, tiers, upgrading, removing, troubleshooting.
- [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md): what setup writes into each coding agent, and its known limits.
- [Privacy](https://github.com/jevlinai/jevlin-go/blob/main/docs/privacy.md): what jevlin sends, per agent, and how to switch it off.
- [Reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md): every command and flag, tiers and providers, the `--stdin`
  request and its envelope, config, files.
- [CHANGELOG.md](https://github.com/jevlinai/jevlin-go/blob/main/CHANGELOG.md): what changed in each release.
- [SECURITY.md](https://github.com/jevlinai/jevlin-go/blob/main/SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.

## The npm wrapper

This package downloads the release binary for your platform on install, verifies it against the release checksums, and forwards every argument to it. The package version is the release tag it fetches.

- `JEVLIN_BINARY=/path` use a binary already on the machine
- `JEVLIN_SKIP_DOWNLOAD=1` install the wrapper without fetching
