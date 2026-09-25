# Agents

What `jevlin agents install`, and setup's agents question, writes into each coding agent.

**Found.** An agent counts as present by the command that launches it or, for Cursor, its config
directory; setup and `jevlin agents status` print which, and nothing is run to find out. One not
found is set up with `jevlin setup -with <id>` or `jevlin agents install -client <id>`.

**Rendered per shell.** Each command a skill teaches is written for the shell that agent runs on
your OS, with your paths filled in. An unestablished shell would get Bash, and the plan says so.

**Whose it is.** A skill, hook entry or allow rule is this installation's only when it runs one
of this installation's binaries **and** names this installation's config. Uninstall removes only
those, and leaves and reports what names another installation or none (one `jevlin agents
install` stamps the latter). An agent's one skill directory stays with the installation that
wrote it, and a hook file left empty is removed. `jevlin agents status` names any file an
earlier version wrote differently, and `jevlin agents install` refreshes it.

## Claude Code

### How it is found

`claude` on PATH.

### What is written and where

A skill in `~/.claude/skills/jevlin/`. In `~/.claude/settings.json`: five hooks (`PreToolUse` on
the Bash and PowerShell tools threads each search into the turn, three track the context window,
`Stop` flushes) and three `permissions.allow` rules, the single-quoted, quoted and bare spellings
of the search command. Reinstalling replaces those rules rather than adding more.

### What to know

Bash on macOS and Linux. On Windows the model picks Git Bash or PowerShell per call, so the skill
teaches both. The hook prefixes the search with the trace, which an allow rule (matched on how a
command begins) no longer matches, so the hook answers `allow` itself for exactly the search the
skill renders. Otherwise every search waits for approval, and a headless session refuses it.

### Known limits

**The sentence written just before a search does not travel.** Claude Code records a message
after the hook runs, so narration in the same message as the search is not in the trace. An
earlier message of the turn does travel, and the ids are correct either way.

**On Windows, a search through the PowerShell tool asks for approval each time.** It runs.
What a PowerShell permission rule looks like is not established, and a guessed one would never
match, so none is written. Searches through the Bash tool, with Git for Windows, are covered.

## Cursor

### How it is found

`cursor` or `cursor-agent` on PATH, or `~/.cursor`, since the editor's `cursor` command is optional.

### What is written and where

A skill in `~/.cursor/skills/jevlin/`; seven entries in `~/.cursor/hooks.json`, which the editor
and the Agent CLI both load.

### What to know

The hooks keep one lineage file per conversation, so two chats on one project do not relabel
each other's searches. `preToolUse` puts the conversation's identity in front of exactly the
search the skill renders and rewrites nothing else; the shell hook allows exactly that command.

On Windows, commands run in the terminal `terminal.integrated.defaultProfile.windows` names,
which this client cannot read, so the skill carries both forms: "If your terminal is PowerShell"
and "If your terminal is Git Bash". Use the matching one; the PowerShell form run from Git Bash
loses its encoding line, and a non-ASCII query goes out mangled.

Cursor also runs Claude Code's hooks from `~/.claude/settings.json`, with its own payload. They
recognize the caller from the payload and exit at once, doing nothing, so a Cursor turn ends
with one flush, not two, and no Cursor command is rewritten as Claude Code's.

### Known limits

**Windows, PowerShell terminal profile, non-ASCII queries.** On Cursor 3.21.9 on 2026-09-21 a
non-ASCII query reached the router intact under both profiles. Once, on Cursor 3.20.21 on
2026-09-18 under PowerShell, the router stored one double-encoded, answering a different
question. Git Bash was never affected; use it, or ASCII queries, to rule this out. Cursor's hook
wrapper re-encodes non-ASCII text, so such a search runs without Cursor's label.

**Windows: the command-line agent started from Git Bash cannot run any hook, so no search.**
Cursor runs its PowerShell hook wrapper with bash `eval`, which cannot parse it. Start the agent
from PowerShell instead; the editor is unaffected. Reported to Cursor (forum thread 172789).

## Codex

### How it is found

`codex` on PATH.

### What is written and where

A skill in `~/.codex/skills/jevlin/` and, when `~/.codex/config.toml` exists, a marked block in
it that widens the sandbox: network on, and writable roots for the state directory always, plus
the intake, sessions and spool directories when `[miner] enabled` is set.

### What to know

Bash on macOS and Linux, PowerShell on Windows. Without the block, a search returns results but
cannot record, so it earns nothing and the claim is never picked up. The config, key and wallet
are never writable, so a command gone wrong cannot change where your credentials go. Sandboxed
commands can read `credentials.json` and the state directory; on Windows, not the wallet.

Codex appends its own tables, such as folder trust or `[windows] sandbox`, to the end of
`config.toml`, where they can land between jevlin's markers. Install and uninstall change only
jevlin's one table, and move any other table inside the markers to just below the block, naming
it in the plan. Install writes the block where it sits, so nothing else moves (after an uninstall,
at the end). A block that is not valid TOML is left and reported; a key you added inside
jevlin's table goes with it, and the plan says so first. If you keep your own
`[sandbox_workspace_write]` table, install leaves it and prints the settings to add by hand.

### Known limits

None known.

## opencode

### How it is found

`opencode` on PATH.

### What is written and where

An in-process plugin, `~/.config/opencode/plugins/jevlin.js`, that threads each search into the
trace. There is no skill directory: install prints a rules block to paste into `AGENTS.md`.

### What to know

It runs Bash on macOS and Linux, and PowerShell on Windows.

### Known limits

`/jevlin off` and `agents prefer` do not reach it: its `AGENTS.md` line carries no preference.

## Pi

### How it is found

`pi` on PATH.

### What is written and where

A skill in `~/.pi/agent/skills/jevlin/`; an auto-discovered extension, `~/.pi/agent/extensions/jevlin.ts`.

### What to know

It runs Bash everywhere, Git Bash on Windows. The extension rewrites the search command with
the session, the call, the assistant text that led to that search, and the compaction count,
read back from the session Pi saved, so a resumed session keeps its place.

### Known limits

None known.

## Hermes

### How it is found

`hermes` on PATH.

### What is written and where

A skill in `<HERMES_HOME or ~/.hermes>/skills/jevlin/`; a `pre_tool_call` hook in a `hooks:`
block of its `config.yaml`. If you already have a `hooks:` section, install prints four lines to
paste under it instead of editing the file, and then counts them as set up.

### What to know

Bash everywhere, Git Bash on Windows. Both load next session. Hermes asks once to approve the
hook: approve it, or start Hermes with `--accept-hooks`, `HERMES_ACCEPT_HOOKS=1` or
`hooks_auto_accept: true`. Its shell tool is in the `terminal` and `coding` toolsets.

When Hermes saves `config.yaml` it may drop jevlin's markers and fold the command. The hook
works either way, and uninstall removes an entry in either form that names this installation,
with any key left empty above it. Anything else, such as an entry you gave a `timeout:`, is left
and reported with its line numbers.

### Known limits

Its hook payload has no assistant text or compaction state, so the trace carries only the hashed session, call and turn.

## Any other agent

### How it is found

It is not. `jevlin agents install` prints a rules block to paste when it finds no agent it knows.

### What is written and where

Nothing; you paste the block. It teaches the Bash form of the search command.

### What to know

Its searches carry a per-shell identity rather than an agent session.

### Known limits

No hooks: no trace text, and no flush at session end. Each search still starts one.
