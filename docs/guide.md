# Jevlin guide

jevlin in the order you meet it, the README's questions answered in full. Per-agent detail is in
[agents.md](agents.md); commands, fields and files are in [reference.md](reference.md).

## Install

Install globally, as the [README](../README.md#how-do-i-install-it) shows, then run `jevlin setup`:

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Setup refuses to run from an `npx` cache or a project's `node_modules`, whose paths would
disappear, and from a location someone else could replace the binary in
([A binary setup refuses](#a-binary-setup-refuses)). If your shell then says
`jevlin: command not found`, see [jevlin: command not found](#jevlin-command-not-found).

### Installing without npm

`curl -fsSL https://jevlin.ai/install.sh | sh` (macOS, Linux and WSL) or
`irm https://jevlin.ai/install.ps1 | iex` (Windows PowerShell; `install.cmd` runs it from CMD)
downloads the latest release for your machine and checks it against the release's
`checksums.txt`, which is mandatory. It also checks that file's signature against the project's
release key when a tool on the machine can: `install.sh` uses openssl, node or python3, and
`install.ps1` uses node. A signature that fails stops the install; with no such tool it says so
and relies on the checksum. It puts the binary at `~/.jevlin/bin/jevlin` (`$JEVLIN_HOME/bin`
when that is set) and runs `jevlin setup` when a terminal is attached; otherwise it prints the
path to run setup with.

| variable | effect |
|---|---|
| `JEVLIN_VERSION=0.7.0` | Installs exactly that release instead of the latest. |
| `JEVLIN_HOME=/path` | The installation directory, instead of `~/.jevlin`. |
| `JEVLIN_NO_SETUP=1` | Installs the binary and stops. |

In PowerShell, set them first as `$env:JEVLIN_VERSION` and so on.

By hand: download the archive for your OS and architecture from the
[releases page](https://github.com/jevlinai/jevlin-go/releases), check it against that release's
`checksums.txt`, put `jevlin` in a directory only you can write, such as `~/.local/bin`, and run
`jevlin setup` from there.

### From source

`go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`, or `make build` in a clone, with
Go 1.27 or newer. A source build reports its version as `dev`, so the daily check never replaces
it and `jevlin upgrade` refuses it; install a release for updates.
[Building from source](reference.md#building-from-source) has the rest.

## Setup

Setup registers this installation with the search platform and stores the key it gets back, so
there is nothing to copy or paste. In order, it offers to bring back a set-aside installation,
writes `~/.jevlin/jevlin.toml` ([The config it writes](#the-config-it-writes)), registers and
prints the claim steps ([The claim link](#the-claim-link)), and then asks about your shell profile
and your coding agents:

| it asks | what your answer does |
|---|---|
| **Use it? [Y/n]** | Only when a previous installation is set aside beside `~/.jevlin`. Setup first says where it is and what it holds. Yes brings back your registration, key and claim link, and skips the registration they answer ([Set-aside installations](#set-aside-installations)). |
| **Add them to ~/.zshrc? [Y/n]** | Writes one marked block to the file your shell reads (below) that puts the binary's directory on PATH and sets `JEVLIN_CONFIG`. No just means longer commands. On Windows it asks **Set them for your user?** and sets the user PATH and `JEVLIN_CONFIG` instead. |
| **Set up the coding agents found on this machine now? [Y/n]** | Writes a skill and, where the agent supports them, hooks into its config. You see the plan first, with what made each agent count as present. |
| **Switch? [y/N]** | Asked only when Codex's config has a setting of your own that its permission profile would change, such as your own profile in `default_permissions`. It shows every line it would write. A bare Enter is no, which sets up the other agents and leaves Codex as it is. See [Codex](agents.md#codex). |

The profile block goes to:

| shell | file |
|---|---|
| zsh | `~/.zshrc` |
| bash on macOS | the first of `~/.bash_profile`, `~/.bash_login` and `~/.profile` that exists (a macOS terminal starts bash as a login shell), or `~/.bashrc` when that file already sources it |
| bash on Linux | `~/.bashrc` |
| fish | `~/.config/fish/config.fish`, in fish syntax |
| any other | nothing: setup prints the lines |

For an npm install the directory is npm's global `bin` directory, where the `jevlin` command is,
used only when only you can write it. The block is written even when the directory is already on
setup's own PATH, and one an earlier setup left in a file your shell no longer reads is removed.
Setup then prints one command that makes the terminal you are in find `jevlin` now
(`export PATH="$PATH:<dir>"`, `set -gx PATH $PATH <dir>` in fish, `$env:Path += ";<dir>"` on
Windows). To undo the block, delete it from `# >>> jevlin >>>` to `# <<< jevlin <<<`; if it no
longer has exactly one start and one end line, setup leaves the file alone and prints the lines to
add by hand.

Only an answer you typed counts. Interrupt a question, or close its input, and setup stops there
with a non-zero exit, recording and writing nothing. The same holds for `agents install`'s
**Proceed?**, Codex's **Switch?** and uninstall's confirmations: a typed no exits 0, no answer
exits non-zero.

Setup's flags (`-yes`, `-dry-run`, `-no-profile`, `-no-agents`, `-with`, `-home`) are in the
[reference](reference.md#commands). `-yes` answers the profile and agents questions, never
Codex's **Switch?**.

### Running setup again

Nothing needs removing first: update the binary, then run `jevlin setup`. It keeps your identity
and key, lets `connect` finish or repair its own onboarding, and updates its profile block and
agent files in place, reporting one already correct as "nothing to write: already set up". For an
installation elsewhere, set `JEVLIN_HOME` to it; setup has no other way to find it.
`setup -home <dir>` alone makes a separate installation and leaves your profile and agents alone,
even with `-yes`; it prints the `jevlin agents install -config` command for it, and an agent whose
skill came from your main installation keeps it. For a clean start, move `~/.jevlin` aside.

### The config it writes

`~/.jevlin/jevlin.toml` is in configuration format 1: a `schema = 1` line, `[router]`,
`[platform]` and `[agents]` with their URLs, and `[search]` with the state and sessions
directories ([reference, Config](reference.md#config) has every key). A config already in that
format is left byte for byte; one with a mistake in it stops setup with the error, and nothing is
written. One from an older jevlin, with no `schema` line, is set aside as `jevlin.toml.old` and a
fresh file written, your registration and key kept ([Moving from 0.6.x](#moving-from-06x)).

## The claim link

Once the config is written, setup prints the steps to claim your agent and waits up to three
minutes for you:

1. Open the link in a browser.
2. Sign in at `platform.jevlin.ai`, or create an account; you come back to the page.
3. Check the agent's name and confirm.

If the link does not open, type the printed code (shown as `AB12-CD34`; case and the hyphen do not
matter) at `/claim` on the same site. You need not do it then. Until you do, search works on the
fast tier within a daily cap, which `jevlin limits` shows; claiming lifts the cap and opens
`balanced` and `max`. The next search, or the next `jevlin connect`, finishes the claim once you
have confirmed. The link is valid for a week, and `jevlin connect` prints a fresh one after that.
Your claim on the platform is undone only at its console; nothing this client does revokes it.
[Claim records](#claim-records-and-connect--force) covers what connect does when a record is lost
or a registration expires.

## Check it works

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
[reference](reference.md#the---stdin-request) describes the request, the envelope and the
PowerShell form.

## Using it

Setup installs one skill, `jevlin`, into each agent that has skills (Claude Code, Cursor, Codex,
Pi, Hermes); opencode gets a plugin and a rules block instead. The skill is what the agent reads:
it tells the agent to prefer this search over its built-in one, how to build the request, how to
read the envelope, and what `/jevlin` does. In the agent:

| in the agent | what it does |
|---|---|
| `/jevlin <question>` | One search, as the skill specifies; the agent answers from the merged list. Naming jevlin or supersearch in a request does the same. |
| `/jevlin off` | The agent's built-in web search becomes the default again; this one runs only when you name it. |
| `/jevlin on` | This search is the default again. |
| `/jevlin status` | Which of the two is the default right now. |
| `/jevlin tier fast\|balanced\|max` | Sets the tier every search carries unless a request names one. |
| `/jevlin tier default` | Unsets it. |
| `/jevlin tier` | The tier in force. |

The skill tells the agent never to change the default or the tier on its own. Everything else is
the `jevlin` command in a shell ([reference, Commands](reference.md#commands)).

### Making it the default, or not

`/jevlin off` and `/jevlin on`, or `jevlin agents prefer off|on|status` from a shell, rewrite the
installed skills, so the choice takes effect in every agent with a skill from its next start;
opencode's rules block carries no preference and is not reached. The choice is saved beside your
config and survives reinstalls. While it is off, asking to "search through jevlin" still routes
that one search here.

### Choosing a tier

Every search carries a tier, which decides which engines the router asks. On the hosted service:

- **`fast`** asks the fast indexes of a named preset and is the quickest. `economy`, the default
  preset, asks two; `standard` asks three, for more coverage. `jevlin agents tier fast standard`
  (or `/jevlin tier standard` in the agent) picks one, and `jevlin agents tier status` lists the
  presets the router offers, with its estimate for each. It suits routine lookups, and it is the
  only tier an unclaimed agent may use.
- **`balanced`** asks four of the seven core engines: Exa, Brave, Parallel search and Perplexity
  search. Choose it when you want several independent sources, or when a fast search came back
  thin.
- **`max`** asks all seven core engines (adding Serper, Telem and TinyFish), with more results per
  engine, a wider merged list and longer excerpts. Choose it when agreement between independent
  engines matters, or when you want the most text per page; your agent then reads more.

`jevlin agents tier balanced` from a shell, or `/jevlin tier balanced` in the agent, sets the tier
every search carries unless the request names one; `default` unsets it, and `status` says what is
in force. `[router] tier` in the config does the same for an installation when no tier is saved.
The skill tells your agent to leave `tier` and `providers` unset, so your setting applies; with
none, the request carries none and the router applies the default for your key. A search costs
what its response's `usage.cost_micros` says; plans and prices are at
[jevlin.ai](https://jevlin.ai/#tiers). A request can also name the engines it wants on `balanced`
or `max` ([reference, Tiers and providers](reference.md#tiers-and-providers)).

## What leaves your machine

Each search sends your query and a small `trace` to the search router at `api.jevlin.ai`, inside
the search request. The trace lets the router group one task's searches: the agent's name, its
session, turn and call ids hashed with SHA-256 (the real ids never leave the machine), a call
counter, which context window of the session it is in, and the assistant text that led to the
search, scrubbed of secrets and capped at 32 KiB. With `[search] turn_end = true` in the config,
the agent also reports a turn in which a search was served. What each agent sends:

| agent | the trace's text | the turn end adds |
|---|---|---|
| Claude Code | An earlier message of the turn: Claude Code records a message after the hook runs, so the sentence beside the search is not in it. | What you asked, the final answer, each tool by name, duration and outcome, the model and its token counts (Claude Code 2.1.196 or later). |
| Cursor | Its latest thought or reply. | What you asked, the turn's searches, the reply and the model. |
| Codex | None. | What you asked, what the assistant wrote and its final answer, each tool by name and outcome. |
| opencode | The assistant text before the search. | What you asked, what the assistant wrote, each tool by name, time and outcome, and the model. |
| Pi | The assistant text before the search. | What you asked, the final answer, each tool by name, duration and outcome, the model and its token counts (Pi 0.80.4 or later). |
| Hermes | None: its hook payload carries no assistant text. | Nothing. |

A turn end never carries a tool's input or output, is sent only for a turn in which a search was
served, and goes only to the router your config names. `export JEVLIN_TRACE=off` sends no trace
and no turn end; searches work the same without them.

The hooks keep lineage files in `~/.jevlin/sessions`, one per workspace (Cursor: per
conversation). Only the agent that wrote a file reads it, so a second agent in a subdirectory
never sends the first one's text. An agent started by another as a shell command carries the
outer session and label: its searches are work the outer session asked for.

`jevlin search "<query>"` puts the query on the command line, visible in `ps` and your shell
history; `search --stdin`, which every skill teaches, keeps it out. The key is in neither. What
the service keeps of a search, and for how long, is the platform's to say: see its
[privacy policy](https://platform.jevlin.ai/privacy). [The trace](reference.md#the-trace) and
[the turn end](reference.md#the-turn-end) list every field and every scrub rule.

## Upgrading

```bash
jevlin upgrade                  # the latest release
jevlin upgrade -version X.Y.Z   # exactly that release, never an older one
jevlin upgrade -rollback        # put back the binary the last upgrade replaced, with no network
npm install -g @jevlinsearch/jevlin@latest    # an npm install is updated with npm
```

`upgrade` downloads only from the project's GitHub releases, requires `checksums.txt` to be
signed by the project's release key and the download to match it, and runs the new binary before
and after installing it, keeping the old one as `jevlin.previous`. It refuses an npm copy and a
development build, takes at most three minutes, and never touches your registration or config.
The new binary then rewrites the skills and hooks this installation set up, and for Codex the
block in `~/.codex/config.toml`: an earlier version's block, which opened Codex's network to every
host, becomes the permission profile, and a setting of yours the profile would change is left
and printed for you to change by hand. If that step fails, the upgrade still succeeded and the
message gives the command that finishes it. Restart open agents.

Once a day, when a Claude Code, Cursor, Codex, opencode or Pi session starts or ends, jevlin
checks for a newer release; Hermes and an agent set up by hand start no check. A native install
then installs a patch release of the version you have (0.7.0 to 0.7.1) the same way `upgrade`
does. A new minor release (0.8.0) is only recorded, and `jevlin status` and `jevlin doctor` say
so: before 1.0 a minor release may change what the installation must hold, so `jevlin upgrade`
installs it when you choose. An npm install is never replaced; the check notes the release and
`status` tells you npm's command. To be told rather than updated, add to your config:

```toml
[update]
mode = "notify"   # or "off" to stop the check; the default is "apply"
```

`status` says when the check last ran and what it found, and `doctor`'s `up to date` line says so
when it has not run for three days: usually no agent with the hooks has been opened, or the hooks
reach the network through a proxy on this machine, where no check is started (Codex's sandbox
does that). After `upgrade -rollback` the daily check leaves the release you rolled back from
alone and installs the next newer one. If an upgrade ever fails so badly that the old binary could
not be put back, `status` and `doctor` list the copies left beside the binary, none deleted, and
no automatic check runs until `jevlin upgrade` succeeds.

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
| `previous_in_use` | Windows: an older jevlin or agent process still runs `jevlin.exe.previous`. Your binary was put back; close that process and run `upgrade` again. |

### Moving from 0.6.x

This version reads only configuration format 1, and a 0.6.x installation's `jevlin.toml` is not
that. On a native install the daily check of 0.6.x replaces the binary with this version as it
would any release; from then on every command that reads the config (`search`, `fetch`,
`limits`, `status`, `doctor`, `connect`) stops with "this configuration is from an older jevlin;
run `jevlin setup`", while the hooks, which fail open, run the agent's tool call without a
lineage file. The daily check goes on running meanwhile, with the installation directory and
`[update] mode` the old file names, so a later release still reaches the machine, and
`jevlin upgrade -rollback` to 0.6.x holds this one where 0.6.x looks for the hold. On an npm
install, run `npm install -g @jevlinsearch/jevlin@latest`, then `jevlin setup`.

`jevlin setup` sets the old file aside as `jevlin.toml.old` (`.old.1`, `.old.2`, ... if the name is
taken), writes `jevlin.toml` in format 1, keeps your registration and search key, and rewrites the
hooks and skills of the agents it finds in place. The new file names the state and sessions
directories, the router, the portal and the agents API the old file named, because your agent
record is in that state directory and your key is bound to those hosts; where the old file named
none, it names the defaults, and `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL` and
`JEVLIN_AGENTS_API_URL`, when set, choose over both. The key and the files kept with it
(`credentials.json`, the registration journal, `claim.json`, the release check's record) belong in
the state directory's parent; 0.6.x kept them in its intake directory's parent instead, so where
those two differ setup moves them, and stops before changing anything if a file of the same name
is already there. Nothing else is migrated: a setting you changed by hand, such as `turn_end` or
`tier`, you copy into the new file yourself, under its format-1 key
([reference, Config](reference.md#config)). Files only the earlier version used are left where
they are, never read and never named; one of them may hold a key the earlier version used, so
look before deleting any of them.

## Removing it and coming back

```bash
jevlin uninstall -dry-run       # what would be removed; changes nothing
jevlin uninstall                # skills, hooks, plugins, and the profile block
jevlin uninstall -binary        # ...and this installation's binary
jevlin uninstall -purge-state   # ...and the registration, key and config
npm uninstall -g @jevlinsearch/jevlin         # an npm install is npm's to remove
```

A plain `uninstall` removes what setup wrote for this installation (`-home`, default `~/.jevlin`)
and keeps your registration, key and config; nothing is revoked. On Windows it reverts PATH and
`JEVLIN_CONFIG` only where they still hold setup's values. Another installation's files are left
and reported, and so is a hook of yours that sits in the same group as jevlin's (Claude Code's
`/hooks` adds one there when the matcher is the same): only jevlin's handler is taken out. It
lists leftover `.lock` files, which are safe to delete.

`-binary` also removes the binary, its `.previous` and an interrupted upgrade's leftovers, never
an npm copy; on Windows it moves the running binary aside and names the file to delete later. If
an agent's file could not be changed, it keeps the binary, because the hooks still in that file run
it, and exits non-zero; run it again once the file can be written.

`-purge-state` also destroys the registration, key and config. At a terminal you type the
installation path; `-yes` cannot answer it, and a run stopped there changes nothing. Your claim on
the platform is undone only at its console. Folders your config names outside the installation
are left and listed, and so is anything else left in the installation directory, by full path:
one of those files may hold a key an earlier version used, so look before deleting it. Close agent
sessions first, or a search may recreate folders.

To come back, run `jevlin setup`, with `-home <dir>` for a non-default installation: the same
agent, no new registration. A bare `jevlin connect` would register anew, because the profile
block that named this installation is gone. You can also set `~/.jevlin` aside instead of
removing it ([Set-aside installations](#set-aside-installations)).

## Troubleshooting

### jevlin: command not found

After an npm install, npm's global `bin` directory is not on your PATH (`npm prefix -g` names the
prefix; the command is in its `bin` directory, and in the prefix itself on Windows). Run setup
through npm's command by its full path, `"$(npm prefix -g)/bin/jevlin" setup -yes` (in Windows
PowerShell, `& "$(npm prefix -g)\jevlin.cmd" setup -yes`). Setup puts the same directory in your
shell profile and prints the one command that makes the terminal you are in see it now;
`"$(npm prefix -g)/bin/jevlin" doctor` names it too. After the installer, run
`~/.jevlin/bin/jevlin setup` the same way. Your agents' skills and hooks name the binary by its
full path and do not depend on PATH.

### A binary setup refuses

Setup refuses a binary someone else could replace, because your agents' hooks and skills and your
PATH will name it: one under the temporary directory, or one where the file or a directory above
it, on the path you ran or the file it links to, is owned by another user or writable by every
user (the sticky bit is no exception). `agents install` and `agents prefer` refuse the same, since
they write the same path. A directory its group can write is accepted when that group is your own
or one whose members can already become root (`admin`, `wheel`, `sudo`, `root`), which is how
Homebrew's prefix is set up. For any other group, such as Debian's `staff` on `/usr/local`, setup
prints a warning naming the directory and goes on: whether anyone else is in that group is
something only you can say. On Windows only the temporary-directory rule applies. To fix a
refusal, move the binary somewhere only you can write, such as `~/.local/bin`, and run it from
there.

### Set-aside installations

You can set `~/.jevlin` aside as `~/.jevlin.bak-<date>` (any `~/.jevlin.<name>` or
`~/.jevlin-<name>`). Setup lists what a set-aside copy holds, newest first, and moves it back only
when you say yes at a terminal, in pieces that belong together:

| piece | what setup does |
|---|---|
| **Registration and key** | Move as one. If `~/.jevlin` has its own, setup stops before anything moves and names both: keep one, move the other away. A half-made state directory there is renamed `state.unregistered-<time>`. |
| **Session files** | Merge one file at a time, never overwriting. |
| **Config** | Moves only if `~/.jevlin` has none. |

Symlinks and anything another account owns are not moved: a set-aside folder that is not yours is
never offered. The set-aside folder is removed only once it is empty.

### Claim records and connect -force

The claim link is kept in `~/.jevlin/claim.json`, beside your stored key, and only for the agent it
was issued to; it is never read from the state directory, which an agent's sandbox may write.
Before `jevlin connect` prints it, it asks the platform which agent your key belongs to: a claimed
agent shows no link, and if the platform cannot answer, none is shown. An interrupted registration
is finished on the next run, never registered twice.

A lost or damaged local record is rebuilt from the platform (one holding characters that would act
on your terminal counts as damaged, and `status` says it cannot be read and to run
`jevlin connect`), and so is one that names an agent your key does not belong to, which is what an
agent's sandbox can leave in the state directory. If the platform no longer recognizes the key,
`connect` stops, and `jevlin connect -force` replaces the credential: run it only on purpose; it
never rebuilds a record. An unclaimed registration that expires is replaced by the next
`jevlin connect` you run, changing only the platform agent and its key; a background resume never
replaces one. A lost claim link is replaced too: for a registration that is still unclaimed, a
foreground `jevlin connect` asks the platform for a fresh link and prints it. The old code is dead
the moment a new one is minted, which is why only a connect you run does this. Where the platform
cannot mint one, `connect` says so; run it again later, or wait for the registration to expire
and connect again.
