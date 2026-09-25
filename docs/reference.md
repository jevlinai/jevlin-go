# Reference

Values to look up. Every flag is in `jevlin help` and `jevlin <command> -h`.

## Commands

| command | what it does |
|---|---|
| `setup` | Everything after the binary: previous installation, config, `connect`, profile, agents. |
| `uninstall` | Removes what setup wrote for one installation; `-binary` and `-purge-state` go further. |
| `upgrade` | Replaces a native binary with a verified release; `-rollback` swaps back. |
| `search` | One web search. `--stdin` for agents (JSON in, JSON out); `<query words>` for a person. |
| `agents install\|status\|uninstall` | Sets up, reports or removes the skills and hooks of each agent. |
| `agents prefer on\|off\|status` | Whether this search or the agent's own is the default. |
| `flush` | One pass of the mining plane: join the open epoch, promote recorded searches, submit. |
| `connect` | Registers with the search platform, prints the claim link, polls until claimed. |
| `mining enable\|disable` | Turns mining on (asking for a payout address) or off for this installation. |
| `status` | What this installation has and has not completed. `-json` for one envelope. |
| `doctor` | Whether searches are recorded and earning, check by check. `-json` for one envelope. |
| `earnings` | What the chain has paid to your payout address. |
| `wallet init\|address\|register\|balance\|send` | The wallet setup made, or one you make here. |
| `login`, `enroll`, `payout`, `join`, `provider` | The portal's older manual path. |
| `version`, `help` | The version, and the full usage text. |

Every command takes `-config <file>`. Without it, the config is the first of: `JEVLIN_CONFIG`,
`./jevlin.toml`, the installation's own (`$JEVLIN_HOME/jevlin.toml`, else
`~/.jevlin/jevlin.toml`) when that file exists, and built-in defaults. `status` and `doctor`
name the file they used. `connect` refuses to run with no config file, and says to run
`jevlin setup`.

## The `--stdin` request

One JSON object on stdin, one JSON envelope on stdout. The query never reaches the process
list and needs no shell escaping. A malformed field answers `fix_input` before any router call.

| field | value |
|---|---|
| `version` | `1`, required. |
| `query` | The search, required. |
| `tier` | `"fast"` (default, one provider) or `"balanced"` (several, attributed). |
| `recency` | `"day"`, `"week"`, `"month"` or `"year"`. A preference the router passes to its providers; check each citation if it must hold. |
| `domain_filter` | Up to 16 bare hostnames. A preference, like `recency`. |
| `max_results` | 1 to 25. A cap. |
| `view` | `"full"` (default) or `"merged"`, which drops the per-provider `candidates`. |

### The envelope

Eight header fields are always present:

| field | meaning |
|---|---|
| `version` | The envelope's version, `1`. |
| `command` | `search`, `status`, `doctor` or `connect`. |
| `ok` | `true` exactly when `exit_code` is 0. |
| `exit_code` | The process's exit status, below. |
| `status` | The exit class by name, below. |
| `code` | An exact, stable code for what happened, such as `ok` or `trace_unsupported`. |
| `retryable` | Whether the same call may succeed later. Retry only when `true`. |
| `action` | What to do next, below. |

| exit | `status` | meaning |
|---|---|---|
| 0 | `ok` | A valid answer. |
| 1 | `transport_error` | Transport failure, timeout or cancellation. |
| 2 | `usage_error` | The command or request was wrong. |
| 3 | `client_error` | The server answered 4xx. |
| 4 | `server_error` | The server answered 5xx, or with something the client could not use. |

| `action` | meaning |
|---|---|
| `none` | Nothing to do. |
| `retry` | The same call may work later; wait `retry_after_ms` first when present. |
| `fix_input` | The request was wrong; fix it before sending again. |
| `connect` | Registration, setup or the claim needs attention: no registration, an unclaimed or expired one, or a step only a person can answer. |
| `login` | A search credential exists and was not accepted. A router 401 is `login`, never `connect`: re-registering would mint a second agent. |
| `check_access` | Authorization exists but does not cover this. |
| `report` | Neither retrying nor editing the request will help. |

