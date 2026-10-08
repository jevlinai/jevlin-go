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
| `limits` | This key's spend caps and today's spend against the effective ceiling, read from the router. Reading spends nothing; setting caps is not built yet. `-json` for one envelope. |
| `wallet init\|address\|register\|balance\|send` | The wallet setup made, or one you make here. |
| `login`, `enroll`, `payout`, `join`, `provider` | The portal's older manual path. |
| `version`, `help` | The version, and the full usage text. |

Every command takes `-config <file>`. Without it, the config is the first of: `JEVLIN_CONFIG`,
the installation's own (`$JEVLIN_HOME/jevlin.toml`, else `~/.jevlin/jevlin.toml`) when that
file exists, and built-in defaults. A `jevlin.toml` in the working directory is never read
unless you name it, so a cloned repo cannot choose where your keys are sent. `status` and `doctor`
name the file they used. `connect` refuses to run with no config file, and says to run
`jevlin setup`.
`search` refuses a second `-config`, even one naming the same file: the Claude Code allow rule
ends after the installed `-config <file>`, so a second one could otherwise send the key to
another config's router without a prompt.

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
| `providers` | Up to 16 provider names: restrict the fan-out, or reach an extended arm that fires only when named. The router owns the list; an unknown name is its call. |
| `view` | `"full"` (default) or `"merged"`, which drops the per-provider `candidates`. `"merged"` is also sent to the router, which adds its own `merged` and `indexes` to its raw answer; the envelope's `merged` stays this client's own merge. The human form's `-view merged` with `-format json` shows that raw answer. |

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
turn_end       = false                             # default false: report a searched turn's final answer (see The turn end)
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

**Who can open the state directory, on Windows.** `jevlin setup` gives `<home>\state` an access
list that names only you. A `state_dir` that `connect`, `enroll` or `mining enable` creates gets
the same list, with you as its owner, and so does each directory it had to create on the way to
it; if a list cannot be set, what was created is removed rather than left to be opened as it is. A directory that already exists is left as it is by
those commands, and so is any entry another program has added to it, because an agent's sandbox
is meant to reach this directory: nothing refuses a state directory over who else is on its
list. `jevlin setup` is the exception for `<home>\state` and its siblings: it gives them an
owner-only list on every run, which removes such an entry until the sandbox's own setup adds it
again. `doctor`'s `state access` line says whose the directory is and names anyone else the list
admits.

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
| `sessions/` | Lineage files, one per workspace or Cursor conversation; with `turn_end` on, an empty mark per searched turn and a turn's final answer for the moment before it is sent. | hooks | Yes, with no agent running; the hooks write them again. |
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
| state access | Windows only: the state directory and its credentials have an access list and are owned by you or SYSTEM or Administrators. Anyone else the list names is shown, and is `OK`: an agent's sandbox is expected there. |

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
| `harness` | Which agent: `claude-code`, `codex`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | The agent's ids, hashed with SHA-256 before they leave the machine. A subagent has a `session_id` of its own. |
| `parent_session_id` | On a subagent's search only: the hashed `session_id` of the agent that started it. Claude Code, Codex and opencode. |
| `window` | Which context window of the session, after compactions. |
| `seq` | A call counter. |
| `history` | The assistant text before the search, scrubbed of secrets, last 32 KiB. Codex and Hermes send none. |
| `host_meta` | Agent-specific metadata. |

