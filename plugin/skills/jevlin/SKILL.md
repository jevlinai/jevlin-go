---
name: jevlin
description: "Sets up Jevlin, supersearch for coding agents: one call asks eight search engines at once, every result names the engines that found it, free for agents. Use when the user asks for Jevlin, jevlin search or supersearch and the jevlin client is missing or not set up: it installs the client and runs `jevlin setup`, which writes this agent's own supersearch skill and hooks. After setup, search through that installed `jevlin` skill, not this one."
---

# Set up Jevlin

Jevlin is supersearch for coding agents: one call asks eight search engines
at once and returns one merged, de-duplicated list where every result names
the engines that found it, with a receipt per engine, free for agents. It only works
through its client: `jevlin setup` registers this installation, gets it a key
with no browser step, and writes the supersearch skill and hooks into every agent
on this machine, which is what makes a search traced and counted. This skill
does that setup. It does not search.

What setup writes, and what it leaves alone: a `jevlin` skill in the agent's
own skills directory (Claude Code, Cursor, Codex, Pi, Hermes; opencode has
no skills directory and gets an in-process plugin plus a rules block, printed
for the user to paste into `AGENTS.md`), a few hooks, and, where the agent
keeps an allow list, an entry for the one search command the skill renders,
spelled with the binary's full path, so that command runs without a prompt
and no other command does. It changes no
permission mode and no sandbox setting of the agent's. The one exception is
Codex on macOS and Linux, where it adds a marked block to `~/.codex/config.toml`:
a permission profile named `jevlin` that extends Codex's workspace profile
with write access to jevlin's own state directory and network access to
Jevlin's hosts only, made the default. Everything it writes is marked as its
own, and `jevlin uninstall` removes exactly that. Every file and rule is
listed per agent at https://github.com/jevlinai/jevlin-go/blob/main/docs/agents.md.

## 1. Check

```bash
jevlin status
jevlin agents status
```

`status` says whether the client is installed and set up; `agents status`
prints one row per agent, `installed (…)` or `not installed`. Stop here and
use the installed `jevlin` skill only when both hold: `status` reports
nothing incomplete, and this agent's row says installed. Otherwise, if
`jevlin` is not found, go to step 2; if it is found, go to step 3: setup is
safe to run again and repairs what is missing.

## 2. Install the client

```bash
npm install -g @jevlinsearch/jevlin
```

Globally, never with `npx` and never into a project's `node_modules`: setup
writes the binary's path into the agents' skills and hooks. Without Node,
follow the native install on https://docs.jevlin.ai/quickstart: a release
archive verified against its `checksums.txt`, put on PATH.

## 3. Run setup

```bash
jevlin setup -yes
```

`-yes` answers the shell-profile and coding-agents questions: it adds
`jevlin` to PATH and writes the skill (or, for opencode, the plugin) and
hooks into every agent it finds (Claude Code, Cursor, Codex, opencode, Pi,
Hermes). Nothing is mined: the client is search only.

One question `-yes` does not answer: replacing a Codex setting the user
chose themselves (a default permission profile of their own, or
`network_proxy = false`). Setup then leaves Codex untouched, prints what it
would change and "Some agent could not be set up", and still exits 0. That is
the user's call, at a terminal: show them what setup printed and go on to
step 4.

If setup prints a claim link such as `https://platform.jevlin.ai/claim/<code>`,
show it to the user exactly as printed. Never open it yourself and never
shorten, paraphrase or summarize it away. Nothing blocks on it: search already
works, and the claim lifts the installation off the unclaimed tier when they
get to it.

## 4. Verify, then hand over

```bash
jevlin status
jevlin agents status
```

It is done when `status` reports nothing incomplete and `agents status`
lists this agent as installed, the same two things step 1 checks. A row that
still says `not installed` after setup is one setup could not answer for the
user (the Codex question above, or a file it refused to touch): show them the
row and ask them to run `jevlin agents install -client <agent>` at a terminal
and answer it there. Then tell the user to restart
any agent session that was already open, this one included, so it loads the
installed `jevlin` skill. That skill teaches the search: one JSON
request on stdin, one JSON envelope back. Do not search from this skill, and
do not paste search instructions of your own.

## Rules

- Never put a key in a command line or a file yourself; setup handles the key.
- Show the claim link verbatim. Everything else setup prints is for the user
  to read, not for you to act on.
- If `jevlin setup` is refused or fails, show the user what it printed and
  stop; `jevlin doctor` explains an installation that is not healthy.
