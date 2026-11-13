# Agents

What `jevlin agents install`, and setup's agents question, writes into each coding agent. What each
one sends with a search is in [privacy.md](privacy.md#by-agent).

| agent | setup writes | hooks | the search runs without a prompt because | starts the release check | needs | Windows |
|---|---|---|---|---|---|---|
| [Claude Code](#claude-code) | a skill | five, in `~/.claude/settings.json` | nine allow rules, and the hook's `allow` | session start and stop | 2.1.196 for the turn end | Git Bash and PowerShell |
| [Cursor](#cursor) | a skill | eight, in `~/.cursor/hooks.json` | the hooks allow it | session start, stop and end | | the terminal profile's shell |
| [Codex](#codex) | a skill and a permission profile | five, in `~/.codex/hooks.json`, run once you approve them | the hook allows it (not on Windows, where no hooks are written: Codex asks) | session start and stop | 0.158.0 | the skill and writable directories; no hooks, no network |
| [opencode](#opencode) | a plugin, and a rules block to paste | the plugin | opencode's own permissions | plugin load, session start and idle | | the shell opencode resolves |
| [Pi](#pi) | a skill and an extension | the extension | Pi's own permissions | session start | 0.80.4 for the turn end | Git Bash |
| [Hermes](#hermes) | a skill | one, in `config.yaml`, approved once | Hermes' own permissions | no | | Git Bash |
| [any other](#any-other-agent) | nothing; you paste a block | none | the agent's own permissions | no | | Bash |

**What every agent gets.** Each command a skill teaches is written for the shell that agent runs on
your OS, with your paths filled in, and names the binary by its full path, so an agent searches
whether or not a bare `jevlin` is on PATH. An agent counts as present by the command that launches
it or, for Cursor, its config directory; `jevlin agents status` prints which. One not found is set
up with `jevlin agents install -client <id>`. How a file is written, and which entries are this
installation's, is in [reference, What jevlin writes into an agent's files](reference.md#what-jevlin-writes-into-an-agents-files).

## Claude Code

### How it is found

`claude` on PATH.

### What is written and where

A skill in `~/.claude/skills/jevlin/`. In `~/.claude/settings.json`: five hooks (`PreToolUse` on
the Bash and PowerShell tools threads each search into the turn; `SessionStart`, `PreCompact` and
`PostCompact` track the context window, and `SessionStart` also starts the daily release check;
`Stop` reports the turn end and starts the check) and nine `permissions.allow` rules. Six are the
single-quoted, quoted and bare spellings of the search command and of `fetch --stdin`, each ending
with the installation's `-config <file>`. Three name folders and no binary: an `Edit` and a `Read`
rule for the `requests` folder beside that config, and a `Read` rule for the skill's own folder, so
the agent opens the `reference.md` beside `SKILL.md` without a question. Reinstalling replaces the
rules rather than adding more.

### What to know

Bash on macOS and Linux. On Windows the model picks Git Bash or PowerShell per call, so the skill
teaches both. The hook prefixes the search with the trace, which an allow rule (matched on how a
command begins) no longer matches, so the hook answers `allow` itself for exactly the search the
skill renders, under the Bash and PowerShell tools alike; anything else waits for approval, and a
headless session refuses it. It does the same for the search read from a request file directly
inside the `requests` folder, and for exactly the fetch the skill renders.

A request that holds a `'` or a `$` cannot ride on the one-line call: the skill has the agent write
it to the `requests` folder with the Write tool, which Claude Code governs by `Edit` rules, and the
call reads it with `--stdin < <file>`, which Claude Code checks against `Read` rules. The Write tool
creates the folder.

### Known limits

The turn end needs Claude Code 2.1.196 or later; earlier versions give the hook no prompt id, so
with `turn_end` on they send no final answer. Searches are unaffected.

## Cursor

### How it is found

`cursor` or `cursor-agent` on PATH, or `~/.cursor`, since the editor's `cursor` command is optional.

### What is written and where

A skill in `~/.cursor/skills/jevlin/`; eight entries in `~/.cursor/hooks.json`, which the editor
and the Agent CLI both load.

### What to know

The hooks keep one lineage file per conversation, so two chats on one project do not relabel each
other's searches. `preToolUse` puts the conversation's identity in front of exactly the search the
skill renders and rewrites nothing else; the shell hook allows exactly that command, and exactly the
fetch the skill renders. The hook that runs when you submit a prompt keeps nothing unless `turn_end`
is on. The trace's text is the reply (`afterAgentResponse`) written earlier in the same turn as the
search; a search before any reply of its turn carries no text, never the previous turn's reply. The
model's reasoning, which `afterAgentThought` hands over, is neither kept nor sent, and that hook only
answers.

On Windows, commands run in the terminal `terminal.integrated.defaultProfile.windows` names, which
this client cannot read, so the skill carries both forms: "If your terminal is PowerShell" and "If
your terminal is Git Bash". Use the matching one; the PowerShell form run from Git Bash loses its
encoding line, and a non-ASCII query goes out mangled.

Cursor also runs Claude Code's hooks from `~/.claude/settings.json`, with its own payload. They
recognize the caller from the payload and exit at once, so a Cursor turn is handled once, and no
Cursor command is rewritten as Claude Code's.

### Known limits

**Windows, PowerShell terminal profile, non-ASCII queries.** A non-ASCII query has reached the
router double-encoded under the PowerShell profile, answering a different question; Git Bash has
not shown it. Use Git Bash, or ASCII queries. Cursor's hook wrapper re-encodes non-ASCII text, so
such a search runs without Cursor's label.

**Windows, an apostrophe in a PowerShell-form search.** Git Bash reads the PowerShell form's
here-string as an ordinary single-quoted string, which an apostrophe in the request closes. The
hooks cannot tell which terminal you picked, so they allow that form without asking only when its
request holds no `'`. The skill says to write an apostrophe as `'`, which the router receives
as the same query.

**Windows: the command-line agent started from Git Bash runs no hook, so no search.** Cursor runs
its PowerShell hook wrapper with bash `eval`, which cannot parse it. Start the agent from
PowerShell; the editor is unaffected.

## Codex

### How it is found

`codex` on PATH.

### What is written and where

A skill in `~/.codex/skills/jevlin/`; on macOS and Linux, five hooks in `~/.codex/hooks.json`
(`PreToolUse`, `SessionStart`, `PreCompact`, `PostCompact`, `Stop`), each written where any of
jevlin's stood before so no other hook moves; and a marked block in `~/.codex/config.toml`. On
macOS and Linux the block is a permission profile, made Codex's default; on Windows it is the
writable directories only ([Windows](#windows)).

### What to know

Bash on macOS and Linux, PowerShell on Windows. Supported Codex is 0.158.0 or newer: below 0.131
Codex refuses the whole file with "invalid type: map, expected a boolean" and does not start
(upgrade Codex, or remove the block between its two markers), and 0.131 to 0.157 has not been
tried.

Codex runs the hooks only after you approve them: start `codex`, or open the app, and approve them
when it asks you to review hooks. Until then they do nothing, and under `codex exec` nothing says
so; searches still run, without a session or a turn. jevlin never records an approval for you.
`jevlin agents status` says what Codex has on record: an approval for some or all of the hooks,
none, or that it cannot tell. A hook whose command changes, after you move the binary or the
config, may need approving again; an upgrade in place does not change the commands.

With the hooks approved, the search command the skill shows carries its session, turn and call,
and is allowed without a prompt, as is the fetch the skill shows. Anything else, including the
search after a `cd`, in a loop or piped on, runs as written and carries the per-shell identity.

#### The permission profile

The block is a Codex permission profile named `jevlin`, made the default with
`default_permissions`. It extends Codex's `:workspace` profile, or a profile of your own that you
agreed to switch from, with write access to the state and sessions directories, and network access
to the search hosts only: the router and the platform's agents API. A host on this machine's
loopback is never listed, because no command in Codex's sandbox can reach it and listing it would
open every local port. The block also turns on Codex's `network_proxy` feature, which is what
enforces the host list. It sits before the first table in the file, because `default_permissions`
must, and is added and taken out without a byte of its own, so uninstall gives your file back
exactly as it was. If you change `[router] url` or `[agents] url`, run `jevlin agents install`
again; `jevlin agents status` says when the profile names other hosts than the config does.

Codex writes into its own config file, and some of that can land between jevlin's markers. Install
and uninstall change only jevlin's own lines, move a setting Codex wrote inside the markers to just
above them and a table to just below them, and name each in the plan. A block that has lost a
marker (`codex features disable network_proxy` deletes the comments above the table it removes) is
still recognized, repaired by the next install and removed whole by uninstall. A change Codex's own
parser would refuse, or a block that is not valid TOML or holds lines jevlin did not write, is left
and reported, and nothing is written for Codex. Another installation's block is left to that
installation, and named. An earlier version's block, which opened the network to every host, is
replaced by the profile on the next install.

#### The Switch? question

A setting of your own that the profile would change is asked about first, in one question that
shows every line it would write, and `-yes` does not answer it: a profile of yours named in
`default_permissions`, `default_permissions = ":workspace"`, a bare
`sandbox_mode = "workspace-write"` (commented out), or `network_proxy = false` in your `[features]`
table (set to true). jevlin never writes a line inside a table of yours; it changes only those
lines, each marked with a comment naming its config, and `agents uninstall` puts each back with
jevlin's block, unless you have changed it since.

With a profile of yours, jevlin's extends it and Codex merges the two. Once jevlin's turns the
network on, every host your profile or the profiles it extends allows is reachable too, each host
decided by the nearest profile that names it, and a wildcard deny (any key holding `*` or `?`)
winning over any exact allow. The question names every such host; install refuses for Codex when
the router's host is denied. If your profile's network is already open with the proxy off,
jevlin's adds only its roots.

Answering no installs nothing for Codex and exits 0. No answer stops the install, exit 2, writing
nothing but one line: if jevlin's own block still holds an open network from an earlier version,
that is closed whatever happens, and the message says so. Without a terminal, `agents install`
prints the exact lines to change by hand and the block to add, and exits 2; a file finished that
way is jevlin's. `setup` reports the agent it could not set up and exits 0. A read-only sandbox, or
a `[sandbox_workspace_write]` table of your own, gets nothing for Codex and the profile to adopt
printed; with `danger-full-access` only the skill and hooks are written.

#### Network and sandbox

Without the block, a search returns results but cannot write its lineage file or its turn mark, and
the claim resume it starts cannot write, so the claim is never picked up. The config and key are
never writable from the sandbox, so a command gone wrong cannot change where your credentials go.
Sandboxed commands can read `credentials.json` and the state directory, and can leave files in the
directories the block makes writable; jevlin's hooks, which run outside the sandbox, never write
through a link left there and never wait on a pipe left at a name they read. If your config puts
`jevlin.toml`, or the folder that holds `credentials.json`, inside one of those directories, the
install widens nothing and says why.

Inside the sandbox Codex routes commands through a proxy on this machine; a process a search
starts contacts only the hosts the profile lists, because Codex fails whichever command is running
when anything inside the sandbox is refused a host. `codex features disable network_proxy` deletes
the profile's `[features.network_proxy]` table, and the profile then lets every command reach any
host; `jevlin agents status` says so, and `jevlin agents install` puts the table back. Status reads
only `~/.codex/config.toml`: a trusted project's own `.codex/config.toml`, or `codex -p <name>`,
that sets `network_proxy = false` opens the network there while jevlin's profile is active, and
status cannot see it.

#### Windows

No permission profile has been seen to work in Codex's Windows sandbox, so none is written: the
block makes the same directories writable and opens no network, and a search needs Codex's
approval to reach the router. No hooks are written either, because what runs a Codex hook there has
not been seen. A sandbox's setup may add its own group to the state directory's access list;
`doctor`'s `state access` line names it, and running `jevlin setup` again gives `<home>\state` an
owner-only list until the sandbox's setup adds it again.

### Known limits

A Codex turn end gives a shell command no duration: Codex's session file holds none that is the
command's. A client of Codex's app server that starts a thread with an explicit sandbox mode
discards the profile, and the network is then off for that thread. On Linux, Codex runs each
command in its own process namespace, so a claim resume a search starts ends with the search; a
search from another agent or your shell, or `jevlin connect`, picks the claim up instead.

## opencode

### How it is found

`opencode` on PATH.

### What is written and where

An in-process plugin, `~/.config/opencode/plugins/jevlin.js`, that threads each search into the
trace and starts the daily release check when the plugin loads, when a top-level session is created
and at each of its idles. There is no skill directory: install prints a rules block to paste into
`AGENTS.md`, which is what teaches the agent the search.

### What to know

It runs Bash on macOS and Linux. On Windows it resolves its shell when it starts: `$SHELL` wins
whenever it is set (a terminal like Git Bash sets it), and only without one does it take the first
of pwsh, PowerShell, Git Bash and cmd it finds. The plugin follows the same signal per call, so a
search carries its bridge in the syntax of the shell that will run it. A subagent runs as a child
session, and its searches name the session that started it.

### Known limits

`/jevlin off` and `agents prefer` do not reach it: its `AGENTS.md` block carries no preference.

## Pi

### How it is found

`pi` on PATH.

### What is written and where

A skill in `~/.pi/agent/skills/jevlin/`; an auto-discovered extension,
`~/.pi/agent/extensions/jevlin.ts`, that threads each search into the trace and starts the daily
release check when a session starts.

### What to know

It runs Bash everywhere, Git Bash on Windows. The extension reads the session Pi saved, so a
resumed session keeps its place. A turn is named by the id Pi saved for the message you wrote: two
searches under one prompt share it, and a fork is a session of its own. Pi can run several loops
for one prompt, retrying a failed request or continuing after it compacts; the turn end waits for
`agent_settled`, which comes once, after the last of them.

### Known limits

The turn end needs Pi 0.80.4 or later, the first to report a settled run. Before Pi 1.1.0, which
reports whether a run was aborted, a turn is reported as interrupted only when its last answer says
so. A run an extension starts with a message of its own belongs to your last message: its searches
share that turn, which has by then been reported, and are joined to no turn end.

## Hermes

### How it is found

`hermes` on PATH.

### What is written and where

A skill in `<HERMES_HOME>/skills/jevlin/` (default `~/.hermes`, on Windows `%LOCALAPPDATA%\hermes`);
a `pre_tool_call` hook in a `hooks:` block of its `config.yaml`. If you already have a `hooks:`
section, install prints the lines to paste instead of editing the file, beginning with `hooks:`, and
then counts them as set up.

### What to know

Bash everywhere, Git Bash on Windows. Both load next session. Hermes asks once to approve the hook:
approve it, or start Hermes with `--accept-hooks`, `HERMES_ACCEPT_HOOKS=1` or
`hooks_auto_accept: true`. Its shell tool is in the `terminal` and `coding` toolsets.

### Known limits

When Hermes saves `config.yaml` it may drop jevlin's markers and fold the command. The hook works
either way, and uninstall removes an entry in either form that names this installation. Anything
else, such as an entry you gave a `timeout:`, is left and reported with its line numbers.

## Any other agent

### How it is found

It is not. `jevlin agents install` prints a rules block to paste when it finds no agent it knows.

### What is written and where

Nothing; you paste the block. It teaches the Bash form of the search command.

### What to know

Its searches carry a per-shell identity rather than an agent session, and start no release check.
