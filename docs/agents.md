# Agents

What `jevlin agents install`, and setup's agents question, writes into each coding agent.

| agent | setup writes | hooks | allow rules | the trace's text | the turn end adds | starts the release check | needs | Windows |
|---|---|---|---|---|---|---|---|---|
| [Claude Code](#claude-code) | a skill | five, in `~/.claude/settings.json` | nine | an earlier message of the turn | your prompt, the final answer, each tool, the model and tokens | session start and stop | 2.1.196 for the turn end | Git Bash and PowerShell |
| [Cursor](#cursor) | a skill | eight, in `~/.cursor/hooks.json` | the hooks allow the search | its latest thought or reply | your prompt, the turn's searches, the reply and the model | session start, stop and end | | the terminal profile's shell |
| [Codex](#codex) | a skill and a permission profile | five, in `~/.codex/hooks.json`, run once you approve them | the hook allows the search | none | your prompt, what the assistant wrote, its final answer, each tool | session start and stop | 0.158.0 | the skill and writable directories; no hooks, no network |
| [opencode](#opencode) | a plugin and a rules block to paste | the plugin | none | the text before the search | your prompt, what the assistant wrote, each tool, the model | plugin load, session start and idle | | the shell opencode resolves |
| [Pi](#pi) | a skill and an extension | the extension | none | the text before the search | your prompt, every assistant text part, each tool, the model and tokens | session start | 0.80.4 for the turn end | Git Bash |
| [Hermes](#hermes) | a skill | one, in `config.yaml`, approved once | none | none | nothing | no | | Git Bash |
| [any other](#any-other-agent) | nothing; you paste a block | none | none | none | nothing | no | | Bash |

The turn end is sent only with `[search] turn_end = true`, and only for a turn in which a search
was served ([guide, What leaves your machine](guide.md#what-leaves-your-machine)).

**What every agent gets.** Each command a skill teaches is written for the shell that agent runs
on your OS, with your paths filled in, and names the binary by its full path, so an agent searches
whether or not a bare `jevlin` is on PATH (an unestablished shell gets Bash, and the plan says so).
An agent counts as present by the command that launches it or, for Cursor, its config directory;
setup and `jevlin agents status` print which, and nothing is run to find out. One not found is set
up with `jevlin setup -with <id>` or `jevlin agents install -client <id>`. A file is replaced whole,
through a temporary file renamed over it, so a run stopped part way leaves it as it was; a link is
written through to what it names, a file keeps its permissions, and one you made read-only is left
alone and reported. If one of an agent's files cannot be written, that agent's removals wait for
the next run, so it is never left half set up. A skill, hook entry or allow rule that runs a
command is this installation's only when it runs one of this installation's binaries **and** names
this installation's config; uninstall removes only those, and leaves and reports what names
another installation or none (one `jevlin agents install` stamps the latter). An agent's one skill
directory stays with the installation that wrote it, and a hook file left empty is removed.
`jevlin agents status` names any file an earlier version wrote differently, and
`jevlin agents install` refreshes it.

## Claude Code

### How it is found

`claude` on PATH.

### What is written and where

A skill in `~/.claude/skills/jevlin/`. In `~/.claude/settings.json`: five hooks (`PreToolUse` on
the Bash and PowerShell tools threads each search into the turn; `SessionStart`, `PreCompact` and
`PostCompact` track the context window, and `SessionStart` also starts the daily release check;
`Stop` reports the turn end and starts the check) and nine `permissions.allow` rules. Six are the
single-quoted, quoted and bare spellings of the search command and of `fetch --stdin`, each ending
with the installation's `-config <file>` (before setup has written that file it names the path
setup will write). Three name folders and no binary: an `Edit` and a `Read` rule for the `requests`
folder beside that config, and a `Read` rule for the skill's own folder, so the agent opens the
`reference.md` beside `SKILL.md` without a question.

The `requests` folder is for a request that holds a `'` or a `$`, which cannot ride on the one-line
call: the skill has the agent write it there with the Write tool, which Claude Code governs by
`Edit` rules, and the call reads it with `--stdin < <file>`, which Claude Code checks against
`Read` rules. Nothing creates the folder (the Write tool does) and nothing prunes it; the text is
also in the transcript Claude Code keeps. Reinstalling replaces the rules rather than adding more,
including rules an earlier version wrote without a config.

### What to know

- **Trace:** an earlier message of the turn. Claude Code records a message after the hook runs, so
  narration in the same message as the search is not in the trace; the ids are correct either way.
- **Turn end:** your prompt, the final answer, each tool by name, duration and outcome, the model
  and its token counts.
- **Release check:** at session start and stop.

Bash on macOS and Linux. On Windows the model picks Git Bash or PowerShell per call, so the skill
teaches both. The hook prefixes the search with the trace, which an allow rule (matched on how a
command begins) no longer matches, so the hook answers `allow` itself for exactly the search the
skill renders, under the Bash tool and the PowerShell tool alike; anything else waits for approval,
and a headless session refuses it. The hook answers `allow` for the same search read from a
request file directly inside the `requests` folder holding one version-1 request, and for exactly
the fetch the skill renders, which it does not rewrite: the allow rules cover that under the Bash
tool, and the hook under the PowerShell tool, which the rules cannot name.

### Known limits

The turn end needs Claude Code 2.1.196 or later. Earlier versions give the hook no prompt id, so
with `turn_end` on they send no final answer; searches are unaffected.

## Cursor

### How it is found

`cursor` or `cursor-agent` on PATH, or `~/.cursor`, since the editor's `cursor` command is optional.

### What is written and where

A skill in `~/.cursor/skills/jevlin/`; eight entries in `~/.cursor/hooks.json`, which the editor
and the Agent CLI both load.

### What to know

- **Trace:** Cursor's latest thought or reply, kept by its `afterAgentThought` and
  `afterAgentResponse` hooks.
- **Turn end:** your prompt, the turn's searches, the reply and the model. jevlin installs no hook
  behind every tool, so its other tools are not listed.
- **Release check:** at session start, stop and end.

The hooks keep one lineage file per conversation, so two chats on one project do not relabel each
other's searches. `preToolUse` puts the conversation's identity in front of exactly the search the
skill renders and rewrites nothing else; the shell hook allows exactly that command, and exactly
the fetch the skill renders, which carries no identity. The hook that runs when you submit a prompt
keeps nothing unless you turned `turn_end` on; then it holds what you asked, scrubbed, until that
turn ends.

On Windows, commands run in the terminal `terminal.integrated.defaultProfile.windows` names, which
this client cannot read, so the skill carries both forms: "If your terminal is PowerShell" and "If
your terminal is Git Bash". Use the matching one; the PowerShell form run from Git Bash loses its
encoding line, and a non-ASCII query goes out mangled.

Cursor also runs Claude Code's hooks from `~/.claude/settings.json`, with its own payload. They
recognize the caller from the payload and exit at once, so a Cursor turn's end is handled once, not
twice, and no Cursor command is rewritten as Claude Code's.

### Known limits

**Windows, PowerShell terminal profile, non-ASCII queries.** A non-ASCII query has reached the
router double-encoded under the PowerShell profile, answering a different question; Git Bash has
not shown it. Use Git Bash, or ASCII queries, to rule it out. Cursor's hook wrapper re-encodes
non-ASCII text, so such a search runs without Cursor's label.

**Windows, an apostrophe in a PowerShell-form search.** Git Bash reads the PowerShell form's
here-string as an ordinary single-quoted string, which an apostrophe in the request closes, and an
interactive terminal then runs the rest of that line. The hooks cannot tell which terminal you
picked, so they allow that form without asking only when its request holds no `'`; one that holds
it waits for your approval and reaches the router without Cursor's label. The skill says to write
an apostrophe as `'`, which the router receives as the same query.

**Windows: the command-line agent started from Git Bash cannot run any hook, so no search.**
Cursor runs its PowerShell hook wrapper with bash `eval`, which cannot parse it. Start the agent
from PowerShell instead; the editor is unaffected.

## Codex

### How it is found

`codex` on PATH.

### What is written and where

A skill in `~/.codex/skills/jevlin/`; on macOS and Linux, five hooks in `~/.codex/hooks.json`
(`PreToolUse`, `SessionStart`, `PreCompact`, `PostCompact`, `Stop`), each written where any of
jevlin's stood before so no other hook moves; and a marked block in `~/.codex/config.toml`, which
install creates if it is not there. On macOS and Linux the block is a permission profile; on
Windows it is the writable directories only ([Windows](#windows)).

### What to know

- **Trace:** no text: the session, turn and call only.
- **Turn end:** read from Codex's own session file when `Stop` runs: what you asked, what the
  assistant wrote along the way, its final answer, and each tool call by name and outcome, never a
  tool's input or output. A subagent's turn sends nothing of its own.
- **Release check:** at session start and stop, from a hook outside the sandbox.

Bash on macOS and Linux, PowerShell on Windows. Supported Codex is 0.158.0 or newer. Older versions
are not supported: below 0.131 Codex refuses the whole file with "invalid type: map, expected a
boolean" and does not start (upgrade Codex, or remove the block between its two markers), and 0.131
to 0.157 has not been tried.

Codex runs the hooks only after you approve them: start `codex`, or open the app, and approve them
when it asks you to review hooks. Until then they do nothing, and under `codex exec` nothing says
so; searches still run, without a session or a turn. jevlin never records an approval for you.
`jevlin agents status` says what Codex has on record, read from its `config.toml`: an approval for
some or all of the hooks, none, or that it cannot tell. An approval on record may be for an earlier
version of a command, and whether it still counts is Codex's to decide. A hook whose command
changes, after you move the binary or the config, may need approving again; an upgrade in place
does not change the commands.

With the hooks approved, a search Codex runs carries its session, turn and call, and a subagent's
search names the session that started it. Only the search command the skill shows is given them,
and allowed without a prompt; anything else, including the search after a `cd`, in a loop or piped
on, runs as written and carries the per-shell identity. The fetch command the skill shows is
allowed without a prompt as well, and anything else that fetches is asked about.

#### The permission profile

The block is a Codex permission profile named `jevlin`, made the default with
`default_permissions`. It extends Codex's `:workspace` profile, or a profile of your own that you
agreed to switch from, with write access to the state and sessions directories, and network access
to the search hosts only: the router and the platform's agents API. A host on this machine's
loopback is never listed, because no command in Codex's sandbox can reach it and listing it would
open every local port; the plan says when a service is on loopback. The block also turns on
Codex's `network_proxy` feature, which is what enforces the host list: with it on, a host neither
profile lists is closed to the commands Codex runs from this config. The block sits before the
first table in the file, because `default_permissions` must, and is added and taken out without a
byte of its own, so uninstall gives your file back exactly as it was. If you change `[router] url`
or `[agents] url` in your config, run `jevlin agents install` again; `jevlin agents status` says
when the profile names other hosts than the config does.

Codex writes into its own config file: tables at the end, a `[features]` table from
`codex features enable`, settings from the desktop app. Some of that can land between jevlin's
markers. Install and uninstall change only jevlin's own lines, move a setting Codex wrote inside the
markers to just above them and a table to just below them, and name each in the plan. Markers count
only as lines of their own outside a multi-line string. jevlin writes its
`[features.network_proxy]` table last in its block, because `codex features disable network_proxy`
deletes the comments above the table it removes; a block that has lost a marker all the same is
still recognized, repaired by the next install and removed whole by uninstall. A file that starts
with a UTF-8 byte-order mark keeps it. An earlier version's block, which opened the network to every
host, is replaced by the profile on the next install, and the plan says so.

A change Codex's own parser would refuse is not written: a `features` or `permissions` table you
wrote inline or with dotted keys, where jevlin's table header would collide with it, is named in
the plan, and nothing is written for Codex. A block that is not valid TOML, or whose profile holds
anything jevlin did not write, is left and reported; when uninstall leaves jevlin's own block, Codex
still runs its commands under jevlin's profile, so uninstall names the block as what it left, with
every line of yours jevlin changed and what to put back, and exits 1. Another installation's block
is left to that installation, and named. A `default_permissions = "jevlin"` line left with jevlin's
mark after the block was removed by hand is one Codex cannot load: status says so, install refuses
over it, and both name `jevlin agents uninstall -client codex`, which puts back the line it
replaced.

#### The Switch? question

A setting of your own that the profile would change is asked about first, in one question that
shows every line it would write, and `-yes` does not answer it. The settings are: a profile of
yours named in `default_permissions`, `default_permissions = ":workspace"`, a bare
`sandbox_mode = "workspace-write"` (commented out), and `network_proxy = false` in your
`[features]` table (set to true). jevlin never writes a line inside a table of yours: it changes
only those lines, each marked with a comment naming its config, and `agents uninstall` puts each
back together with jevlin's block, unless you have changed it since.

With a profile of yours, jevlin's profile extends it and Codex merges the two, so your hosts and
roots still apply. Once jevlin's profile turns the network on, every host your profile or the
profiles it extends allows is reachable too, even where your own network was off: each host is
decided, as Codex decides it, by the nearest profile that names it, and a wildcard deny (any key
holding `*` or `?`) wins over any exact allow, jevlin's included. The question names every such
host, or says every host but those denied where one allows `"*"`; install refuses for Codex when
the router's host is denied. If your profile's network is already open with the proxy off, jevlin's
adds only its roots and leaves your network as it is.

Answering no installs nothing for Codex and exits 0. No answer stops the install, exit 2, with
nothing written but one line: if jevlin's own block still holds an open network from an earlier
version, that is closed whatever happens, and the message says so. Without a terminal,
`agents install` prints the exact lines to change by hand and the block to add, with its markers,
every line flush left; where the file already has a block of jevlin's, it says to replace that
block. It exits 2, and a file finished that way is jevlin's. `setup` reports the agent it could not
set up and exits 0. A dry run prints the question and plans the yes. A read-only sandbox, or a
`[sandbox_workspace_write]` table of your own, gets nothing for Codex and the profile to adopt
printed. With `danger-full-access` there is nothing to widen, and only the skill and hooks are
written. If the profile in the file is another jevlin installation's, nothing is written for Codex
and the plan names it.

#### Network and sandbox

Without the block, a search returns results but cannot write its lineage file or its turn mark,
so its searches carry no session and no turn end is sent, and the claim resume it starts cannot
write, so the claim is never picked up. The config and key are never writable, so a command gone
wrong cannot change where your credentials go. Sandboxed commands can read `credentials.json` and
the state directory, and can leave files in the directories the block makes writable; jevlin's
hooks, which run outside the sandbox, never write through a link left there and never wait on a
pipe left at a name they read. If your config puts `jevlin.toml`, or the folder that holds
`credentials.json`, inside one of those directories, the install widens nothing and says why: give
each directory its own place, as setup does.

Inside the sandbox Codex routes commands through a proxy on this machine, and the claim resume a
search starts reaches the agents API through it. A process a search starts contacts only the hosts
the profile lists, because Codex fails whichever command is running when anything inside the
sandbox is refused a host. `codex features disable network_proxy` deletes the profile's
`[features.network_proxy]` table, and the profile then lets every command Codex runs reach any
host; `jevlin agents status` says so in those words, and `jevlin agents install` puts the table
back. Status reads only `~/.codex/config.toml`: a trusted project's own `.codex/config.toml`, or
`codex -p <name>` with `<name>.config.toml`, that sets `network_proxy = false` turns the proxy off
there while jevlin's profile is active, and status cannot see it. Don't turn the proxy off in a
project config while jevlin's profile is your default.

#### Windows

No permission profile has been seen to work in Codex's Windows sandbox, so none is written. The
block makes the same directories writable and opens no network: a search there needs Codex's
approval to reach the router. No hooks are written either, because what runs a Codex hook there has
not been seen. A sandbox's setup may add its own group to the state directory's access list;
`doctor`'s `state access` line names whoever besides you is on it, and does not call that a
problem, and running `jevlin setup` again gives `<home>\state` an owner-only list until the
sandbox's setup adds it again.

### Known limits

A Codex turn end gives a shell command no duration: the session file Codex 0.160.0 writes holds
none that is the command's. A client of Codex's app server that starts a thread with an explicit
sandbox mode discards the profile, and the network is then off for that thread. On Linux, Codex
runs each command in its own process namespace, so a claim resume a search starts ends with the
search; a search from another agent or your shell, or `jevlin connect`, picks the claim up instead.

## opencode

### How it is found

`opencode` on PATH.

### What is written and where

An in-process plugin, `~/.config/opencode/plugins/jevlin.js`, that threads each search into the
trace and starts the daily release check. There is no skill directory: install prints a rules
block to paste into `AGENTS.md`.

### What to know

- **Trace:** the assistant text before the search.
- **Turn end:** when a turn that searched goes idle, the plugin pipes it to jevlin, which sends it
  only with `turn_end` on: what you asked, what the assistant wrote, each tool by name, time and
  outcome, and the model. A tool's input and output are never read into it.
- **Release check:** when the plugin loads, when a top-level session is created and at each of its
  idles (opencode reports an idle twice, and the second start finds the check not due).

It runs Bash on macOS and Linux. On Windows it resolves its shell when it starts: `$SHELL` wins
whenever it is set (a terminal like Git Bash sets it, and opencode then runs its bash tool in
bash), and only without one does it take the first of pwsh, PowerShell, Git Bash and cmd it finds.
The plugin follows the same signal per call, so a search carries its bridge in the syntax of the
shell that will actually run it. A subagent runs as a child session, and its searches name the
session that started it; the plugin learns that from the session's creation, or asks opencode once.

### Known limits

`/jevlin off` and `agents prefer` do not reach it: its `AGENTS.md` block carries no preference.

## Pi

### How it is found

`pi` on PATH.

### What is written and where

A skill in `~/.pi/agent/skills/jevlin/`; an auto-discovered extension, `~/.pi/agent/extensions/jevlin.ts`.

### What to know

- **Trace:** the assistant text that led to the search, with the session, the call, the turn and
  the compaction count, read back from the session Pi saved, so a resumed session keeps its place.
- **Turn end:** when Pi's run has settled, the extension builds the turn from the session Pi saved
  and hands it to jevlin, which sends it only with `turn_end` on: what you asked, every text part
  the assistant wrote, each tool by name, duration and outcome, the model and its token counts. A
  tool's input and output are never read into it, and Pi's system prompt is never looked at. The
  text is scrubbed in the extension and again by jevlin.
- **Release check:** when a session starts.

It runs Bash everywhere, Git Bash on Windows. A turn is named by the id Pi saved for the message
you wrote: two searches under one prompt share it, the next prompt's differ, and a resumed session
names its turns as it did before. A fork is a session of its own. Pi can run several loops for one
prompt, retrying a failed request or continuing after it compacts, each ending with `agent_end`;
the report waits for `agent_settled`, which comes once, after the last of them.

### Known limits

The turn end needs Pi 0.80.4 or later, the first to report a settled run; on an earlier Pi the
searches carry their turn and no turn is reported. Before Pi 1.1.0, which reports whether a run was
aborted, a turn is reported as interrupted only when its last answer says so. A run an extension
starts with a message of its own, not yours, belongs to your last message: its searches share that
turn, which has by then been reported, and are joined to no turn end. The turn end has been run
live on Pi 1.3.0 on Linux, and not on macOS or Windows.

## Hermes

### How it is found

`hermes` on PATH.

### What is written and where

A skill in `<HERMES_HOME>/skills/jevlin/` (default `~/.hermes`, on Windows `%LOCALAPPDATA%\hermes`);
a `pre_tool_call` hook in a `hooks:` block of its `config.yaml`. If you already have a `hooks:`
section, install prints the lines to paste instead of editing the file, beginning with `hooks:`, and
then counts them as set up.

### What to know

- **Trace:** no text: its hook payload has no assistant text or compaction state, so the trace
  carries only the hashed session, call and turn.
- **Turn end:** none.
- **Release check:** none.

Bash everywhere, Git Bash on Windows. Both load next session. Hermes asks once to approve the hook:
approve it, or start Hermes with `--accept-hooks`, `HERMES_ACCEPT_HOOKS=1` or
`hooks_auto_accept: true`. Its shell tool is in the `terminal` and `coding` toolsets.

### Known limits

When Hermes saves `config.yaml` it may drop jevlin's markers and fold the command. The hook works
either way, and uninstall removes an entry in either form that names this installation, with any
key left empty above it. Anything else, such as an entry you gave a `timeout:`, is left and
reported with its line numbers.

## Any other agent

### How it is found

It is not. `jevlin agents install` prints a rules block to paste when it finds no agent it knows.

### What is written and where

Nothing; you paste the block. It teaches the Bash form of the search command.

### What to know

- **Trace:** none: its searches carry a per-shell identity rather than an agent session.
- **Turn end:** none.
- **Release check:** none.
