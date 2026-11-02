# Jevlin guide

jevlin in the order you meet it. What each agent gets is in [agents.md](agents.md), what jevlin
sends in [privacy.md](privacy.md), and every command, flag and file in [reference.md](reference.md).

## Install

Install globally, as the [README](../README.md#how-do-i-install-it) shows, then run `jevlin setup`:

```bash
npm install -g @jevlinsearch/jevlin
jevlin setup
```

Setup refuses to run from an `npx` cache or a project's `node_modules`, whose paths would
disappear, and from a place someone else could replace the binary in
([A binary setup refuses](#a-binary-setup-refuses)). If your shell then says
`jevlin: command not found`, see [jevlin: command not found](#jevlin-command-not-found).

### Let your agent install it

An agent can run this install itself. The marketplaces carry a setup skill, not the search: it
installs the client and runs `jevlin setup`, which writes the agent's own skill and hooks.

```bash
npx skills add jevlinai/jevlin-go                          # any agent with skills (skills.sh)
/plugin install jevlin --marketplace jevlinai/jevlin-go    # Claude Code, 2.1.275 or later
claude plugin marketplace add jevlinai/jevlin-go           # Claude Code, older: then /plugin install jevlin@jevlin
codex plugin marketplace add jevlinai/jevlin-go            # Codex CLI: then /plugins
```

Cursor loads the same package from its Marketplace once it is listed there, and from the
repository until then.

### Installing without npm

`curl -fsSL https://jevlin.ai/install.sh | sh` (macOS, Linux and WSL) or
`irm https://jevlin.ai/install.ps1 | iex` (Windows PowerShell; `install.cmd` runs it from CMD)
installs the latest release. The scripts are this repository's [`install/`](../install/). Each
checks the download against the release's `checksums.txt`, which is mandatory, and checks that
file's signature against the project's release key when a tool on the machine can (`install.sh`:
openssl 1.1.1 or newer, node or python3; `install.ps1`: node); a signature that fails stops the
install. It puts
the binary at `~/.jevlin/bin/jevlin` (`$JEVLIN_HOME/bin` when that is set) and starts
`jevlin setup` when you ran it at a terminal; otherwise it prints the setup command to run.

| variable | effect |
|---|---|
| `JEVLIN_VERSION=0.7.0` | Installs exactly that release instead of the latest. |
| `JEVLIN_HOME=/path` | The installation directory, instead of `~/.jevlin`. |
| `JEVLIN_NO_SETUP=1` | Installs the binary and stops. |

In PowerShell, set them first as `$env:JEVLIN_VERSION` and so on. By hand: download the archive for
your OS and architecture from the [releases page](https://github.com/jevlinai/jevlin-go/releases),
check it against that release's `checksums.txt`, put `jevlin` in a directory only you can write,
such as `~/.local/bin`, and run `jevlin setup` from there.

### From source

`go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`, or `make build` in a clone, with Go
1.27 or newer. A source build reports its version as `dev`, so nothing updates it; install a
release for updates ([reference, Building from source](reference.md#building-from-source)).

## Setup

Setup registers this installation with the search platform and stores the key it gets back, so
there is nothing to copy or paste. In order, it offers to bring back a set-aside installation,
writes `~/.jevlin/jevlin.toml` ([The config it writes](#the-config-it-writes)), registers and prints
the claim steps ([The claim link](#the-claim-link)), and then asks about your shell profile and
your coding agents:

| it asks | what your answer does |
|---|---|
| **Use it? [Y/n]** | Only when a previous installation is set aside beside `~/.jevlin`. Yes brings back your registration, key and claim link ([Set-aside installations](#set-aside-installations)). |
| **Add them to ~/.zshrc? [Y/n]** | Writes one marked block to the file your shell reads (below), putting the binary's directory on PATH and setting `JEVLIN_CONFIG`. No just means longer commands. On Windows it asks **Set them for your user?** and sets the user PATH and `JEVLIN_CONFIG` instead. |
| **Set up the coding agents found on this machine now? [Y/n]** | Writes a skill and, where the agent supports them, hooks into its config. You see the plan first. |
| **Switch? [y/N]** | Only when Codex's config has a setting of your own that its permission profile would change. A bare Enter is no, which sets up the other agents and leaves Codex as it is ([agents, Codex](agents.md#codex)). |

| shell | the profile block goes to |
|---|---|
| zsh | `~/.zshrc` |
| bash on macOS | the first of `~/.bash_profile`, `~/.bash_login` and `~/.profile` that exists, or `~/.bashrc` when that file already sources it |
| bash on Linux | `~/.bashrc` |
| fish | `~/.config/fish/config.fish`, in fish syntax |
| any other | nothing: setup prints the lines |

For an npm install the directory is npm's global `bin` directory, where the `jevlin` command is.
Setup then prints one command that makes the terminal you are in find `jevlin` now; a terminal
opened afterwards needs nothing. To undo the block, delete it from `# >>> jevlin >>>` to
`# <<< jevlin <<<`.

Only an answer you typed counts. Interrupt a question, or close its input, and setup stops with a
non-zero exit, recording and writing nothing; a typed no exits 0. `-yes` answers the profile and
agents questions, never Codex's **Switch?**, and the other flags are in the
[reference](reference.md#commands).

### Running setup again

Nothing needs removing first: update the binary, then run `jevlin setup`. It keeps your identity
and key, finishes or repairs its own onboarding, and updates its profile block and agent files in
place, reporting one already correct as "nothing to write: already set up". For an installation
elsewhere, set `JEVLIN_HOME` to it. `setup -home <dir>` alone makes a separate installation and
leaves your profile and agents alone; it prints the `jevlin agents install -config` command for
it.

### The config it writes

`~/.jevlin/jevlin.toml` is in configuration format 1: a `schema = 1` line, `[router]`,
`[platform]` and `[agents]` with their URLs, and `[search]` with the state and sessions directories
([reference, Config](reference.md#config) has every key). A config already in that format is left
byte for byte; one with a mistake in it stops setup with the error. One from an older jevlin, with
no `schema` line, is set aside as `jevlin.toml.old` and a fresh file written, your registration and
key kept ([Moving from 0.6.x](#moving-from-06x)).

## The claim link

Once the config is written, setup prints the steps to claim your agent and waits up to three
minutes for you:

1. Open the link in a browser.
2. Sign in at `platform.jevlin.ai`, or create an account; you come back to the page.
3. Check the agent's name and confirm.

If the link does not open, type the printed code (shown as `AB12-CD34`; case and the hyphen do not
matter) at `/claim` on the same site. You need not do it then. Until you do, search works on the
fast tier within a daily cap, which `jevlin limits` shows; claiming opens `balanced` and `max`. The
next search, or `jevlin connect`, finishes the claim once you have confirmed. The link and the
unclaimed registration last a week; after that, `jevlin connect` registers again and prints a new
link. Your claim on the platform is undone only at its console.

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

The [reference](reference.md#the---stdin-request) describes the request, the envelope and the
PowerShell form.

## Using it

Setup installs one skill, `jevlin`, into each agent that has skills (Claude Code, Cursor, Codex,
Pi, Hermes); opencode gets a rules block to paste into `AGENTS.md` instead. The skill tells the
agent to prefer this search over its built-in one, how to build the request and how to read the
answer. Naming jevlin in a request sends that search here. In Claude Code and Cursor you can also
type (in Codex, `$jevlin` instead of `/jevlin`):

| in the agent | what it does |
|---|---|
| `/jevlin <question>` | One search; the agent answers from the merged list. |
| `/jevlin off` | The agent's built-in web search becomes the default again; this one runs only when you name it. |
| `/jevlin on` | This search is the default again. |
| `/jevlin status` | Which of the two is the default right now. |
| `/jevlin tier fast\|balanced\|max` | Sets the tier every search carries unless a request names one. |
| `/jevlin tier default` | Unsets it. |
| `/jevlin tier` | The tier in force. |

The skill tells the agent never to change the default or the tier on its own.

### Making it the default, or not

`/jevlin off` and `/jevlin on`, or `jevlin agents prefer off|on|status` from a shell, rewrite the
installed skills, so the choice takes effect in every agent with a skill from its next start;
opencode's rules block carries no preference. The choice is saved beside your config and survives
reinstalls.

### Choosing a tier

Every search carries a tier, which decides which engines the router asks. On the hosted service:

- **`fast`** asks the fast indexes of a named preset and is the quickest. `economy`, the default
  preset, asks two; `standard` asks three, for more coverage. `jevlin agents tier fast standard`
  (or `/jevlin tier standard`) picks one, and `jevlin agents tier status` lists the presets the
  router offers. It suits routine lookups, and it is the only tier an unclaimed agent may use.
- **`balanced`** asks four of the seven core engines. Choose it when you want several independent
  sources, or when a fast search came back thin.
- **`max`** asks all seven core engines, with more results from each. Choose it when agreement
  between independent engines matters.

On every tier an agent's search gets the same short excerpt of each page; when it needs more of a
page, it reads the page in full with `fetch`. `jevlin agents tier balanced`, or
`/jevlin tier balanced`, sets the tier every search carries unless the request names one;
`[router] tier` in the config does the same for an installation when no tier is saved. The skill
tells your agent to leave the tier alone, so your setting applies; with none, the router applies
your key's default. Plans and prices are at [jevlin.ai](https://jevlin.ai). The engines, presets
and sizes are in [reference, Tiers and providers](reference.md#tiers-and-providers).

## What leaves your machine

See [privacy.md](privacy.md): what each search and turn end sends, per agent, and how to switch it
off.

## Upgrading

```bash
jevlin upgrade                  # the latest release
jevlin upgrade -version X.Y.Z   # exactly that release, never an older one
jevlin upgrade -rollback        # put back the binary the last upgrade replaced, with no network
npm install -g @jevlinsearch/jevlin@latest    # an npm install is updated with npm
```

`upgrade` downloads only from the project's GitHub releases, requires `checksums.txt` to be signed
by the project's release key and the download to match it, and runs the new binary before and after
installing it, keeping the old one as `jevlin.previous`. It never touches your registration or
config. The new binary then rewrites the skills and hooks this installation set up. Restart open
agents. If it fails, the first word after `upgrade:` says what to do
([reference, Upgrade failures](reference.md#upgrade-failures)).

Once a day, when a Claude Code, Cursor, Codex, opencode or Pi session starts or ends, jevlin checks
for a newer release. A native install then installs a patch release of the version you have (0.7.0
to 0.7.1) the same way `upgrade` does; a new minor release (0.8.0) is only recorded, and
`jevlin status` and `jevlin doctor` say so, because before 1.0 a minor release may change what the
installation must hold. An npm install is never replaced; the release is recorded for `status` and
`doctor` to report. To be told rather than updated, add to your config:

```toml
[update]
mode = "notify"   # or "off" to stop the check; the default is "apply"
```

`doctor`'s `up to date` line says when the check has not run for three days: usually no agent with
the hooks has been opened. After `upgrade -rollback` the daily check leaves the release you rolled
back from alone.

### Moving from 0.6.x

This version reads only configuration format 1. After a 0.6.x install takes this version, every
command that reads the config, `search` included, stops with "this configuration is from an older
jevlin; run `jevlin setup`" until you do; the hooks fail open, so your agents' other tool calls are
unaffected. On an npm install, run `npm install -g @jevlinsearch/jevlin@latest` first.

`jevlin setup` sets the old file aside as `jevlin.toml.old`, writes `jevlin.toml` in format 1 naming
the directories and hosts the old one named, keeps your registration and key, and rewrites your
agents' hooks and skills. A setting you changed by hand, such as `turn_end` or `tier`, you copy
into the new file yourself ([reference, Config](reference.md#config)). Files only the earlier
version used are left where they are; one of them may hold a key, so look before deleting any.

## Removing it and coming back

```bash
jevlin uninstall -dry-run       # what would be removed; changes nothing
jevlin uninstall                # skills, hooks, plugins, and the profile block
jevlin uninstall -binary        # ...and this installation's binary
jevlin uninstall -purge-state   # ...and the registration, key and config
npm uninstall -g @jevlinsearch/jevlin         # an npm install is npm's to remove
```

A plain `uninstall` removes what setup wrote for this installation and keeps your registration, key
and config; nothing is revoked. Another installation's files, and a hook of yours, are left and
reported. `-binary` also removes the binary and its `.previous`, never an npm copy; if an agent's
file could not be changed it keeps the binary, because the hooks still in that file run it, and
exits non-zero. `-purge-state` also destroys the registration, key and config, after you type the
installation path at a terminal (`-yes` cannot answer it); anything else left in the installation
directory is listed, since one of those files may hold a key an earlier version used. Your claim on
the platform is undone only at its console. Close agent sessions first, or a search may recreate
folders.

To come back, run `jevlin setup`, with `-home <dir>` for a non-default installation: the same agent,
no new registration. You can also set `~/.jevlin` aside instead
([Set-aside installations](#set-aside-installations)).

## Troubleshooting

### jevlin: command not found

After an npm install, npm's global `bin` directory is not on your PATH (`npm prefix -g` names the
prefix; the command is in its `bin` directory, and in the prefix itself on Windows). Run setup
through npm's command by its full path, `"$(npm prefix -g)/bin/jevlin" setup -yes` (in Windows
PowerShell, `& "$(npm prefix -g)\jevlin.cmd" setup -yes`); setup puts the directory in your shell
profile and prints the command that makes this terminal see it. After the installer, run
`~/.jevlin/bin/jevlin setup` the same way. Your agents' skills and hooks name the binary by its full
path and do not depend on PATH.

### A binary setup refuses

Setup refuses a binary someone else could replace, because your agents' hooks and skills and your
PATH will name it: one under the temporary directory, or one where the file or a directory above it
is owned by another user or writable by every user. A directory its group can write is accepted
when that group is your own or one whose members can already become root (`admin`, `wheel`, `sudo`,
`root`), as Homebrew's prefix is; for any other group setup warns and goes on. On Windows only the
temporary-directory rule applies. To fix a refusal, move the binary somewhere only you can write,
such as `~/.local/bin`, and run it from there.

### Set-aside installations

You can set `~/.jevlin` aside as `~/.jevlin.bak-<date>` (any `~/.jevlin.<name>` or
`~/.jevlin-<name>`). Setup lists what a set-aside copy holds, newest first, and moves it back only
when you say yes at a terminal, in pieces that belong together:

| piece | what setup does |
|---|---|
| **Registration and key** | Move as one. If `~/.jevlin` has its own, setup stops before anything moves and names both: keep one, move the other away. |
| **Session files** | Merge one file at a time, never overwriting. |
| **Config** | Moves only if `~/.jevlin` has none. |

Symlinks and anything another account owns are not moved, and the set-aside folder is removed only
once it is empty.

### Claim records and connect -force

The claim link is kept beside your key in `~/.jevlin/claim.json`, never in the state directory,
which an agent's sandbox may write. Before `jevlin connect` prints it, it asks the platform which
agent your key belongs to: a claimed agent shows no link, and if the platform cannot answer, none is
shown. An interrupted registration is finished on the next run, never registered twice, and a lost
or damaged local record is rebuilt from the platform. A lost claim link for a registration that is
still unclaimed is replaced by a foreground `jevlin connect`, which asks the platform for a fresh
one; the old code stops working when it does. If the platform no longer recognizes the key,
`connect` stops, and `jevlin connect -force` replaces the credential: run it only on purpose.
