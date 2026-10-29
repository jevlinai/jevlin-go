<p align="center"><img src="https://raw.githubusercontent.com/jevlinai/jevlin-go/734aba4f98c8d484a0bcb26c4f2b25ffccca41a1/docs/assets/jevlin-logo.png" alt="jevlin" width="220"></p>
<p align="center">
  <a href="https://www.npmjs.com/package/@jevlinsearch/jevlin"><img src="https://img.shields.io/npm/v/@jevlinsearch/jevlin?label=npm&color=cb3837" alt="npm"></a>
  <a href="https://github.com/jevlinai/jevlin-go/releases"><img src="https://img.shields.io/github/v/release/jevlinai/jevlin-go?label=release" alt="release"></a>
  <a href="https://github.com/jevlinai/jevlin-go/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/jevlinai/jevlin-go/ci.yml?branch=main&label=ci" alt="ci"></a>
  <a href="https://github.com/jevlinai/jevlin-go/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="license"></a>
  <a href="https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen" alt="PRs welcome"></a>
  <a href="https://jevlin.ai"><img src="https://img.shields.io/badge/jevlin.ai-site-111" alt="jevlin.ai"></a>
  <a href="https://docs.twilight.org"><img src="https://img.shields.io/badge/twilight-docs-E8552F" alt="Twilight docs"></a>
</p>

Supersearch for coding agents, free for agents, with the trajectory behind every search: one call asks every engine in the set at once, every result names the engines that found it, and every engine leaves a receipt.
One binary gives Claude Code, Cursor, Codex, opencode, Pi and Hermes a `search` that can fan a
query out across eight search indexes at once, merges what they return into one attributed
list, and carries enough of the agent's context that the router can see the task a search
belongs to, not just the query string. There is no daemon, proxy or MCP server: between tool
calls nothing is running.