The scrub removes what has a credential's shape (API keys, Stripe secret keys, GitHub and AWS
tokens, JWTs, a password inside a URL), a PEM private key block whole, markers included, quoted
or not (one cut short goes from its BEGIN marker through the lines that still look like a key's
body: base64 and armor headers, indented, blockquoted or not), email addresses, and the
account name in a home path: after `/home/`, `/Users/` or `C:\Users\`, also with every separator
doubled as Python and JSON print a path. A Windows name of up to four words (`C:\Users\Équipe
Données\Documents`) goes whole when the path goes on past it; at the end of a path only its first
word goes. It also removes what has no shape.

A secret known by its name. A name counts when one of its parts is `PASSWORD`, `PASSWD`,
`PASSPHRASE`, `SECRET`, `SECRETS`, `TOKEN`, `CREDENTIAL`, `CREDENTIALS` or `APIKEY` in any letter
case, the parts being what `_`, `-` and `.` separate (`DATABASE_PASSWORD`, `client-secret`,
`db.password`) and, in camelCase, the last word (`accessToken`, `clientSecret`, `.npmrc`'s
`_authToken`; `tokenCount` and `keyName` hold a count and a name and stay). `KEY` and `PASS` count
in a name written in capitals or of two or more parts or words (`API_KEY`, `--api-key`, `apiKey`,
`db_pass`); so `primaryKey=` loses its value too, as `primary_key=` does. Also `PGPASSWORD`,
`MYSQL_PWD`, and `sig` as a URL query parameter (an Azure SAS). A bare `key=…`, `--key=2` or
`pass=2` is left. Its value goes when it is written:

- after `=`, wherever the `=` is, so a secret chained behind another setting goes too
  (`?user=fred&password=…`, `--env=DB_PASSWORD=…`); with spaces or tabs around `=` after
  PowerShell's `$env:NAME` and `${env:NAME}`, and before any quoted value (`password = "…"`);
- after a colon, as YAML and JSON write it: a key at the start of a line, quoted or not, takes
  the rest of the line (`password: …`, `"client_secret": "…",`); elsewhere only where a member of
  an object, a map, a call, a statement or a code span opens (after `{`, `,`, `(`, `[`, `;` or a
  backtick: `user: "app"; password: "…"`, `` `password: "…"` ``), and only a quoted value that
  closes on its own line, and only that string (`{"password":"…","user":"app"}` keeps its user).
  A key and a colon inside a string (`input("Password: ")`) take nothing, and no colon rule
  reads past a line break. A value that is a reference or a placeholder stays:
  `${{ secrets.X }}`, a template expression, `${VAR}`, `$VAR`, `<pad>`, a type name (`string`,
  `String`, `Option<String>`), a size (`1234 bytes`), or a block that only opens (`{`, `[`, `|`).
  A reference that carries a literal is a value and goes: a default, assignment or alternate in an
  expansion (`${VAR:-…}`, `${VAR-…}`, `${VAR:=…}`, `${VAR=…}`, `${VAR:+…}`, and Spring's
  `${db.password:…}` after a plain colon) whose word is not empty and not itself a variable, and
  a template expression that holds a quoted string (`{{ .Values.x | default "…" }}`,
  `default('…')`, `${{ secrets.X || '…' }}`). After a plain colon, a word that starts with a
  digit, a blank or `?` is bash's substring (`${VAR:0:5}`) and stays. `${VAR:?message}` stays: it
  never gives a value, and its word is the message printed when the variable is unset. A variable
  is `$` and capitals, digits and `_` (`$CI_JOB_TOKEN`), or letters and `_` with no digit
  (`$password`), or a name a later step replaced (`$[REDACTED]`); a word that mixes lowercase
  letters and digits after `$` (`$ecret123`) is a value;
- as the word after a long flag whose last word is a secret's (`curl --api-key …`,
  `--password …`, `--clientSecret …`), so `--key-name`, `--secret-id`, `--passphrase-file` and
  `--token-ttl` keep theirs. A last word that names the value's form, `string`, `value` or
  `phrase`, asks the word before it instead (`--secret-string …`, `--secret-value …`,
  `--pass-phrase …`, `--secretString …`; `--value` and `--default-value` keep theirs). Not a
  value: lowercase letters only (`the --password flag`), the flag's own name in capitals
  (`--token TOKEN`), a redirection or pipe (`<`, `>`, `|`), a command's output (`$(…)`), anything
  after a `--with-` or `--no-` switch, or a quote that does not close on its own line (in
  `"mysql --password " + pw` the quote closes a string). A single-dash flag such as `-p` is not
  read.

A quoted value is read as a shell reads a word: to its closing quote, together with quoted parts
and characters joined to it, so PowerShell's `'it''s …'`, POSIX's `'it'\''s …'` and Python's
`"""…"""` go whole. Its first part may run across at most 100 line breaks only when the name
starts its line: after indentation, and after nothing on the line but a run of declaration words
(`export`, `set`, `declare`, `typeset`, `local`, `readonly`, `env`, Dockerfile's `ENV` and
`ARG`, `const`, `let`, `var`, a word of flags such as `-x`), list markers (`-`, `*`, `+`, `1.`)
and quote markers (`>`), and directly after `$`, `$env:` or `${env:`. Only there is the quote
sure to open a value. Anywhere else it closes on its own line, because in `print("password=", pw)` or `"PASSWORD=" + pw` the quote closes a string,
and read as an opening one it would run into the next line's value. A backslash escapes the next
character only when that reading closes the quote on its own line, so `'C:\keys\'` closes where
a POSIX shell closes it; a quote that does not close takes the rest of its line. An unquoted value
goes to whitespace, `&` or `;`, keeping a character a backslash escapes (`correct\ horse`), and a
closing `)`, `]`, `}`, quote, backtick or `,` after it that belongs to the text around it stays. When the
word holding the name opened with a quote that is still open, the value runs to where that
quote closes on its line, or to a `;` or `&` before it (`-e "DB_PASSWORD=correct horse
battery"`, `"Server=db;Password=a b"`). cmd's `set NAME=…` takes the rest of the line where cmd
reads it as a command: at the start of a line, or after `&`, `(`, `|` or a backtick, for a name
with no lowercase letter.

An environment listing: every value in a run of lines naming five or more different variables,
as `NAME=value`, `export`, `declare -x` or `typeset -x`, also behind a list marker or `cat -n`
numbering, and every value in a PowerShell `Name`/`Value` table under its rule of dashes
(`Get-ChildItem Env:`; any hashtable PowerShell prints looks the same and loses its values too),
each row a name, two or more blanks and a value. A name with no value (an empty variable, a
`$null` entry) is a row too, and a line indented exactly to the `Value` column continues the
value before it, as `Format-Table -Wrap` prints one, and goes with it. The first line that is
none of these ends the table: an empty line, a code fence, a sentence, a numbered item.
A line whose value starts with `=` (`requests==2.31.0`), ends with `,` (a keyword argument) or
holds another `NAME=` after a space (a logfmt record) is not part of a listing, and breaks the
run. And the value of `JEVLIN_TRACE_BRIDGE` in a quoted command, as POSIX, PowerShell (`$env:`
and `${env:}`) and cmd write it.

This machine's names: the hostname's first label wherever it stands as a word, and your
account name only where the text uses it as an account. That is the value of `USER`,
`USERNAME`, `LOGNAME` or `SUDO_USER`, directly before `@` (`ssh name@host`, a prompt), and a
home path, including `/mnt/c/Users/` and an account name with a space in it. Anywhere else your
account name is left, because it is often an ordinary word. The account is the one `USERNAME`
names on Windows, `USER` or else `LOGNAME` elsewhere, or else the home directory's name, and is
searched for only when it is ASCII, so that both scrubbers fold its case alike; a non-ASCII name
goes in a home path by the rule above and nowhere else. A generic name such as `root`,
`ubuntu`, `vscode` or `macbook-pro` is left, because it identifies nobody.

It is a filter over text a model wrote, not a guarantee. A secret with no telling name and no
known shape, in ordinary prose, passes, and so do these: `password: …` in the middle of a
sentence; `NAME = value` unquoted outside PowerShell; a value inside a quote that opened before
another word (`echo "export PASSWORD=a b"` keeps `b`); a lowercase cmd `set` name, or a `set`
written after other words, keeps the tail of a value with spaces; a flag's value of lowercase
letters only, or after a flag whose last word is not a secret's (`--auth …`); a flag's quoted
value broken across lines; a quoted value after `=` in the middle of a line that runs across
lines keeps every line after the one its quote opens on, which is the whole value when the quote
ends its line (`docker run -e API_TOKEN="` and a line break); a secret of capitals and digits
after `$` (`password: $ECRET123`), read as a variable; a Spring default that starts with a digit
(`${DB_PASSWORD:1234…}`), read as bash's substring; JSON
escaped inside a quoted string, such as a `curl -d "{\"password\": \"…\"}"` body;
NUL-separated `env -0` output; a PowerShell table row whose name fills its column, and the rows
after a one-word line that follows a table, which reads as a name with no value; and a Windows
name with spaces at the very end of a path keeps all but its first word. In return, a line that
begins `Password: …` loses the rest of the line, a call at a line's start (`apiKey: getKey(),`)
loses its value, a variable named with lowercase letters and digits (`$token2`) or a template
that names another by a quoted string (`{{ include "chart.name" . }}`) after a secret's key
loses it, as does bash's substring with a named offset (`${TOKEN:start}`), `--key-value` and
`--token-string` lose their value whatever it is, a string holding
`; password: ` followed by another quoted string on its line loses the text between them, and
prose that quotes a PEM BEGIN marker and later its END marker loses what stands between them.

Both scrubbers are built to take time linear in their input, so a long run of blanks or a
repeated token in the assistant's text does not hold up the search it precedes; every input
found to take longer is held to a time bound by test.

The text is scrubbed before it is cut, and an entry too large to scrub whole is omitted whole.
Over 48 KiB, `history` is dropped; an envelope still too large is not sent. It travels
base64url-encoded in `JEVLIN_TRACE_BRIDGE`, written in the syntax of the shell that runs the
command, and only inside the search request — and only onto a command where that syntax
actually reaches the search. A loop, a list or a pipeline with the search anywhere but first is
left exactly as written; the search still runs, carrying the hashed per-shell identity instead.
With no hook, a search carries that same per-shell identity: the hostname and the parent shell's
pid, keyed with a random `trace.key` in `state_dir` that `setup` and `connect` create and that is
never sent, so the router cannot guess its way back to the hostname. A search that finds no key
makes one if it can write `state_dir`; that is why a sandbox that denies the write still keeps one
id once setup has made the key. A search with no usable key that cannot make one (`state_dir` is
read-only and holds none, or the key is a link, open to others or not 32 bytes) sends a one-off id,
different on every search, rather than any id the hostname could be recovered from. The hashed
`session_id` is also mirrored as the request's own top-level `session_id` and `X-Session-Id`
header — the router groups quick reformulations by it there, and reads the trajectory from the
envelope; the same identifier in both places, sent only while an envelope rides, and dropped with
the envelope on the one compatibility retry. The trace is unauthenticated metadata: nothing
treats it as proof of origin. `JEVLIN_TRACE=off` sends none.

## The turn end

Off unless `[miner] turn_end = true`. The trace carries what the assistant wrote before a search;
this carries what it concluded. When a turn ends, the agent's end-of-turn hook sends the router's
`POST /v1/turns` one record:

| field | carries |
|---|---|
| `session_id`, `turn_id` | The same hashed ids the turn's searches carried. |
| `harness` | Which agent. |
| `status` | `completed`, `interrupted` or `failed`. |
| `final_text` | The assistant's last message of the turn, scrubbed of secrets, last 32 KiB. Only on a completed turn, and absent when there is no message to send; the status is sent either way. |
| `final_chars`, `truncated` | Its length before the cut, and whether it was cut. |
| `user_text` | What you asked in that turn, scrubbed of secrets, first 32 KiB. Claude Code, Cursor, opencode and Codex. |
| `steps` | The turn in order: what the assistant wrote (scrubbed, capped), and for each tool it called the tool's **name, duration and whether it succeeded**, where the agent records them. Never a tool's input or output. A search of ours is marked by its call id. Claude Code, opencode and Codex in full; under Cursor, the searches only. |
| `model`, `usage` | The model, and the turn's token counts. Claude Code, Cursor, opencode and Codex. |

Only for a turn in which a jevlin search was served: `search` marks the turn after the router
answers, so a search you refused, or one that failed, does not count, and a turn with no served
search sends nothing. Only to the router `miner.router_url` names; without that line nothing is
sent. With it on, what you typed in a searched turn is sent; what your tools read and wrote never is: not a file's contents, not a command, not its output. The model's private reasoning and a subagent's own steps are not sent either. The hook writes the record to an owner-only file in
`sessions/` and a detached `jevlin turn-end` sends it once and deletes the file, sent or not.
`JEVLIN_TRACE=off` turns it off too. Claude Code (2.1.196 or later), Cursor, opencode, and Codex
where its hooks are installed (macOS and Linux); other agents send none. Codex's turn is read from
Codex's own session file when the turn ends; a subagent's turn sends nothing of its own, and the
calls that started and awaited it appear as tool calls. A project set to retain no content keeps the status and not the text.

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
make verify     # build, vet and lint (each incl. Windows), tidy, race, vuln, cross-compile
make quick RUN=TestName   # vet, lint and only the named tests, while you work
```

Go 1.25 or newer; the tests also need Node.js (CI uses 22) to run the embedded opencode plugin.
