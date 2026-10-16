<p align="center"><img src="https://raw.githubusercontent.com/jevlinai/jevlin-go/734aba4f98c8d484a0bcb26c4f2b25ffccca41a1/docs/assets/jevlin-logo.png" alt="jevlin" width="220"></p>

Multi-provider web search for coding agents, with the trajectory behind every search. One
binary gives Claude Code, Cursor, Codex, opencode, Pi and Hermes a `search` that fans a query
out across several web indexes at once, merges what they return into one attributed list, and
carries enough of the agent's context that the router can see the task a search belongs to,
not just the query string. There is no daemon, proxy or MCP server: between tool calls nothing
is running.

Searches can also count as mining on the Twilight testnet. That is optional, off by default,
and described [at the end](#mining-on-the-twilight-testnet).

## Install

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Install it globally, never with `npx`: setup writes the binary's path into your agents' skills
and hooks, and npm throws away an `npx` cache or a project's `node_modules`.

Without Node, download the archive for your OS and architecture from the
[releases page](https://github.com/jevlinai/jevlin-go/releases) and verify it against that
release's `checksums.txt`. Put `jevlin` in a directory only you can write, such as
`~/.local/bin`, and run `jevlin setup` from there. Setup and `agents install` refuse a binary
under the temp directory, or in a directory another user owns or every user can write, and
warn about one a shared group can write. On Windows only the temp-directory rule is checked.

## What a search returns

One request goes to the router at `api.jevlin.ai`; the router runs it against its providers
and answers once, with everything they returned:

- **`result.merged`**: the citations of every provider that answered, deduplicated by page.
  Each entry carries `found_by`, how many providers agree on that page, and `best_rank`, the
  best position any of them gave it. Agreement between independent indexes is the fan-out's
  own evidence, and the skill tells the agent to read this list first.
- **`result.candidates`**: each provider's answer as it came back, so a disagreement can be
  inspected rather than averaged away. `"view":"merged"` drops this when tokens matter.
- **`decision`**, which providers ran; **`usage.cost_micros`**, what the search cost, in
  millionths of a dollar; **`latency`**, per provider; **`request_id`**, so a search can be
  found again.

Agents call `jevlin search --stdin` with one JSON request and read one JSON object back. The
query never touches the command line. A person can type `jevlin search "<query words>"`.

```bash
jevlin search -format model "what is proof of authority consensus"
```

## Tiers and request options

Every search carries a band, the router's cost tier: `fast` is one provider, `balanced` is
several with each source attributed, `max` is every provider, for corroboration. `/jevlin tier
balanced` in an agent, or `jevlin agents tier balanced` from a shell, sets the band every
search carries unless a request names one. The skill tells the agent to leave the tier alone
so your band applies, to ask for `balanced` when it needs several sources, and never to send
`max` on its own.

Four more fields pass straight through to the router: `recency` (`day`, `week`, `month`,
`year`), `domain_filter` (up to 16 hostnames), `max_results` (1 to 25) and `providers` (up to
16 names, to restrict the fan-out or reach an arm that fires only when named). The
[skill](cmd/jevlin/skill.md) is what the agent reads; the
[reference](docs/reference.md#the---stdin-request) has the envelope.

## Trajectory observability

A query on its own says little about whether a search was any good. Each search therefore
carries a small `trace` that lets the router group the searches of one task and see what the
agent was doing when it asked:

| field | carries |
|---|---|
| `harness` | Which agent: `claude-code`, `codex`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | The agent's ids, hashed with SHA-256 before they leave the machine. A subagent carries its own session id and its parent's. |
| `window`, `seq` | Which context window of the session, after compactions, and a call counter. |
| `history` | The assistant text just before the search, scrubbed of secrets, last 32 KiB. |

With `[miner] turn_end = true`, the agent's end-of-turn hook also sends what the turn
concluded: the final message, what you asked, the sequence of tools called with name, duration
and outcome (never a tool's input or output), the model and the token counts. Only for turns
in which a search was served.

The hooks keep a lineage file per workspace under `~/.jevlin/sessions`, so the searches of one
task line up even across context compactions and subagents, and only the agent that wrote a
file reads it. The scrub removes anything shaped like a credential, PEM blocks, email
addresses, the account name in a home path, and any value under a name like `password`,
`token` or `api_key`. [The trace](docs/reference.md#the-trace) and
[the turn end](docs/reference.md#the-turn-end) list every field and every scrub rule.

`export JEVLIN_TRACE=off` sends no trace and no turn end; searches work the same without them.

## What setup asks

Setup registers with the search platform and stores the key it gets back, so there is no key
to copy or paste. Then it asks, in this order:

| question | what your answer does |
|---|---|
| **Use it?** | Asked only when an earlier installation was set aside beside `~/.jevlin`. Yes moves it back. |
| **Enable mining rewards?** | No, the default, gives you search. Yes also enrolls this installation in Twilight testnet mining and asks for a payout address. |
| **Payout address** | Only if mining is on: a `twilight1…` address you control, or empty to create a wallet here. |
| **Add them to ~/.zshrc?** | Or whichever profile your shell reads. Puts `jevlin` on PATH and sets `JEVLIN_CONFIG`. On Windows it asks **Set them for your user?** instead. |
| **Set up the coding agents found on this machine now?** | Writes a skill and hooks into each agent's own config, after showing you what. |

Then it prints a link where you claim your agent on `platform.jevlin.ai`. The
[guide](docs/guide.md#setup) says what each answer does and which flags answer them for you.

## Then check

```bash
jevlin status          # what this installation has and has not completed
jevlin agents status   # which agents are set up
jevlin limits          # this key's spend caps and today's spend
jevlin search -format model "what is proof of authority consensus"
```

It is working when the search prints results and `agents status` lists your agents as
installed. Restart any agent that was already open.

## The agents it sets up

| agent | what is written | one thing to know |
|---|---|---|
| [Claude Code](docs/agents.md#claude-code) | a skill; hooks and allow rules in `~/.claude/settings.json` | On Windows, a search through its PowerShell tool asks for approval. |
| [Cursor](docs/agents.md#cursor) | a skill; hooks in `~/.cursor/hooks.json` | Cursor also runs Claude Code's hooks, which stand aside for it. |
| [Codex](docs/agents.md#codex) | a skill; on macOS and Linux, hooks in `~/.codex/hooks.json` and a permission profile in `~/.codex/config.toml` that opens only the search hosts; on Windows, writable directories in that file and no network | Its hooks run only once you approve them in Codex. Supported with Codex 0.158.0 or newer; older versions are not supported. |
| [opencode](docs/agents.md#opencode) | a plugin, and a line for you to paste into `AGENTS.md` | `/jevlin off` does not reach it. |
| [Pi](docs/agents.md#pi) | a skill and an extension under `~/.pi/agent/` | A resumed session keeps its place in the trace. |
| [Hermes](docs/agents.md#hermes) | a skill; a hook in `config.yaml` | Approve the hook once; it loads next session. |

Any other agent: [agents.md](docs/agents.md#any-other-agent) says what to paste. `jevlin agents
prefer off` makes the agent's own search the default again; naming jevlin in a request still
routes that one search here.

## What leaves the machine

The trace and the turn end go only to the search router, inside the search request and the
turn record. Nothing else reads them. The query goes in the JSON, never in a command line, so
it is in neither `ps` nor your shell history. With mining on, the rewards service receives
metadata only, never the query, the results or the text; see below.

## Upgrading and removing

```bash
jevlin upgrade                 # a native install: the latest release, verified before it runs
npm install -g @jevlinsearch/jevlin@latest   # an npm install is updated with npm
jevlin uninstall               # remove the skills, hooks and profile block; keep your wallet
```

The guide covers [upgrading](docs/guide.md#upgrading) and
[removing it and coming back](docs/guide.md#removing-it-and-coming-back).

## Mining on the Twilight testnet

Jevlin runs a Slot on [Twilight](https://docs.twilight.org), a proof-of-work chain whose work
is defined by Slots rather than by a puzzle. On that Slot, a search served by the router is a
unit of work. If you say yes to mining at setup, or run `jevlin mining enable` later, each
search is recorded on your machine and reported to the Slot's verifier after the fact:

- **One verified search per epoch makes you eligible**, and the epoch's share is split equally
  among everyone eligible. More searches do not earn more.
- **Your first reward takes one to two hours.** You join an epoch two ahead, and it has to
  close and settle first.
- **A long idle gap can miss an epoch.** Recorded searches are submitted by the next search or
  agent session, and evidence that arrives after the epoch's deadline is late.
- **Rewards are testnet TWLT**, paid to a `twilight1…` address you control. A wallet setup
  creates is sealed by the passphrase you choose, and its 24-word recovery phrase is printed
  once.

`jevlin doctor` says whether searches are recorded and earning, check by check, and `jevlin
earnings` reads what the chain has paid. Search works before you claim your agent, and with
mining off. The [guide](docs/guide.md#earning) has the detail; how the chain and its Slots work
is at [docs.twilight.org](https://docs.twilight.org).

## Documentation

- [Jevlin guide](docs/guide.md): setup in detail, bands, the trace, the wallet, earning,
  upgrading, removing.
- [Agents](docs/agents.md): what is written into each coding agent, and its known limits.
- [Reference](docs/reference.md): commands, the `--stdin` envelope, config, environment
  variables, files, the trace, the turn end, `doctor`'s checks.
- [CHANGELOG.md](CHANGELOG.md): what changed in each release.
- [SECURITY.md](SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.

## The npm wrapper

This package downloads the release binary for your platform on install, verifies it against the release checksums, and forwards every argument to it. The package version is the release tag it fetches.

- `JEVLIN_BINARY=/path` use a binary already on the machine
- `JEVLIN_SKIP_DOWNLOAD=1` install the wrapper without fetching
