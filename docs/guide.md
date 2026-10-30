# Jevlin guide

jevlin in the order you meet it. Per-agent detail: [agents.md](agents.md); tables: [reference.md](reference.md).

## What you need

| | |
|---|---|
| **A moment to claim your agent** | Setup prints a link; open it in a browser, sign in at `platform.jevlin.ai` (or create an account) and claim the agent. Search works before you do; the claim is what ties the agent to your account. |

## Setup

Install globally, as the [README](../README.md#install) shows, then run `jevlin setup`. Setup
refuses to run from an `npx` cache or a project's `node_modules`, whose paths would disappear.

Setup also refuses a binary someone else could replace, because your agents' hooks and skills
and your PATH will name it: one under the temporary directory, or one where the file or a
directory above it, on the path you ran or the file it links to, is owned by another user or
writable by every user (the sticky bit is no exception). `agents install` and `agents prefer`
refuse the same, since they write the same path. A directory its group can write is accepted
when that group is your own or one whose members can already become root (`admin`, `wheel`,
`sudo`, `root`), which is how Homebrew's prefix is set up. For any other group, such as
Debian's `staff` on `/usr/local`, setup prints a warning naming the directory and goes on:
whether anyone else is in that group is something only you can say. On Windows only the
temporary-directory rule applies; who else can write a folder is not checked there yet. To
fix a refusal, move the binary somewhere only you can write, such as `~/.local/bin`, and run
it from there.

| it asks | what your answer does |
|---|---|
| **Use it? [Y/n]** | Only when a previous installation is set aside beside `~/.jevlin`. Setup first says where it is and what it holds. Yes brings back your registration, key and claim link, and skips the registration they answer. |
| **Add them to ~/.zshrc? [Y/n]** | Or `~/.bashrc`, whichever your shell reads. One marked block puts the binary on PATH and sets `JEVLIN_CONFIG`. No just means longer commands. On Windows it asks **Set them for your user?** and sets PATH and `JEVLIN_CONFIG`. |
| **Set up the coding agents found on this machine now? [Y/n]** | Writes a skill and, where the agent supports them, hooks into its config. You see the plan first, with what made each agent count as present. |
| **Switch? [y/N]** | Asked only when Codex's config has a setting of your own that its permission profile would change, such as your own profile in `default_permissions`. It shows every line it would write. A bare Enter is no, which sets up the other agents and leaves Codex as it is. See [Codex](agents.md#codex). |

Before any question, setup writes `~/.jevlin/jevlin.toml` in configuration format 1: a `schema = 1`
line, `[router]`, `[platform]` and `[agents]` with their URLs, and `[search]` with the state and
sessions directories ([reference, Config](reference.md#config) has every key). A config already
there in that format is left byte for byte; one from an older jevlin, with no `schema` line, is set
aside as `jevlin.toml.old` and a fresh file written, your registration and key kept (see
[Moving from 0.6.x](#moving-from-06x)); one in format 1 with a mistake in it stops setup with the
error, and nothing is written.

Only an answer you typed counts. Interrupt a question, or close its input, and setup stops there
with a non-zero exit, recording and writing nothing. The same holds for `agents install`'s
**Proceed?**, Codex's **Switch?** and uninstall's confirmations: a typed no exits 0, no answer
exits non-zero.

| flag | effect |
|---|---|
| `-yes` | Answers yes to the profile and agents questions, terminal or not. Answers **Use it?** only at a terminal. Never answers Codex's **Switch?**. Without `-yes` or a terminal, setup leaves the profile and agents alone and prints the command for each. An agent that could not be set up is reported, and setup still exits 0. |
| `-dry-run` | Lists every file it would write or move, and changes nothing. |
| `-no-profile` | Leaves the shell profile (Windows: user environment) alone, even with `-yes`. |
| `-no-agents` | Skips agent detection, even with `-yes`. `-with` still sets up what it names. |
| `-with <id>` | Sets up `claude`, `codex`, `cursor`, `opencode`, `pi` or `hermes`, found or not. Repeatable. |

## The claim link

Setup prints a claim link once the config is written, before the shell profile, and waits up to
three minutes for you to visit it. You need not do it then: search already works, and using an
agent later resumes the claim on its own. The link
is kept in `~/.jevlin/claim.json`, beside your stored key, and only for the agent it was issued
to; it is never read from the state directory, which an agent's sandbox may write. Before
`jevlin connect` prints it, it asks the platform which agent your key belongs to: a claimed agent
shows no link, and if the platform cannot answer, none is shown. An interrupted registration is
finished on the next run, never registered twice. A lost or damaged local record is rebuilt from
the platform (one holding characters that would act on your terminal counts as damaged, and
`status` says it cannot be read and to run `jevlin connect`), and so is one that names an agent
your key does not belong to, which is what an agent's sandbox can leave in the state directory,
or an older version interrupted while it replaced an expired registration. If the platform no
longer recognizes the key, `connect` stops, and `jevlin connect -force` replaces the credential:
run it only on purpose; it never rebuilds a record. An unclaimed registration that expires is
replaced by the next `jevlin connect` you run, changing only the platform agent and its key; a
background resume never replaces one. A lost claim link is replaced too, including one an older
version kept in the state directory: for a registration that is still unclaimed, a foreground
`jevlin connect` asks the platform for a fresh link and prints it — the old code is dead the
moment a new one is minted, which is why only a connect you run does this, never a background
resume. Where the platform cannot mint one, `connect` says so; run it again later, or wait for
the registration to expire and connect again.

`jevlin connect` also re-runs onboarding by hand. Your claim on the platform is undone only at
its console; nothing this client does revokes it.

## Then check

```bash
jevlin status          # what this installation has and has not completed
jevlin agents status   # which agents are set up, and how each was found
jevlin doctor          # one line per check
jevlin search -format model "what is proof of authority consensus"
```

Restart any agent that was already open. Your agent sends JSON on stdin instead:

```bash
jevlin search --stdin <<'JSON'
{"version":1,"query":"what is proof of authority consensus"}
JSON
```

Each skill carries this in its agent's shell, with your paths filled in. The
[reference](reference.md#the---stdin-request) describes the envelope and the PowerShell form.

## Running setup again

Nothing needs removing first: update the binary, then run `jevlin setup`. It keeps your
identity and key, and lets `connect` finish or repair its own onboarding. It leaves a format-1
config as it is and sets an older one aside as `jevlin.toml.old`. It updates its
profile block and agent files in place, reporting one already correct as "nothing to write:
already set up". For an installation elsewhere, set `JEVLIN_HOME` to it; setup has no other way
to find it. `setup -home <dir>` alone makes a separate installation and leaves your profile and
agents alone, even with `-yes`; it prints the `jevlin agents install -config` command for it,
and an agent whose skill came from your main installation keeps it. For a clean start, move
`~/.jevlin` aside.

## Making it the default, or not

The skill tells your agent to prefer this search over its built-in one. In an agent that got a
skill, `/jevlin off` makes the agent's own search the default, `/jevlin on` restores this one,
and `/jevlin status` says which is in force; from a shell, `jevlin agents prefer off|on|status`.
The choice is saved beside your config and survives reinstalls. While it is off, asking to
"search through jevlin" still routes that one search here.

## Choosing a tier

Every search carries a tier, which decides which engines run and what one query may cost: `fast` is three fast indexes, one per
index group, quicker than the others, about $0.015 at the arms' own prices; `balanced` is the
core arms with 8 results each and a 12-page merged window, about $0.035; `max` is the same arms
asked for 15 results each, a 24-page window and 2,000-character excerpts, about $0.045 plus the
tokens your agent spends reading it. Nothing is marked up; `usage.cost_micros` is the number.
`/jevlin tier
balanced` in an agent, or `jevlin agents tier balanced` from a shell, sets the tier every search
carries unless the request names one; `/jevlin tier default` unsets it, and `/jevlin tier` says
what is in force. The skill tells your agent to leave `tier` alone so your tier applies, and to
ask for `balanced` only when you need several sources beyond the three fast indexes, a thin
result or indexes that disagree; it never sends `max` on its own. With no
tier set, the request carries none and the router applies the default for your key.

## What travels with a search

Each search carries a small `trace` so the router can group one task's searches: hashed session
and turn ids (your agent's real ids never leave the machine), a call counter, and the assistant
text just before the search, capped at 32 KB. That text is conversation content leaving your
machine, and it goes only to the search router, inside the search request. `export
JEVLIN_TRACE=off` sends no trace; searches work the same without it.

The hooks keep lineage files in `~/.jevlin/sessions`, one per workspace (Cursor: per
conversation). Only the agent that wrote a file reads it, so a second agent in a subdirectory
never sends the first one's narration. An agent started by another as a shell command carries
the outer session and label: its searches are work the outer session asked for.

`jevlin search "<query>"` puts the query in the command line, visible in `ps` and your shell
history; `search --stdin`, which every skill teaches, keeps it out. The key is in neither.

## Upgrading

Once a day, when an agent session starts or ends, jevlin checks the project's GitHub releases
for a newer one, and a native install replaces its own binary with it, the same careful way
`jevlin upgrade` does: signature and checksum verified, the new binary run before and after,
the old one kept as `jevlin.previous`, nothing else touched. To be told instead, add to your
config:

```toml
[update]
mode = "notify"   # or "off" to stop the check; the default is "apply"
```

Under `notify`, `jevlin status` and `jevlin doctor` say a release is available and give the
command that installs it. The default installs a release nobody on this machine has looked at,
on the strength of the project's release signature, checked against a key built into the copy
you already have; choose `notify` where you would rather pick the moment yourself. An npm
install is never replaced this way; the check only notes that a release exists, and
`jevlin status` and `jevlin doctor` tell you to run npm's command.

`jevlin status` says when it last checked and what it found; `jevlin doctor`'s `up to date`
line says the same, and says so when the check has not run for three days, which usually means
no agent with the hooks has been opened, or that your agents' hooks reach the network through a
proxy on this machine, where no check is started (Codex's sandbox does that, and a check from
inside it would break the Codex command running). Restart open agents after an upgrade.

If a release misbehaves, `jevlin upgrade -rollback` puts the previous binary back, and the
daily check then leaves the release you rolled back from alone; it installs the next newer one,
and `jevlin upgrade` installs any release you ask for. If an automatic upgrade ever fails so
badly that the old binary could not be put back, `jevlin status` and `jevlin doctor` say so and
list the copies left beside the binary, none of them deleted; reinstall from the releases page
or put one back by hand, and no automatic check runs until `jevlin upgrade` succeeds.

```bash
jevlin upgrade                  # the latest release
jevlin upgrade -version X.Y.Z   # exactly that release, never an older one
jevlin upgrade -rollback        # put back the binary the last upgrade replaced
npm install -g @jevlinsearch/jevlin@latest    # an npm install is updated with npm
```

`upgrade` downloads only from the project's GitHub releases, checks that `checksums.txt` is
signed by the project's release key and that the download matches it, and runs the new binary
before and after installing it. A release without that signature is refused. The release that
first carries the check is installed by your current copy, which checks the checksum only; every
upgrade after it checks the signature too. It keeps the old one as `jevlin.previous`;
`-rollback` swaps back with no network. It refuses an npm copy and a development build, takes at
most three minutes, and never touches your registration or config. The new binary then
rewrites the skills and hooks this installation set up, and for Codex the block in
`~/.codex/config.toml`: an earlier version's block, which opened Codex's network to every host,
becomes the permission profile without a question, and a setting of your own that the profile
would change is left and printed for you to change by hand, since nobody is at a terminal to ask.
The profile is supported with Codex 0.158.0 or newer. If that step fails, the upgrade still
succeeded, and the message gives the command that finishes it. Restart open agents.

When it fails, the first word after `upgrade:` says what to do:

| word | meaning |
|---|---|
| `retry` | Try again later; nothing changed. A slow first run, such as a virus scan, ends here. |
| `release_invalid` | The release itself is wrong. Do not retry blindly. |
| `ownership` | This copy is not one `upgrade` may replace, such as an npm install. |
| `filesystem` | Fix the permissions or the file it names. |
| `lifecycle_busy` | Another setup or upgrade is running. |
| `refused` | An older version was asked for. |
| `manual_intervention` | Putting the old binary back failed too. Every surviving copy is listed; none is deleted. |

On Windows, `previous_in_use` means an older Jevlin or agent process still runs
`jevlin.exe.previous`. Your binary was put back; close that process and run `upgrade` again.

### Moving from 0.6.x

This version reads only configuration format 1, and a 0.6.x installation's `jevlin.toml` is not
that. On a native install the daily check replaces the binary with this version as it would any
release; from then on every command that reads the config (`search`, `fetch`, `limits`, `status`,
`doctor`, `connect`) stops with "this configuration is from an older jevlin; run `jevlin setup`",
while the hooks, which fail open, run the agent's tool call without a lineage file, and the
check itself goes no further until setup has run. On an npm install, run
`npm install -g @jevlinsearch/jevlin@latest`, then `jevlin setup`.

`jevlin setup` sets the old file aside as `jevlin.toml.old` (`.old.1`, `.old.2`, ... if the name
is taken), writes `jevlin.toml` in format 1, keeps your registration and search key, and rewrites
the hooks and skills of the agents it finds in place. The new file names the state and sessions
directories, the router, the portal and the agents API the old file named, because your
registration and key live beside that state directory and the key is bound to those hosts; where
the old file named none, it names the defaults, and `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL` and
`JEVLIN_AGENTS_API_URL`, when set, choose over both. Nothing else is migrated: a setting you
changed by hand, such as `turn_end` or `tier`, you copy into the new file yourself, under its
format-1 key ([reference, Config](reference.md#config)). Files only the earlier version used are left untouched where they
are, never read and never named; this version has no use for them, and you may delete them.

## Removing it and coming back

```bash
jevlin uninstall -dry-run       # what would be removed; changes nothing
jevlin uninstall                # skills, hooks, plugins, and the profile block
jevlin uninstall -binary        # ...and this installation's binary
jevlin uninstall -purge-state   # ...and the identity, key and config
npm uninstall -g @jevlinsearch/jevlin         # an npm install is npm's to remove
```

A plain `uninstall` removes what setup wrote for this installation (`-home`, default
`~/.jevlin`). On Windows it reverts PATH and `JEVLIN_CONFIG` only where they still hold setup's
values. Another installation's files are left and reported, and so is a hook of yours: one you
added to the group jevlin's hook sits in (Claude Code's `/hooks` puts a new hook there when the
matcher is the same) stays, and only jevlin's handler is taken out, by `uninstall` and by every
`agents install` or upgrade that rewrites it. Your registration, key and config stay, and
nothing is revoked. `-binary` also removes the binary, its
`.previous` and an interrupted upgrade's leftovers, never an npm copy; on Windows it moves the
running binary aside and names the file to delete later. If a coding agent's file could not be
changed, `-binary` keeps the binary, because the hooks still in that file run it: the run says
so and exits non-zero, and running it again once the file can be written finishes both. `uninstall` lists leftover `.lock`
files, which are safe to delete.

`-purge-state` destroys the participant state. At a terminal, you type the installation path;
`-yes` cannot answer it. Nothing is revoked: your claim on the platform is undone only at its
console. Folders your config names outside the installation are left and listed. Anything else
still in the installation directory afterwards — files an earlier version or another program
put there, which this version neither reads nor removes — is listed by full path, as it is in
the dry run, so you can delete it yourself. Close agent
sessions first, or a search may recreate folders. A run stopped at its confirmation leaves the
installation exactly as it was.

To come back, run `jevlin setup`, with `-home <dir>` for a non-default installation: the same
agent, no new registration. A bare `jevlin connect` would register anew, because the profile
block that named this installation is gone.

You can instead set `~/.jevlin` aside as `~/.jevlin.bak-<date>` (any `~/.jevlin.<name>` or
`~/.jevlin-<name>`). Setup lists what a set-aside copy holds, newest first, and moves it back
only when you say yes at a terminal, in pieces that belong together:

| piece | what setup does |
|---|---|
| **Registration and key** | Move as one. If `~/.jevlin` has its own, setup stops before anything moves and names both: keep one, move the other away. A half-made state directory there is renamed `state.unregistered-<time>`. |
| **Session files** | Merge one file at a time, never overwriting. |
| **Config** | Moves only if `~/.jevlin` has none. |

Symlinks and anything another account owns are not moved: a set-aside folder that is not yours
is never offered. The set-aside folder is removed only once it is empty.

The profile lines are one block between `# >>> jevlin >>>` and `# <<< jevlin <<<`; delete it to
undo them. If the block no longer has exactly one start and one end line, setup leaves the file
alone and prints the lines to add by hand.
