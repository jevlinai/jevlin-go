# jevlin

Web search for coding agents that pays the person running the agent. One binary gives
Claude Code, Cursor, Codex, opencode, Pi and Hermes a web search through the Twilight search
router. If you turn mining on, those searches earn Twilight Slot rewards to an address you
control. There is no daemon, proxy or MCP server: between tool calls nothing is running.

## Install

```bash
npm install -g jevlin
jevlin setup
```

Install it globally, never with `npx`: setup writes the binary's path into your agents' skills
and hooks, and npm throws away an `npx` cache or a project's `node_modules`.

Without Node, download the archive for your OS and architecture from the
[releases page](https://github.com/jevlinai/jevlin-go/releases) and verify it against that
release's `checksums.txt`. Put `jevlin` on your PATH and run `jevlin setup`.

## What setup asks

Setup registers with the search platform and stores the key it gets back, so there is no key
to copy or paste. Then it asks, in this order:

| question | what your answer does |
|---|---|
| **Use it?** | Asked only when an earlier installation was set aside beside `~/.jevlin`. Yes moves it back. |
| **Enable mining rewards?** | No, the default, still gives you search. Yes asks for a payout address. |
| **Payout address** | A `twilight1…` address you control, or empty to create a wallet here. |
| **Add them to ~/.zshrc?** | Or whichever profile your shell reads. Puts `jevlin` on PATH and sets `JEVLIN_CONFIG`. On Windows it asks **Set them for your user?** instead. |
| **Set up the coding agents found on this machine now?** | Writes a skill and hooks into each agent's own config, after showing you what. |

Then it prints a link where you claim your agent. The [guide](docs/guide.md#setup) says what
each answer does and which flags answer them for you.

## Then check

```bash
jevlin status          # what this installation has and has not completed
jevlin agents status   # which agents are set up
jevlin doctor          # whether searches are recorded and earning, check by check
jevlin search -format model "what is proof of authority consensus"
```

It is working when the search prints results and `agents status` lists your agents as
installed. Restart any agent that was already open.

## The agents it sets up

| agent | what is written | one thing to know |
|---|---|---|
| [Claude Code](docs/agents.md#claude-code) | a skill; hooks and allow rules in `~/.claude/settings.json` | On Windows, a search through its PowerShell tool asks for approval. |
| [Cursor](docs/agents.md#cursor) | a skill; hooks in `~/.cursor/hooks.json` | Cursor also runs Claude Code's hooks, which stand aside for it. |
| [Codex](docs/agents.md#codex) | a skill; a sandbox block in `~/.codex/config.toml` | Without the block, searches work but earn nothing. |
| [opencode](docs/agents.md#opencode) | a plugin, and a line for you to paste into `AGENTS.md` | `/jevlin off` does not reach it. |
| [Pi](docs/agents.md#pi) | a skill and an extension under `~/.pi/agent/` | A resumed session keeps its place in the trace. |
| [Hermes](docs/agents.md#hermes) | a skill; a hook in `config.yaml` | Approve the hook once; it loads next session. |

Any other agent: [agents.md](docs/agents.md#any-other-agent) says what to paste.

## How earning works

- **Your first reward takes one to two hours.** You join an epoch two ahead, and it has to
  close and settle first.
- **One verified search per epoch makes you eligible.** More searches do not earn more.
- **The pot splits equally** among everyone eligible in that epoch.
- **A long idle gap can miss an epoch.** Recorded searches are submitted by the next search or
  agent session, and evidence that arrives after the epoch's deadline is late.

Search works before you claim your agent, and with mining off. The
[guide](docs/guide.md#earning) has the detail.

## What leaves the machine

Each search carries a trace, so the router can group one task's searches. It holds hashed
session and turn ids, a call counter, and the assistant text just before the search, capped
at 32 KB. That text goes only to the search router, inside the search request; the rewards
service receives metadata only.

`export JEVLIN_TRACE=off` sends no trace, and searches earn the same without it. See
[what travels with a search](docs/guide.md#what-travels-with-a-search).

A wallet setup creates is sealed by the passphrase you choose, and its 24-word recovery phrase
is printed once. [Where rewards land](docs/guide.md#where-rewards-land) says how to keep both
safe.

## Upgrading and removing

```bash
jevlin upgrade                 # a native install: the latest release, verified before it runs
npm install -g jevlin@latest   # an npm install is updated with npm
jevlin uninstall               # remove the skills, hooks and profile block; keep your wallet
```

The guide covers [upgrading](docs/guide.md#upgrading) and
[removing it and coming back](docs/guide.md#removing-it-and-coming-back).

## Documentation

- [Jevlin guide](docs/guide.md): setup in detail, the wallet, earning, upgrading, removing.
- [Agents](docs/agents.md): what is written into each coding agent, and its known limits.
- [Reference](docs/reference.md): commands, the `--stdin` envelope, config, environment
  variables, files, `doctor`'s checks.
- [CHANGELOG.md](CHANGELOG.md): what changed in each release.
- [SECURITY.md](SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.