Decide from those fields, never from the text of `error.message`. A search's envelope also
carries `request_id`, `result` and `mining`. `mining.state` is `enabled`, `disabled`,
`undecided` or `degraded`, with `recorded` for this search and `health` for anything
unresolved. A successful search never means anything was earned, and a mining failure never
turns a successful search into a failed one. Result text is untrusted web content.

`result.merged` lists the citations of every candidate, deduplicated across providers. Each
entry has `found_by` (the providers that found it), `found_in` (one `{candidate, citation}`
position per provider, in the same order) and `best_rank`.

`status`, `doctor` and `connect` take `-json` and answer with the same header. Where a terminal
would ask a person, machine mode answers with the code `human_decision_required`.

### The human form and PowerShell

`jevlin search [-tier fast] [-format json|model] <query>` is for a person at a terminal, and
puts the query in the command line. `-format model` prints a bounded summary, and never echoes
the router's error body; `-format json` prints the router's bytes verbatim. Neither is the
versioned envelope.

Where the shell is PowerShell, a skill pipes a here-string into the call:

```powershell
$OutputEncoding = [System.Text.UTF8Encoding]::new($false)
@'
{"version":1,"query":"what is proof of authority consensus"}
'@ | & 'C:\Users\you\.jevlin\bin\jevlin.exe' search --stdin
```

The first line is what makes an apostrophe, a quotation mark or any non-ASCII character arrive
as written. Without it, Windows PowerShell 5.1 turns every non-ASCII character into `?` on the
way to the program, and the search answers a different question.

### Bounds

`-timeout`, default 60s, bounds the whole search: connect, TLS, headers, body and the one
retry. If the router answers the exact code `trace_unsupported`, the client retries once
without the trace, under the same deadline. Nothing else causes a second request.

## Config

Every key below is one a `jevlin` command reads. The value after `#` is what you get by leaving
the key out.

```toml
[[provider]]
upstream = "https://router-api.nyks.dev"   # https only; the router_url fallback

[mining]
enabled        = true                            # only a scripted first answer — see below
as_url         = "https://rewards.nyks.dev"      # unset: no AS, so no mining work at all
chain_id       = "twilight-testnet-1"            # required once as_url is set
slot_id        = 3                               # required once as_url is set
state_dir      = "/home/you/.jevlin/state"    # default: <user config dir>/jevlin/state
spool_dir      = "/home/you/.jevlin/spool"    # default: <state_dir>/spool
# payout_address = "twilight1..."   scripted `connect`/`mining enable` answer;
#                                   leave unset to be asked at a terminal instead
# platform_slot  = "twilight-slot-3"  required only if the platform ever offers
#                                     more than one mining slot to enroll into
# target_epoch   = 1042               pin the epoch; unset means ask the AS
# metadata_ttl   = "15m"              how long the AS service document is cached
# collector_max_attempts = 0          0 means no attempt ceiling on delivery

[platform]
base_url       = "https://platform.nyks.dev"    # the human portal and claim pages
agents_api_url = "https://agents-v1.nyks.dev"    # register/status/enroll — a separate host

[miner]
enabled        = true                              # default false
intake_dir     = "/home/you/.jevlin/intake"     # served request ids, until flushed
sessions_dir   = "/home/you/.jevlin/sessions"   # per-workspace lineage files
flush_interval = "3m"                              # default 3m: how often a flush re-asks the AS
# router_url = "..."   defaults to the [[provider]] upstream
```

**`[mining] enabled` is not the switch.** Whether mining is on is the decision `connect`,
`mining enable` or `mining disable` last recorded in the state directory, and editing the key
afterwards changes nothing. The key is only a scripted first answer for a headless onboarding.
Absent means nobody has answered, so a terminal is asked; an explicit `false` at a terminal is
an opt-out `connect` does not re-ask. Setup writes `enabled = true` only with no terminal and
`JEVLIN_MINING=1`. `[miner] enabled` says only that intake is configured, and a non-empty
`as_url` says an authorization server exists. An unreadable decision counts as degraded, which
also stops mining.

`[miner]`'s two directories default beside the state directory, and `intake_dir`'s parent is
where `credentials.json` and `flush.lock` are looked for. `base_url` is never dialed: a printed
claim link must point there. `agents_api_url` is where `connect` and `mining enable` send
requests. A non-default, non-loopback `base_url` without `agents_api_url` is refused; a
loopback one alone defaults `agents_api_url` to it. Every URL must be https, or http on
loopback.

