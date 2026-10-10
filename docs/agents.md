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

**The turn end needs Claude Code 2.1.196 or later.** Earlier versions give the hook no prompt id,
so with `turn_end` on they send no final answer; searches are unaffected.

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

A skill in `~/.cursor/skills/jevlin/`; eight entries in `~/.cursor/hooks.json`, which the editor
and the Agent CLI both load.

### What to know

The hooks keep one lineage file per conversation, so two chats on one project do not relabel
each other's searches. `preToolUse` puts the conversation's identity in front of exactly the
search the skill renders and rewrites nothing else; the shell hook allows exactly that command.

One of the hooks runs when you submit a prompt. It keeps nothing unless you turned `turn_end` on;
then it holds what you asked, scrubbed, until that turn ends. With `turn_end` on, a turn that
searched is reported with your prompt, its searches, the reply and the model. Cursor's hooks carry
no text written between tool calls, and jevlin installs no hook behind every tool, so its other
tools are not listed.

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

**Windows, an apostrophe in a PowerShell-form search.** Git Bash reads the PowerShell form's
here-string as an ordinary single-quoted string, which an apostrophe in the request closes, and
an interactive terminal then runs the rest of that line. The hooks cannot tell which terminal you
picked, so they allow that form without asking only when its request holds no `'`. One that holds
it waits for your approval and reaches the router without Cursor's label. The skill says to write
an apostrophe as `\u0027`, which the router receives as the same query.

**Windows: the command-line agent started from Git Bash cannot run any hook, so no search.**
Cursor runs its PowerShell hook wrapper with bash `eval`, which cannot parse it. Start the agent
from PowerShell instead; the editor is unaffected. Reported to Cursor (forum thread 172789).

## Codex

### How it is found

`codex` on PATH.

### What is written and where

A skill in `~/.codex/skills/jevlin/`; on macOS and Linux, five hooks in `~/.codex/hooks.json`
(`PreToolUse`, `SessionStart`, `PreCompact`, `PostCompact`, `Stop`), each written where any of
jevlin's stood before so no other hook moves; and a marked block in `~/.codex/config.toml`, which
install creates if it is not there.

On macOS and Linux the block is a Codex permission profile named `jevlin`, made the default with
`default_permissions`. It extends Codex's `:workspace` profile, or a profile of your own that you
agreed to switch from, with write access to the state directory, plus the intake, sessions and
spool directories when `[miner] enabled` is set, and network access to the search hosts only:
the router, the authorization server when `as_url` is set, and the platform's agents API. A host
on this machine's loopback is never listed, because no command in Codex's sandbox can reach it
and listing it would open every local port; the plan says when a service is on loopback. The
block also turns on Codex's `network_proxy` feature, which is what enforces the host list: with
it on, a host neither profile lists is closed to the commands Codex runs from this config. The
block sits before the first table in the file, because `default_permissions` must, and is added
and taken out without a byte of its own, so uninstall gives your file back exactly as it was.

On Windows no permission profile has been seen to work in Codex's sandbox, so none is written.
The block makes the same directories writable and opens no network: a search there needs Codex's
approval to reach the router.

### What to know

Bash on macOS and Linux, PowerShell on Windows. Without the block, a search returns results but
cannot record, so it earns nothing and the claim is never picked up. The config, key and wallet
are never writable, so a command gone wrong cannot change where your credentials go. Sandboxed
commands can read `credentials.json` and the state directory; on Windows, not the wallet.
They can also leave files in the directories the block makes writable. jevlin's hooks and
flushes, which run outside the sandbox, never write through a link left there and never wait on
a pipe left at a name they read, so a sandboxed command cannot turn them into a write to a file
of yours elsewhere, or stall a search. If your config puts `jevlin.toml`, or the folder that
holds `credentials.json`, inside one of those directories, the install widens nothing and says
why: give each directory its own place, as setup does. On Windows a sandbox's setup may
add its own group to the state directory's access list; `doctor`'s `state access` line names
whoever besides you is on it, and does not call that a problem. Running `jevlin setup` again
gives `<home>\state` an owner-only list, which removes that entry until the sandbox's setup
adds it again.

Supported Codex is 0.158.0 or newer. Older versions are not supported: below 0.131 Codex refuses
the whole file with "invalid type: map, expected a boolean" and does not start (upgrade Codex, or
remove the block between its two markers), and 0.131 to 0.157 has not been tried. Inside the sandbox Codex routes commands through a proxy on this machine, and jevlin's
flush and claim resume use it to reach the authorization server; jevlin never uses a proxy that
is not on this machine for that. A process a search starts contacts only the hosts the profile
lists, because Codex fails whichever command is running when anything inside the sandbox is
refused a host. If you change `router_url`, `as_url` or the platform in your config, run
`jevlin agents install` again; `jevlin agents status` says when the profile names other hosts
than the config does.

`codex features disable network_proxy` deletes the profile's `[features.network_proxy]` table,
and the profile then lets every command Codex runs reach any host. `jevlin agents status` says so
in those words, and `jevlin agents install` puts the table back. Status reads only
`~/.codex/config.toml`: a trusted project's own `.codex/config.toml`, or `codex -p <name>` with
`<name>.config.toml`, that sets `network_proxy = false` turns the proxy off for that project or
profile while jevlin's profile is active, which opens the network there, and status cannot see it.
Don't turn the proxy off in a project config while jevlin's profile is your default.

Any change to a file that Codex's own parser would refuse is not written: a `features` or
`permissions` table you wrote inline or with dotted keys, where jevlin's table header would collide
with it, is named in the plan, and nothing is written for Codex.

