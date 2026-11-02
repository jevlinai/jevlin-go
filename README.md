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

It works in Claude Code, Cursor, Codex, opencode, Pi and Hermes. Search is free for agents:
setup registers you and gets a key, with no card and nothing to top up.

## Why jevlin

- **Several engines in one call.** A search fans out to independent indexes, among them Exa,
  Brave, Parallel and Perplexity, and comes back as one list.
- **Results you can weigh.** Each page names the engines that found it. Pages that several
  independent engines agree on come first.
- **Inside the agent you already use.** Setup installs a skill (for opencode, a plugin) and hooks
  where the agent has them, so the agent reaches for this search instead of its built-in one.
- **Nothing running in the background.** It is one binary your agent calls. Between searches
  there is no daemon and no proxy.
- **The task behind each search.** A small, scrubbed trace lets the router see which searches
  belong to one task, not just the query string. You can turn it off.

## Install

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Install it globally, not with `npx`: setup writes the binary's path into your agents' skills
and hooks, and npm throws an `npx` cache away. If your shell then says `jevlin: command not
found`, the [guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#setup) shows how to run setup by its full path.

Setup registers you with the search platform and stores the key, writes its config under
`~/.jevlin`, and asks before it adds `jevlin` to your shell's PATH and before it sets up the
coding agents it finds. Restart any agent that was already open.

**Claim your agent.** Setup prints a link: open it, sign in at `platform.jevlin.ai` (or create
an account) and confirm the agent's name. Search works before you do, on the fast tier with a
daily cap; claiming lifts the limits and opens the other tiers. The link is valid for a week, and
`jevlin connect` prints a fresh one.

### Other ways to install

- **Let your agent do it.** The marketplaces carry a setup skill that installs jevlin and runs
  `jevlin setup`. An agent handed a link can follow [jevlin.ai/agents.md](https://jevlin.ai/agents.md).

  ```bash
  npx skills add jevlinai/jevlin-go                          # any agent with skills (skills.sh)
  /plugin install jevlin --marketplace jevlinai/jevlin-go    # Claude Code, 2.1.275 or later
  claude plugin marketplace add jevlinai/jevlin-go           # Claude Code, older: then /plugin install jevlin@jevlin
  codex plugin marketplace add jevlinai/jevlin-go            # Codex CLI: then /plugins
  ```

- **Without Node.** Download the archive for your system from the
  [releases page](https://github.com/jevlinai/jevlin-go/releases), check it against that
  release's `checksums.txt`, put `jevlin` in a directory only you can write, such as
  `~/.local/bin`, and run `jevlin setup` from there.
- **From source.** With Go 1.27 or newer, run `go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`,
  or clone this repository and run `make build` for `bin/jevlin`. A source build reports its
  version as `dev`, so it never updates itself and `jevlin upgrade` refuses it. See
  [building from source](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#building-from-source).

## Check it works

```bash
jevlin search -format model "what is proof of authority consensus"
jevlin status          # what this installation has and has not completed
jevlin agents status   # which agents are set up
jevlin doctor          # whether it is ready to search, check by check
```

It is working when the search prints results and `agents status` lists your agents.

## Using it

Ask your agent anything that needs the web, and the skill sends the search through jevlin. To
ask for one directly, type `/jevlin <question>` or name jevlin in your request.

| in the agent | what it does |
|---|---|
| `/jevlin <question>` | One search; the agent answers from the merged list. |
| `/jevlin tier fast\|balanced\|max` | Sets the tier every search uses. `/jevlin tier default` unsets it. |
| `/jevlin off`, `/jevlin on` | Makes the agent's own web search the default again, or this one. |

Every search runs in one of three tiers:

| tier | what runs | about |
|---|---|---|
| `fast` | Two or three fast indexes, in about a second. The only tier an unclaimed agent may use. | $0.0013 to $0.0053 a search |
| `balanced` | Four core engines: Exa, Brave, Parallel search and Perplexity search. | $0.022 |
| `max` | All seven core engines, with more results and more text per page. | $0.027 |

The prices are the engines' own, with no markup, and every response reports what its search
cost. Your agent never chooses a tier: you do, and with none set the router applies your key's
default. The [guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#choosing-a-tier) has the details, and `jevlin limits` shows your key's caps
and today's spend.

## Supported agents

| agent | after setup |
|---|---|
| [Claude Code](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#claude-code) | Ready. On Windows, a search through its PowerShell tool asks for approval. |
| [Cursor](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#cursor) | Ready. |
| [Codex](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#codex) | Approve jevlin's hooks in Codex. Needs Codex 0.158.0 or newer. |
| [opencode](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#opencode) | Paste the block setup prints into your `AGENTS.md`. |
| [Pi](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#pi) | Ready. |
| [Hermes](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#hermes) | Approve the hook once; it loads next session. |

For any other agent, [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#any-other-agent) says what to paste.

## What leaves your machine

A search sends your query and a small trace to the search router at `api.jevlin.ai`. The trace
carries hashed session ids and the assistant's text just before the search, scrubbed of anything
shaped like a secret and capped at 32 KiB; Codex and Hermes send no text. `export
JEVLIN_TRACE=off` stops it, and searches work the same without it. Your agent passes the query
on standard input, so it never appears on a command line; a search you type,
`jevlin search "<query words>"`, does put it there. The search platform sees your
registration and claim, never a query, a result or a trace.
[The trace](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#the-trace) lists every field and every scrub rule.

## Upgrading and removing

```bash
jevlin upgrade                         # an install from a release archive: the latest, verified before it runs
jevlin uninstall                       # remove the skills, hooks and profile lines; keep your registration and key
jevlin uninstall -purge-state          # also delete the identity, key and config, confirmed at a terminal
npm uninstall -g @jevlinsearch/jevlin  # then remove an npm install
```

An npm install is updated with `npm install -g @jevlinsearch/jevlin@latest`. Once a day, when
an agent session starts or ends, jevlin checks for a newer release. An install from a release
archive installs a patch release (0.7.0 to 0.7.1) itself and leaves a new minor release for
`jevlin upgrade`; an npm install is only told. The guide covers
[upgrading](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#upgrading) and
[removing it and coming back](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#removing-it-and-coming-back).

## Documentation

- [Guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md): setup in detail, the claim, tiers, the trace,
  upgrading, removing.
- [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md): what is written into each coding agent, and its known limits.
- [Reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md): every command and flag, the `--stdin` request and its
  envelope, providers and request options, config, files, the trace, the turn end.
- [CHANGELOG.md](https://github.com/jevlinai/jevlin-go/blob/main/CHANGELOG.md): what changed in each release.
- [SECURITY.md](https://github.com/jevlinai/jevlin-go/blob/main/SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.