Search is free for agents: setup registers you with the search platform and gets a key, there
is no card and nothing to top up. What funds it is described
[at the end](#mining-on-the-twilight-testnet).


## Install

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Install it globally, never with `npx`: setup writes the binary's path into your agents' skills
and hooks, and npm throws away an `npx` cache or a project's `node_modules`.

An agent can run this install itself. The marketplaces carry a setup skill, not the search:
it installs the client and runs `jevlin setup`, which writes the agent's own skill and hooks.
Without setup there is no search.

```bash
npx skills add jevlinai/jevlin-go                          # any agent with skills (skills.sh)
/plugin install jevlin --marketplace jevlinai/jevlin-go    # Claude Code, 2.1.275 or later
claude plugin marketplace add jevlinai/jevlin-go           # Claude Code, older: then /plugin install jevlin@jevlin
codex plugin marketplace add jevlinai/jevlin-go            # Codex CLI: then /plugins
```

Cursor loads the same package from its Marketplace once it is listed there, and from the
repository until then. The same instructions, for an agent handed a link, are at
[jevlin.ai/agents.md](https://jevlin.ai/agents.md).

Without Node, download the archive for your OS and architecture from the
[releases page](https://github.com/jevlinai/jevlin-go/releases) and verify it against that
release's `checksums.txt`. Put `jevlin` in a directory only you can write, such as
`~/.local/bin`, and run `jevlin setup` from there. Setup and `agents install` refuse a binary
under the temp directory, or in a directory another user owns or every user can write, and
warn about one a shared group can write. On Windows only the temp-directory rule is checked.

## The providers

One request goes to the router at `api.jevlin.ai`, which holds the provider keys. In the
`balanced` and `max` [tiers](#three-tiers), the core arms run on every fan-out, under a
per-query cost ceiling; `fast` asks three fast indexes, one per index group:

| kind | arms |
|---|---|
| web indexes | Exa, Brave, Serper, Telem, Parallel search, TinyFish, Perplexity search |

More fire only when a request names them in `providers` (on `balanced` or `max`): SerpAPI (a
Google read in the slow lane: it keeps working after the response and lands on the trace), the
research agents (Parallel, Tavily research, the Perplexity agent, Octen), Tavily, Linkup and
Linkup answer, Valyu, Kagi, Mojeek, Gemini, Grok, Exa answer and Brave answer. The set is the
router's and changes without a client release; `decision` in every response says which arms
ran.

## What a search returns

The router runs the request against the arms and answers once, with everything they returned:

- **`result.merged`**: the router's own list of the pages every provider that answered
  returned, one entry per page, best first. Each entry carries `found_by`, the providers that found that page, and `best_rank`, the best
  position any of them gave it. Agreement between independent indexes is the fan-out's
  own evidence, and the skill tells the agent to read this list first.
- **`result.indexes`**: each index the router asked and how it answered, so a search where an
  index failed or the deadline cut it short reads as degraded rather than as an empty web.
- **`result.candidates`**: each provider's answer as it came back, so a disagreement can be
  inspected rather than averaged away. Only `"view":"full"` carries it: the default view says
  each page's text once, in the merged list.
- **`decision`**, which providers ran; **`usage.cost_micros`**, what the search cost, in
  millionths of a dollar; **`latency_ms`**, for the search and for each provider;
  **`request_id`**, so a search can be found again.

Agents call `jevlin search --stdin` with one JSON request and read one JSON object back, so
the query never touches the command line. A person can type `jevlin search "<query words>"`,
which does put it there.

```bash
jevlin search -format model "what is proof of authority consensus"
```

## Three tiers

Every search runs in one of three tiers. `fast` asks three fast indexes, one per index group
(Exa, Brave and Perplexity search as the router is deployed today), and answers in about a
second; `balanced` and `max` fan out to the core arms. A tier is also a per-query cost ceiling
on the router, and a set that would exceed the ceiling is trimmed rather than refused, with
`decision.trimmed` naming what went.

| tier | what runs | for | what it costs |
|---|---|---|---|
| `fast` | three fast indexes, one per index group, the quickest | a routine lookup, the default for a new key | about $0.015 a search |
| `balanced` | the core arms, 8 results each, a 12-page merged window, every arm's text | when the agent needs more than one source, or the first answer came back thin | about $0.035 |
| `max` | the core arms, 15 results each, a 24-page window, 2,000-character excerpts | corroboration: the same page found by independent indexes, and the most text per page | about $0.045, plus the tokens your agent spends reading it |

The dollar figures are the arms' own prices passed through with no markup; `usage.cost_micros`
on every response is the real number. A search is free while mining is on (the trajectory pays
for it); a key without mining draws credit. `max` and a named `providers` set (below) always
draw credit: they are the two ways to multiply the work a request does.

Who chooses:

- **You**, ahead of time: `/jevlin tier balanced` in an agent, or `jevlin agents tier balanced`
  from a shell, sets the tier every search carries unless a request names one. `/jevlin tier
  default` unsets it and `/jevlin tier` says what is in force.
- **The agent**, one step at a time: the skill tells it to leave `tier` alone so your tier
  applies, to ask for `balanced` only when it needs several sources beyond the three fast
  indexes (a thin result, indexes that disagree, the full set attributed), and never to send
  `max` on its own. `max` is yours to choose.
- **The router**, when nobody did: a request that names no tier, with none set, carries none, and
  the router applies the default for your key.

## Request options

These fields pass straight through to the router: `recency` (`day`, `week`, `month`,
`year`), `domain_filter` (up to 16 hostnames), `max_results` (1 to 25), `merged_limit` (1 to
50 pages in the merged list), `max_chars` (the merged list's total characters of page text) and
`providers` (up to 16 names, to restrict the fan-out or reach an arm that fires only when
named). A router that applies `recency` and `domain_filter` itself says so in
`result.filters_applied`: it sent each filter to every provider that takes it and dropped any
page outside it. Without that report the results may be unfiltered, and the skill tells the
agent to check each page's date and host itself. A named set
runs on `balanced` or `max` only: on `fast` the router refuses it, since the fast set is the
router's. It is billed at the named arms' own prices, draws credit even with mining on, and
earns no reward, because a single-arm search carries none of the comparison a trajectory is
paid for. The
[skill](https://github.com/jevlinai/jevlin-go/blob/main/cmd/jevlin/skill.md) is what the agent reads; the
[reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#the---stdin-request) has the envelope.

## Trajectory observability

A query on its own says little about whether a search was any good. Each search therefore
carries a small `trace` that lets the router group the searches of one task and see what the
agent was doing when it asked:

| field | carries |
|---|---|
| `harness` | Which agent: `claude-code`, `codex`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | The agent's ids, hashed with SHA-256 before they leave the machine. A subagent has a session id of its own, and from Claude Code, Codex and opencode it also carries its parent's. |
| `window`, `seq` | Which context window of the session, after compactions, and a call counter. |
| `history` | The assistant text just before the search, scrubbed of secrets, last 32 KiB. Codex and Hermes send none. |

With `[mining] turn_end = true`, the agent's end-of-turn hook also sends what the turn
concluded: the final message, what you asked, the sequence of tools called with name, duration
and outcome (never a tool's input or output), the model and the token counts. Only for turns
in which a search was served.

The hooks keep a lineage file per workspace (in Cursor, per conversation) under
`~/.jevlin/sessions`, so the searches of one task line up even across context compactions and
subagents, and only the agent that wrote a file reads it. The scrub removes anything shaped
like a credential, PEM blocks, email addresses, the account name in a home path, an
`Authorization` header's credential, and any value under a name like `password`, `token` or
`api_key`. [The trace](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#the-trace) and
[the turn end](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#the-turn-end) list every field and every scrub rule.

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
| **Switch?** | Asked only when Codex's config has a setting of your own that its permission profile would change. It shows every line it would write; a bare Enter is no and leaves Codex as it is. |

After the mining questions, and before the shell profile, it prints a link where you claim
your agent on `platform.jevlin.ai` and waits up to three minutes for you to. You need not do it
then: search already works, and using an agent later finishes the claim. The
[guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#setup) says what each answer does and which flags answer them for you.

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
| [Claude Code](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#claude-code) | a skill; hooks and allow rules in `~/.claude/settings.json` | On Windows, a search through its PowerShell tool asks for approval. |
| [Cursor](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#cursor) | a skill; hooks in `~/.cursor/hooks.json` | Cursor also runs Claude Code's hooks, which stand aside for it. |
| [Codex](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#codex) | a skill; on macOS and Linux, hooks in `~/.codex/hooks.json` and a permission profile in `~/.codex/config.toml` that opens only the search hosts; on Windows, writable directories in that file and no network | Its hooks run only once you approve them in Codex. Supported with Codex 0.158.0 or newer; older versions are not supported. |
| [opencode](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#opencode) | a plugin, and a block for you to paste into `AGENTS.md` | `/jevlin off` does not reach it. |
| [Pi](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#pi) | a skill and an extension under `~/.pi/agent/` | A resumed session keeps its place in the trace. |
| [Hermes](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#hermes) | a skill; a hook in `config.yaml` | Approve the hook once; it loads next session. |

Any other agent: [agents.md](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md#any-other-agent) says what to paste. `jevlin agents
prefer off` makes the agent's own search the default again; naming jevlin in a request still
routes that one search here.

## Commands: in the agent, and in a shell

Setup installs one skill, `jevlin`, the supersearch skill, into each agent that has skills (Claude Code, Cursor,
Codex, Pi, Hermes; opencode gets a plugin and a rules block instead). The skill is what the
agent reads: it tells the agent to prefer this search over its built-in one, how to build the
request, how to read the envelope, and what the commands below do. Everything the agent can do
with jevlin goes through that one skill.

| in the agent | what it does |
|---|---|
| `/jevlin <question>` | One search, as the skill specifies: the query goes as JSON on stdin, the merged list comes back, the agent answers from it. Naming jevlin or supersearch in an ordinary request does the same. |
| `/jevlin off` | The agent's built-in web search becomes the default again; this one runs only when you name it. The skill is rewritten to say so, in every agent, from its next start. |
| `/jevlin on` | This search is the default again. |
| `/jevlin status` | Which of the two is the default right now. |
| `/jevlin tier fast\|balanced\|max` | Sets the tier every search carries unless a request names one. |
| `/jevlin tier default` | Unsets it; `[router] tier` in your config applies if set, else the router's default for your key. |
| `/jevlin tier` | The tier in force. |

The settings are yours: the skill tells the agent never to change the default or the tier on
its own. `/jevlin off` does not reach opencode, whose rules block carries no preference.

Those are the only forms the skill teaches, because they are the only ones an agent needs
mid-task. Everything else is the `jevlin` command in a shell. The agent can run these too when
you ask it to; the skill points it at `status` and `doctor` when a search succeeds but mining
says it is not earning.

| command | what it does |
|---|---|
| `jevlin search` | One search. `--stdin` for agents (JSON in, JSON out); `<query words>` for a person. |
| `jevlin setup` | Everything after the binary: previous installation, config, registration, profile, agents. |
| `jevlin status` | What this installation has and has not completed. `-json` for one envelope. |
| `jevlin doctor` | Whether searches are recorded and earning, check by check, and the one thing to fix. `-json` too. |
| `jevlin limits` | This key's spend caps, rate windows and today's spend, read from the router. A claimed key gets 120 searches a minute, 3,000 an hour and 16 at once; an unclaimed one 10, 120 and 2, with $0.10 a day. |
| `jevlin agents install\|status\|uninstall` | Sets up, reports or removes the skills and hooks of each agent. |
| `jevlin agents prefer on\|off\|status` | What `/jevlin on\|off\|status` runs: which search is the default. |
| `jevlin agents tier <tier>\|default\|status` | What `/jevlin tier` runs: the tier every search carries. |
| `jevlin mining enable\|disable` | Turns Twilight mining on, asking for a payout address, or off. |
| `jevlin earnings` | What the chain has paid to your payout address. |
| `jevlin wallet init\|address\|register\|balance\|send` | The wallet setup made, or one you make here. |
| `jevlin flush` | One pass of the mining plane by hand: join the open epoch, submit recorded searches. |
| `jevlin connect` | Registers with the search platform, prints the claim link, polls until claimed. |
| `jevlin upgrade` | A native install: the latest release, verified before it runs; `-rollback` swaps back. |
| `jevlin uninstall` | Removes what setup wrote; `-binary` and `-purge-state` go further. |
| `jevlin version`, `jevlin help` | The version, and the full usage text. |

`login`, `enroll`, `payout` and `join` are the portal's older manual path. A
command that reads the config takes `-config <file>`; `setup`, `upgrade` and `uninstall` take
`-home <dir>` instead, the installation. The [reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#commands) has every flag.

The skill's text is [`cmd/jevlin/skill.md`](https://github.com/jevlinai/jevlin-go/blob/main/cmd/jevlin/skill.md), rendered per agent and per
shell at install; the installed copy is at `~/.claude/skills/jevlin/`, `~/.cursor/skills/jevlin/`,
`~/.codex/skills/jevlin/`, `~/.pi/agent/skills/jevlin/` or `~/.hermes/skills/jevlin/`.
[Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md) says what else is written into each one.

## What leaves the machine

The trace and the turn end go only to the search router, inside the search request and the
turn record. Nothing else reads them. An agent's query goes in the JSON on stdin, so it is in
neither `ps` nor a shell history. The human form, `jevlin search "<query words>"`, puts the
query on the command line, where both can see it; use `--stdin` for anything sensitive. With
mining on, the rewards service receives metadata only, never the query, the results or the
text; see below.

## Upgrading and removing

```bash
jevlin upgrade                 # a native install: the latest release, verified before it runs
npm install -g @jevlinsearch/jevlin@latest   # an npm install is updated with npm
jevlin uninstall               # remove the skills, hooks and profile block; keep your wallet
```

Once a day, when an agent session starts or ends, jevlin also checks for a newer release, and
`jevlin status` says what it found. A native install replaces its own binary with it, with the
same verification as `jevlin upgrade`, unless the config says `[update] mode = "notify"`: then
it only tells you. An npm install is only ever told. `mode = "off"` stops the check.

The guide covers [upgrading](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#upgrading) and
[removing it and coming back](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#removing-it-and-coming-back).

## Mining on the Twilight testnet

Search is free because router traffic is proof of work. Jevlin runs a Slot on
[Twilight](https://docs.twilight.org), a chain whose work is defined by Slots rather than by a
puzzle, and on that Slot a search served by the router is a unit of work: the chain pays the
Slot, and the Slot's share is what funds the searches. If you say yes to mining at setup, or
run `jevlin mining enable` later, your searches earn you a part of that share too. Each search
is recorded on your machine and reported to the Slot's verifier after the fact:

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
mining off. The [guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#earning) has the detail; how the chain and its Slots work
is at [docs.twilight.org](https://docs.twilight.org).

## Documentation

- [Jevlin guide](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md): setup in detail, tiers, the trace, the wallet, earning,
  upgrading, removing.
- [Agents](https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md): what is written into each coding agent, and its known limits.
- [Reference](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md): commands, the `--stdin` envelope, config, environment
  variables, files, the trace, the turn end, `doctor`'s checks.
- [CHANGELOG.md](https://github.com/jevlinai/jevlin-go/blob/main/CHANGELOG.md): what changed in each release.
- [SECURITY.md](https://github.com/jevlinai/jevlin-go/blob/main/SECURITY.md): how to report a vulnerability.
- [CONTRIBUTING.md](https://github.com/jevlinai/jevlin-go/blob/main/CONTRIBUTING.md): how to propose a change.

## License

Apache-2.0.

## The npm wrapper

This package downloads the release binary for your platform on install, verifies it against the release checksums, and forwards every argument to it. The package version is the release tag it fetches.

- `JEVLIN_BINARY=/path` use a binary already on the machine
- `JEVLIN_SKIP_DOWNLOAD=1` install the wrapper without fetching