A setting of your own that the profile would change is asked about first, in one question that
shows every line it would write, and `-yes` does not answer it: a profile of yours named in
`default_permissions` (jevlin's profile then extends yours, and Codex merges the two, so your
hosts and roots still apply; once jevlin's profile turns the network on, every host your profile
or the profiles it extends allows is reachable too, even where your own network was off, and the
question names them all, or says every host where one allows `"*"`; if your profile's network is
already open with the proxy off, jevlin's adds only its roots and leaves your network as it is), `default_permissions =
":workspace"`, a bare `sandbox_mode = "workspace-write"` (commented out), and `network_proxy =
false` in your `[features]` table (set to true). jevlin never writes a line inside a table of
yours: it changes only those lines, each marked with a comment naming its config, and `agents
uninstall` puts each back together with jevlin's block, unless you have changed it since, in
which case it is left as you have it. Answering no installs nothing for Codex and exits 0. No
answer stops the install, exit 2, with nothing written except one thing: if jevlin's own block
still holds an open network from an earlier version, that one line is closed, whatever else
happens — on a no at `Proceed?` or at setup's agents question too — and the message says so. Without a terminal, `agents install` prints the exact lines to
change by hand and the block to add with its markers, every line flush left so it can be copied as
printed; where the file already has a block of jevlin's, it says to replace that block, or prints
it whole to delete when it is the earlier version's or holds lines of yours, which it says where
to put back. It exits 2; a file finished that way is
jevlin's, and a later install or uninstall treats it as installed; `setup` reports the agent it could not set up
and exits 0, because the rest of setup succeeded. A dry run prints the question and plans the
yes. Whatever the answer, a block of jevlin's own that opens the network is closed: an earlier
version's block loses its `network_access = true` line and keeps every other line, and jevlin's
profile with its own network and the proxy off loses its network. A read-only sandbox, or a
`[sandbox_workspace_write]` table of your own, gets nothing for Codex and the profile to adopt
printed; widening those is yours to do. With `danger-full-access` there is nothing to widen, and
only the skill and hooks are written. If the profile in the file is another jevlin installation's,
nothing is written for Codex and the plan names it.

Codex runs the hooks only after you approve them: start `codex`, or open the app, and approve
them when it asks you to review hooks. Until then they do nothing, and under `codex exec` nothing
says so; searches still run and record, without a session or a turn. jevlin never records an
approval for you. `jevlin agents status` says what Codex has on record, read from its
`config.toml`: an approval for some or all of the hooks, none, or that it cannot tell. An
approval on record may be for an earlier version of a command, and whether it still counts is
Codex's to decide; a Codex too old to run hooks reads as "no approval on record" too, since no
version is checked. A hook whose command changes, after you move the binary or the config, may
need approving again; an upgrade in place does not change the commands.

With the hooks approved, a search Codex runs carries its session, turn and call, and a
subagent's search names the session that started it. Only the search command the skill shows is
given them, and allowed without a prompt; anything else, including the search after a `cd`, in a
loop or piped on, runs as written and carries the per-shell identity. A Codex search carries no
assistant text.

With `[miner] turn_end = true`, Codex's `Stop` hook also reports a turn in which a jevlin search was
served (see [The turn end](reference.md#the-turn-end)). It reads the turn from Codex's own session
file: what you asked, what the assistant wrote along the way, its final answer, and each tool call by
name and outcome, never a tool's input or output. A subagent's turn sends nothing of its own. Without
`turn_end`, the hooks read no prompt, no tool's output and none of the assistant's words.

Codex writes into its own config file: tables at the end, a `[features]` table from `codex features
enable`, settings from the desktop app. Some of that can land between jevlin's markers. Install
and uninstall change only jevlin's own lines, move a setting Codex wrote inside the markers to
just above them and a table to just below them, and name each in the plan. A block that is not
valid TOML, or whose profile holds anything jevlin did not write, is left and reported. Markers
count only as lines of their own outside a multi-line string, so a comment or a string that
happens to hold them is yours. jevlin writes its `[features.network_proxy]` table last in its
block, because `codex features disable network_proxy` deletes the comments above the table it
removes; a block that has lost a marker all the same is still recognized, repaired by the next
install and removed whole by uninstall. A file that starts with a UTF-8 byte-order mark keeps it. An earlier version's block, which opened the network to every
host, is replaced by the profile on the next install, and the plan says so.

### Known limits

On Windows no hooks are written, because what runs a Codex hook there has not been seen, and no
profile, for the same reason. A Codex turn end gives a shell command no duration: the session
file Codex 0.160.0 writes holds none that is the command's. A client of Codex's app server that
starts a thread with an explicit sandbox mode discards the profile, and the network is then off
for that thread: the skill cannot help there. On Linux, Codex runs each command in its own
process namespace, so a flush or claim resume a search starts ends with the search; the hooks,
which run outside the sandbox, flush instead.

## opencode

### How it is found

`opencode` on PATH.

### What is written and where

An in-process plugin, `~/.config/opencode/plugins/jevlin.js`, that threads each search into the
trace. There is no skill directory: install prints a rules block to paste into `AGENTS.md`.

### What to know

It runs Bash on macOS and Linux, and PowerShell on Windows.

A subagent runs as a child session, and its searches name the session that started it. The
plugin learns that from the session's creation, or asks opencode once.

Each search now carries the turn it was made in. With `turn_end` on, when a turn that searched
goes idle the plugin pipes that turn to jevlin: what you asked, what the assistant wrote, each
tool by name, time and outcome, and the model. A tool's input and output are never read into it.

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
