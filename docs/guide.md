# Jevlin guide

jevlin in the order you meet it. Per-agent detail: [agents.md](agents.md); tables: [reference.md](reference.md).

## What you need

| | |
|---|---|
| **A moment to claim your agent** | Setup prints a link. Sign in at `platform.nyks.dev` (or create an account) and approve mining. Search works before you do; only the reward waits. |
| **Somewhere to be paid** | Setup makes a wallet for you, or you paste a `twilight1…` address you already control, from Keplr for example. |

### Where rewards land

If setup makes the wallet, it prints a **24-word recovery phrase exactly once**: write it on
paper before you continue. It is stored nowhere, and anyone who has it controls the money. The
key on this machine only receives; spending needs the passphrase you chose.

The wallet lives in `~/.jevlin/wallet`, and neither a search nor a flush reads it. On Windows
the folder and every file in it carry an access list with only you on it, so access another
program adds to `~/.jevlin`, such as an agent's sandbox, does not reach it. `jevlin setup`
repairs a wrong list, and stops on something it cannot secure, such as a link; `doctor`'s
`wallet access` line names anyone else who can read the wallet.

On macOS and Linux an agent's sandbox runs as you, so a sandboxed command can read the wallet
file. Only your passphrase protects the key inside: use one you use nowhere else, and keep the
24 words off the machine. Anyone who copies the file can try passphrases against it on their
own computer, without limit, and nothing tells you they are trying.

## Setup