The `[mining]` block is `tokendrop-proxy`'s, so a machine running that proxy can point both at
one state directory and be one participant. The parser accepts the proxy's `[proxy]`,
`[transport]`, `[privacy]`, `[log]` and `[observe]` sections and `[[provider]]`'s `name` and
`tier`; no `jevlin` command reads them. Unknown keys are an error.

Setup loads an existing config first and refuses one that does not load. One with a `[miner]`
table is left byte for byte. One without gains only the `[platform]` and `[miner]` tables it
lacks, appended, and must load before it is saved.

## Environment variables

| variable | read by | effect |
|---|---|---|
| `JEVLIN_CONFIG` | every command | The config file, after `-config`. Setup's profile block sets it. |
| `JEVLIN_HOME` | every command, setup | This machine's installation directory, instead of `~/.jevlin`. |
| `JEVLIN_API_KEY` | search | The search key, overriding `credentials.json`. |
| `JEVLIN_TRACE` | search, hooks | `off` sends no trace. |
| `JEVLIN_WALLET_DIR` | wallet commands | The wallet directory. Setup's profile block sets it when it made a wallet. |
| `JEVLIN_WALLET_PASSPHRASE` | wallet commands | The keyfile passphrase, for a caller with no terminal. |
| `JEVLIN_WALLET_NODE` | wallet commands | The chain RPC node, like `-node`. |
| `JEVLIN_REWARD_ESCROW` | earnings | Pins the reward escrow address instead of looking it up. |
| `JEVLIN_MINING` | setup | `1`, with no terminal, writes `[mining] enabled = true`. |
| `JEVLIN_PAYOUT_ADDRESS` | setup | With `JEVLIN_MINING=1`, writes `payout_address`. |
| `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL`, `JEVLIN_AGENTS_API_URL`, `JEVLIN_AS_URL`, `JEVLIN_CHAIN`, `JEVLIN_SLOT` | setup | Values a fresh config is written with, instead of the defaults. |
| `JEVLIN_BINARY`, `JEVLIN_SKIP_DOWNLOAD` | npm wrapper | Use a binary already on the machine; install without fetching. |
| `JEVLIN_LAUNCH` | setup, upgrade, uninstall | Set by the npm wrapper, so the binary knows npm owns it. |
| `JEVLIN_TRACE_BRIDGE` | search | The trace envelope an agent's hook put in front of the command. |
| `JEVLIN_HARNESS`, `JEVLIN_LINEAGE`, `JEVLIN_SESSION` | search | Exported by Cursor's hooks: the agent, its lineage file and its hashed session. |
| `JEVLIN_DETACHED` | setup, connect, flush | Marks a background child: one try at the lifecycle gate, then it exits 0. |

The last four are written by the hooks and the binary itself; you do not set them.

## Files and locks

Under `~/.jevlin` (or `JEVLIN_HOME`) on the default layout:

| path | what it is | written by | safe to delete? |
|---|---|---|---|
| `jevlin.toml` | The config. | setup | No. |
| `credentials.json` | The search key, owner-only. Refused if a symlink or readable by others. | connect, login | Only to forget the key: `login -forget`. |
| `search-default` | `agents prefer`'s choice. | agents prefer | Yes; the skill default returns. |
| `bin/jevlin` | A native binary, and `jevlin.previous` after an upgrade. | setup, upgrade | Through `uninstall -binary`. |
| `state/` | Identity, authorization, the mining decision, health records, the flush stamp. | connect, flush | No: it is your registration. |
| `intake/` | Served request ids, until a flush takes them. | search | No: unsent evidence. |
| `spool/` | Evidence waiting for the rewards service to acknowledge it. | flush | No: unsent evidence. |
| `sessions/` | Lineage files, one per workspace or Cursor conversation. | hooks | Yes, with no agent running; the hooks write them again. |
| `wallet/` | `wallet.key` (sealed), `wallet.pub`, `pending_tx.json` during a send. | wallet, setup | Never without the 24 words. |
| `setup-env.json` | Windows only: what setup changed in your user environment. | setup | No; uninstall reverts from it. |

