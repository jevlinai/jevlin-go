---
name: jevlin
description: "Sets up Jevlin, free multi-provider web search for coding agents. Use when the user asks for Jevlin or jevlin search and the jevlin client is missing or not set up: it installs the client and runs `jevlin setup`, which writes this agent's own search skill and hooks. After setup, search through that installed `jevlin` skill, not this one."
---

# Set up Jevlin

Jevlin is one search that fans out to eight web-search providers and
returns merged, de-duplicated sources, free for coding agents. It only works
through its client: `jevlin setup` registers this installation, gets it a key
with no browser step, and writes the search skill and hooks into every agent
on this machine, which is what makes a search traced, approved without a
prompt, and counted. This skill does that setup. It does not search.

## 1. Check

```bash
jevlin status
```

If that prints an installation that has completed setup and lists this agent
under its agents, stop here and use the installed `jevlin` skill. If
`jevlin` is not found, go to step 2. If it is found but setup is incomplete,
go to step 3.

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
`jevlin` to PATH and writes the skill and hooks into every agent it finds
(Claude Code, Cursor, Codex, opencode, Pi, Hermes). Mining stays off unless
the user turns it on later with `jevlin mining enable`; search works either
way.

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

It is done when `agents status` lists this agent as installed. Tell the user
to restart any agent session that was already open, this one included, so it
loads the installed `jevlin` skill. That skill teaches the search: one JSON
request on stdin, one JSON envelope back. Do not search from this skill, and
do not paste search instructions of your own.

## Rules

- Never put a key in a command line or a file yourself; setup handles the key.
- Show the claim link verbatim. Everything else setup prints is for the user
  to read, not for you to act on.
- If `jevlin setup` is refused or fails, show the user what it printed and
  stop; `jevlin doctor` explains an installation that is not healthy.