Install globally, as the [README](../README.md#install) shows, then run `jevlin setup`. Setup
refuses to run from an `npx` cache or a project's `node_modules`, whose paths would disappear.

| it asks | what your answer does |
|---|---|
| **Use it? [Y/n]** | Only when a previous installation is set aside beside `~/.jevlin`. Setup first says where it is and what it holds. Yes brings back your wallet, registration and key, and skips the questions they answer. |
| **Enable mining rewards? [y/N]** | A bare Enter is no, and search still works. Change your mind later with `jevlin mining enable` or `jevlin mining disable`. |
| **Payout address (leave empty to create a wallet here):** | Asked only after yes. Paste an address, or leave it empty: it asks for a keyfile passphrase twice, then prints the 24 words once. Have paper ready. |
| **Add them to ~/.zshrc? [Y/n]** | Or `~/.bashrc`, whichever your shell reads. One marked block puts the binary on PATH and sets `JEVLIN_CONFIG`, plus `JEVLIN_WALLET_DIR` when a wallet was made here. No just means longer commands. On Windows it asks **Set them for your user?** and sets PATH and `JEVLIN_CONFIG` only. |
| **Set up the coding agents found on this machine now? [Y/n]** | Writes a skill and, where the agent supports them, hooks into its config. You see the plan first, with what made each agent count as present. |

Only an answer you typed counts. Interrupt a question, or close its input, and setup stops there
with a non-zero exit, recording and writing nothing; an interrupted **Enable mining rewards?** is
asked again next time. The same holds for `agents install`'s **Proceed?**, uninstall's
confirmations and `wallet send`'s: a typed no exits 0, no answer exits non-zero.

| flag | effect |
|---|---|
| `-yes` | Answers yes to the profile and agents questions, terminal or not. Answers **Use it?** only at a terminal. Never answers the mining question. Without `-yes` or a terminal, setup leaves the profile and agents alone and prints the command for each. |
| `-dry-run` | Lists every file it would write or move, and changes nothing. |
| `-no-profile` | Leaves the shell profile (Windows: user environment) alone, even with `-yes`. |
| `-no-agents` | Skips agent detection, even with `-yes`. `-with` still sets up what it names. |
| `-with <id>` | Sets up `claude`, `codex`, `cursor`, `opencode`, `pi` or `hermes`, found or not. Repeatable. |

## The claim link

Setup ends by printing a claim link and waits a few minutes for you to visit it. You need not do
it then: search already works, and using an agent later resumes the claim on its own. An interrupted registration is finished on the next run, never registered twice. A lost local
record is rebuilt from the platform. If the platform no longer recognizes the key, `connect`
stops, and `jevlin connect -force` replaces the credential: run it only on purpose. An
unclaimed registration that expires is replaced by the next `jevlin connect` you run, changing
only the platform agent and its key; a background resume never replaces one. A rebuilt
registration with no link prints a one-line notice: wait for the claim or expiry, or `-force`.

`jevlin connect` also re-runs onboarding by hand. `jevlin mining disable` stops mining and
revokes access at the rewards service; your approval on the platform is revoked only at its console.

## Then check

```bash
jevlin status          # what this installation has and has not completed
jevlin payout show     # ACTIVE, and the address as the chain renders it
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
wallet, identity, key and recorded searches, asks the mining question only if it has no answer,
and lets `connect` finish or repair its own onboarding. It updates its profile block and agent
files in place, reporting one already correct as "nothing to write: already set up". For an
installation elsewhere, set `JEVLIN_HOME` to it; setup has no other way to find it. `setup
-home <dir>` alone makes a separate installation and leaves your profile and agents alone, even
with `-yes`; it prints the `jevlin agents install -config` command for it, and an agent whose
skill came from your main installation keeps it. For a clean start, move `~/.jevlin` aside.

## Making it the default, or not

The skill tells your agent to prefer this search over its built-in one. In an agent that got a
skill, `/jevlin off` makes the agent's own search the default, `/jevlin on` restores this one,
and `/jevlin status` says which is in force; from a shell, `jevlin agents prefer off|on|status`.
The choice is saved beside your config and survives reinstalls. While it is off, asking to
"search through jevlin" still routes that one search here; other searches earn nothing.

## What travels with a search

Each search carries a small `trace` so the router can group one task's searches: hashed session
and turn ids (your agent's real ids never leave the machine), a call counter, and the assistant
text just before the search, capped at 32 KB. That text is conversation content leaving your
machine, and it goes only to the search router, inside the search request. The rewards service
receives metadata only, never the query or the text. `export JEVLIN_TRACE=off` sends no trace;
searches are metered and earn the same.

The hooks keep lineage files in `~/.jevlin/sessions`, one per workspace (Cursor: per
conversation). Only the agent that wrote a file reads it, so a second agent in a subdirectory
never sends the first one's narration. An agent started by another as a shell command carries
the outer session and label: its searches are work the outer session asked for.

`jevlin search "<query>"` puts the query in the command line, visible in `ps` and your shell
history; `search --stdin`, which every skill teaches, keeps it out. The key is in neither.

## Earning

- **Your first reward takes one to two hours.** You join an epoch two ahead, and it has to
  close, reconcile and settle. Nothing is wrong during the wait.
- **One verified search per epoch makes you eligible.** It is a threshold, not a weight.
- **The pot splits equally among everyone eligible**, so your share falls as more people join.
- **A long idle gap can miss an epoch.** A flush submits recorded searches, and the next search
  or agent session starts one. Evidence that misses the epoch's deadline is late. `jevlin
  flush` submits what is pending by hand.

The rewards service checks each recorded request id with the search provider, never taking this
client's word for volume. The envelope's `mining` object says whether a search was recorded;
`jevlin earnings` lists what the chain has paid to your payout address.

## When doctor is unsure

**`recording UNKNOWN`** with "recent miner activity, but nothing is queued locally or verified
at the AS" means something ran, yet no search was recorded: usually an agent using a different
config, or a sandbox that cannot write the intake directory. Check `jevlin agents status` and
re-run `jevlin agents install`. `recording` never says `NO`; **`could not determine`** gives its
reason, and is not a fault either.

**`intake writable could not determine`** means neither the intake directory nor its parent
exists, which happens only when `miner.intake_dir` names a custom path; `doctor` will not build
a tree to test it. An `OK` speaks only for your terminal, not an agent's sandbox. With Codex,
re-running `jevlin agents install` fixes a `NO`.

## Changing the payout address

Your first address takes effect when you set it. A different one waits for a Slot operator's
approval, and you are paid at the old one until then. Ask at
<https://platform.nyks.dev/contact-us>, naming the new address. Nobody needs your key, your
recovery phrase or the contents of `~/.jevlin` for this, and no operator will ask for them.

## Sending funds

`jevlin wallet send -to <address> -amount <n>` moves funds out of the wallet. If the node's
reply is lost after it accepts the transaction, `send` prints **"outcome unknown"** and exits
non-zero. Do not start a new transfer: run `wallet send` or `wallet balance` again, and it asks
the node about that transaction, then reports what happened or re-sends the same signed bytes.
`-abandon-pending` discards the record without finding out, if you are sure it does not matter.
The client trusts its RPC node; see the [node settings](reference.md#wallet).

## The older manual path

The portal's older commands, `enroll`, `login`, `join`, `wallet register` and `payout set`,
still work beside `connect`. `provider` belongs to that path and applies only on a Slot whose
profile is `OPENROUTER_V1`; on the default profile you supply no provider key. `jevlin help`
describes each.

## Upgrading

```bash
jevlin upgrade                  # the latest release
jevlin upgrade -version X.Y.Z   # exactly that release, never an older one
jevlin upgrade -rollback        # put back the binary the last upgrade replaced
npm install -g jevlin@latest    # an npm install is updated with npm
```

`upgrade` downloads only from the project's GitHub releases, checks the checksum, and runs the
new binary before and after installing it. It keeps the old one as `jevlin.previous`;
`-rollback` swaps back with no network. It refuses an npm copy and a development build, takes at
most three minutes, and never touches your wallet, registration or config. The new binary then
rewrites the skills and hooks this installation set up. If that step fails, the upgrade still
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

## Removing it and coming back

```bash
jevlin uninstall -dry-run       # what would be removed; changes nothing
jevlin uninstall                # skills, hooks, plugins, and the profile block
jevlin uninstall -binary        # ...and this installation's binary
jevlin uninstall -purge-state   # ...and the wallet, identity, key, evidence and config
npm uninstall -g jevlin         # an npm install is npm's to remove
```

A plain `uninstall` removes what setup wrote for this installation (`-home`, default
`~/.jevlin`). On Windows it reverts PATH and `JEVLIN_CONFIG` only where they still hold setup's
values. Another installation's files are left and reported. Your wallet, registration, key,
unsent searches and config stay, and nothing is revoked. `-binary` also removes the binary, its
`.previous` and an interrupted upgrade's leftovers, never an npm copy; on Windows it moves the
running binary aside and names the file to delete later. `uninstall` lists leftover `.lock`
files, which are safe to delete.

`-purge-state` destroys the participant state. At a terminal, you type the wallet's address
(with no wallet, the installation path); `-yes` cannot answer it. It first tries, for up to
eight seconds, to revoke access at the rewards service. Close agent sessions first, or a search
may recreate empty folders. If you made the wallet here, keep the 24 words or move the funds
before you purge. A run stopped at its confirmation leaves the installation exactly as it was.

To come back, run `jevlin setup`, with `-home <dir>` for a non-default installation: the same
agent, the same wallet, no new registration. A bare `jevlin connect` would register anew,
because the profile block that named this installation is gone.

You can instead set `~/.jevlin` aside as `~/.jevlin.bak-<date>` (any `~/.jevlin.<name>` or
`~/.jevlin-<name>`). Setup lists what a set-aside copy holds, newest first, and moves it back
only when you say yes at a terminal, in pieces that belong together:

| piece | what setup does |
|---|---|
| **Registration and key** | Move as one. If `~/.jevlin` has its own, setup stops before anything moves and names both: keep one, move the other away. A half-made key there is renamed `state.unenrolled-<time>`. |
| **Wallet** | Moves whole. A `~/.jevlin` with a wallet is simply used. An unfinished wallet folder there is renamed `wallet.incomplete-<time>`. |
| **Unsent searches, session files** | Merge one file at a time, never overwriting. |
| **Config** | Moves only if `~/.jevlin` has none. |

Symlinks are not moved, and the set-aside folder is removed only once it is empty.

The profile lines are one block between `# >>> jevlin >>>` and `# <<< jevlin <<<`; delete it to
undo them. If the block no longer has exactly one start and one end line, setup leaves the file
alone and prints the lines to add by hand.