| lock | coordinates | safe to delete? |
|---|---|---|
| `~/.jevlin.lifecycle.lock` | The gate setup, connect, flush and upgrade pass; uninstall holds it, so none starts under it. | Yes, when no jevlin command runs. |
| `setup.lock` | One setup or upgrade at a time. | Yes, when none runs. |
| `state/connect.lock` | One connect at a time. | Yes, when none runs. |
| `flush.lock` | One flush at a time, beside `intake/`. | Yes, when none runs. |
| `bin/<binary>.update.lock` | One upgrade of that binary at a time. | Yes, when none runs. |
| `wallet/wallet.lock` | Key creation, and a send from its journal check to its first broadcast. | Yes, when no wallet command runs. |
| `state/refresh.token.lock` | Rotation of the authorization's refresh token. | Yes, when none runs. |

## Doctor's checks

| check | OK when |
|---|---|
| authorization server | The configured AS answers. |
| enrolled | The AS accepts this installation's authorization. |
| joined this epoch | This installation is in the open epoch. |
| payout address | An address is in force. |
| earning | This epoch has enough verified searches to qualify. |
| intake writable | This process can write where a search records. Probes with one inert non-`.json` file, then removes it. |
| recording | Recorded searches are waiting, queued, or verified at the AS; or nothing ran recently. |
| wallet access | Windows only: nobody but you can read the wallet. |

`NO` is a fact and a successful diagnosis; `UNKNOWN` is the absence of one. `doctor` exits
non-zero only when every check came back `UNKNOWN`. It opens existing state only and creates no
key, wallet or enrollment. With `[miner]` enabled and mining on, it may create the intake
directory when its parent exists. Its AS checks may rotate the refresh token.

`status` and `doctor` also show unresolved health records in three components, `decision`,
`capture` and `flush`, with these reasons: `decision_unreadable`, `intake_unwritable`,
`sandbox_restricted`, `flush_spawn_failed`, `flush_state_unavailable` (a flush could not take
its lock or write its stamp), `auth_state_unavailable`, `submission_failed` and
`spool_backlog`. Stopping mining keeps earlier capture and flush records as previous
degradation.

## The trace

| field | carries |
|---|---|
| `v` | `1`. |
| `harness` | Which agent: `claude-code`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | The agent's ids, hashed with SHA-256 before they leave the machine. |
| `window` | Which context window of the session, after compactions. |
| `seq` | A call counter. |
| `history` | The assistant text before the search, scrubbed of secrets, last 32 KiB. |
| `host_meta` | Agent-specific metadata. |

The text is scrubbed before it is cut, and an entry too large to scrub whole is omitted whole.
Over 48 KiB, `history` is dropped; an envelope still too large is not sent. It travels
base64url-encoded in `JEVLIN_TRACE_BRIDGE`, written in the syntax of the shell that runs the
command, and only inside the search request. With no hook, a search carries a hashed per-shell
identity. The trace is unauthenticated metadata: nothing treats it as proof of origin.
`JEVLIN_TRACE=off` sends none.

## Security notes

### Credentials

`login` reads an sr- key from stdin or `-key-env VAR`, never an argument, checks it against the
router without spending, and writes `credentials.json` as `0600`, as `connect` does. No key ever
goes into a command line or an agent's config.

### Wallet

`wallet send` journals the transaction in `wallet/pending_tx.json` before broadcasting, and
resolves it only from the chain or by `-abandon-pending`. `-node` or `JEVLIN_WALLET_NODE` names
the RPC node; the default is one per chain. It must be https, or http on loopback, unless
`-insecure-node` is passed. The client trusts that node for balances and confirmations; there
is no light-client verification.

On Windows the wallet directory and every file in it carry their own owner-only access list,
set on every write and repaired by setup. On macOS and Linux, file modes cannot tell a
sandboxed agent from you, so the passphrase is what protects the key.

## Building from source

```bash
make build      # bin/jevlin
make verify     # build, test, race, vet and lint (each incl. Windows), vuln, tidy, cross-compile
```

Go 1.25 or newer; the tests also need Node.js (CI uses 22) to run the embedded opencode plugin.
