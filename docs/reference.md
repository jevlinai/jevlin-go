# Reference

Values to look up. The [guide](guide.md) explains what to do with them; [agents.md](agents.md)
says what is written into each agent.

## Commands

| command | flags | what it does |
|---|---|---|
| `search` | `--stdin`, `-tier fast\|balanced\|max`, `-format json\|model`, `-view merged`, `--fresh`, `-timeout d` (default 1m0s), `-config file` | One web search. `--stdin` for agents ([the request](#the---stdin-request)); `<query words>` for a person ([the human form](#the-human-form-and-powershell)). `--fresh` skips the router's cache, as `"cache": "refresh"` does in a request. |
| `fetch` | `--stdin` (required), `-timeout d` (default 1m0s), `-config file` | The full text of pages a search returned, for agents ([Reading a page in full](#reading-a-page-in-full)). |
| `agents install\|status\|uninstall` | `-client name` (repeatable), `-config file`; `install` and `uninstall` also `-dry-run` and `-yes` | Sets up, reports or removes the skills and hooks of each agent. `-yes` answers **Proceed?**, never a question about a Codex setting of yours. |
| `agents prefer on\|off\|status` | `-config file` | Whether this search or the agent's own is the default, in every agent with a skill. |
| `agents tier fast [preset]\|balanced\|max\|default\|status` | `-config file` | The tier every search carries unless the request names one; `default` unsets it. After `fast`, a preset name picks which fast indexes run, and a preset named alone is taken as `fast <preset>` when the router lists it; `status` lists the router's presets with its estimate for each. |
| `setup` | `-home dir`, `-yes`, `-dry-run`, `-no-profile`, `-no-agents`, `-with id` (repeatable) | Everything after the binary ([guide, Setup](guide.md#setup)). `-yes` answers the profile and agents questions, terminal or not, and **Use it?** only at a terminal; never Codex's **Switch?**. Without `-yes` or a terminal it leaves the profile and agents alone and prints the command for each. `-dry-run` lists every file it would write or move. `-no-profile` leaves the shell profile (Windows: user environment) alone; `-no-agents` skips detection; `-with` sets up `claude`, `codex`, `cursor`, `opencode`, `pi` or `hermes`, found or not. |
| `uninstall` | `-home dir`, `-binary`, `-purge-state`, `-dry-run`, `-yes` | Removes what setup wrote for one installation; `-binary` also its binary, `-purge-state` also its registration, key and config, confirmed by typing the installation path at a terminal, which `-yes` never answers ([guide](guide.md#removing-it-and-coming-back)). |
| `upgrade` | `-version X.Y.Z`, `-rollback`, `-home dir`; `-auto` and `-config file` are the daily check's | Replaces a native binary with a verified release, never an older one; `-rollback` puts back `jevlin.previous` with no network. `-auto` is the daily check the hooks start: a patch release of this copy's own minor is installed, a new minor only recorded ([guide, Upgrading](guide.md#upgrading)). |
| `connect` | `-name name`, `-json`, `-force`, `-config file`; `-resume` is internal | Registers with the search platform, prints the claim link and code, polls until claimed. `-force` overwrites a stored key that belongs to another registration. `-resume` is the one poll a search starts in the background. |
| `login` | `-key-env VAR`, `-no-verify`, `-show`, `-forget`, `-config file` | Stores a key you already have, read from stdin (or `-key-env`) and checked against the router without spending unless `-no-verify`; `-show` says where a search would get its key, `-forget` removes the file. |
| `status` | `-json`, `-config file` | What this installation has and has not completed. |
| `doctor` | `-json`, `-config file` | Whether this installation is ready to search, check by check ([Doctor's checks](#doctors-checks)). |
| `limits` | `-json`, `-config file` | This key's daily, weekly and monthly spend caps and today's spend, ceiling and remainder, read from the router. Reading spends nothing; setting caps is not built yet. |
| `version`, `help` | | The version (`dev` for a source build), and the usage text. |
| `hook`, `turn-end` | | Internal: what the agents' hooks run. |

A command that reads the config takes `-config <file>`; `setup`, `upgrade` and `uninstall` take
`-home <dir>` instead. Without `-config`, the config is the first of: `JEVLIN_CONFIG`, the
installation's own (`$JEVLIN_HOME/jevlin.toml`, else `~/.jevlin/jevlin.toml`) when that file
exists, and built-in defaults. A `jevlin.toml` in the working directory is never read unless you
name it, so a cloned repository cannot choose where your key is sent. `status` and `doctor` name
the file they used; `connect` refuses to run with no config file and says to run `jevlin setup`.
`search` and `fetch` refuse a second `-config`, even one naming the same file: Claude Code's
allow rule ends after the installed `-config <file>`, so a second one could otherwise send the key
to another config's router without a prompt. Before setup has written the installation's own
config, a `-config` naming that path reads as built-in defaults, as no `-config` would.

## Tiers and providers

The tiers, as the hosted service runs them (search-router `deploy/live/searchrouter.toml`); they
are the router's, and change without a client release. What a search cost is in its response's
`usage.cost_micros`; `jevlin agents tier status` lists the presets with the router's estimate.

| tier | what runs | results per arm | merged window | page text | who may use it |
|---|---|---|---|---|---|
| `fast` | The indexes of a preset: `economy` (the default: `serper`, `parallel-fast`) or `standard` (`exa-lite`, `serper`, `parallel-fast`). | the router's default | 12 pages | 1,200 characters | every key |
| `balanced` | Four core arms: `exa`, `brave`, `parallel-search`, `perplexity-search`. | the router's default | 12 pages | 1,200 characters | claimed agents |
| `max` | All seven core arms: those four and `serper`, `telem`, `tinyfish`. | 15 | 24 pages | 2,000 characters | claimed agents |

`parallel-search` and `parallel-fast` return at most 10 results on every tier. A request's own
`max_results`, `merged_limit`, `page_chars` and `max_chars` replace these defaults, and a
`--stdin` search is sent its own when the caller sets none ([the request](#the---stdin-request)).
A tier is also a per-query cost ceiling on the router: a set that would exceed it is trimmed rather
than refused, and `decision.trimmed` names what went.

The hosted service enables nine providers: the seven core arms and two extended ones, `exa-lite`
and `parallel-fast`. A request's `providers` may name any of the nine on `balanced` or `max`; an
extended arm then runs only because it was named. On `fast` the router refuses `providers` with
400 `invalid_request`, and it refuses a name it does not enable as an unknown provider.
`decision` in every response says which arms ran.

## The `--stdin` request

One JSON object on stdin, one JSON envelope on stdout. The query never reaches the process list
and needs no shell escaping. A malformed field answers `fix_input` before any router call; `tier`
and the query's length are not checked here, and the router answers for them.

| field | value |
|---|---|
| `version` | `1`, required. |
| `query` | The search, required, not blank. |
| `tier` | `"fast"`, `"balanced"` or `"max"` ([Tiers and providers](#tiers-and-providers)). Absent, the request carries your tier (`agents tier`, then `[router] tier`), or none, and the router applies your key's default. |
| `recency` | `"day"`, `"week"`, `"month"` or `"year"`. A router that applies it says so in `result.filters_applied`; without that report the results may be unfiltered, so check each page's `published` date if it must hold. |
| `domain_filter` | Up to 16 bare hostnames. Reported in `result.filters_applied` like `recency`; without the report, check each page's `url` host if it must hold. |
| `max_results` | 1 to 25: a cap per arm. |
| `merged_limit` | 1 to 50: how many pages `result.merged` holds. Absent, the router's window applies, except that a `--stdin` search is sent 16 wherever that window would be larger: with `search_queries`, or on `max`. |
| `max_chars` | A positive integer: the characters of page text across the whole merged list, spent in list order, so later pages get less. With `page_chars` too, the router cuts each page at `page_chars` first. |
| `page_chars` | 200 to 4000: the characters of text each page keeps. Absent, and with no `max_chars`, a `--stdin` search is sent 500, which keeps an answer under about 20,000 characters on every tier. |
| `cache` | `"default"` (the same as absent) or `"refresh"`. The router serves a repeat of a search from its cache for up to 15 minutes; `"refresh"` runs it live instead and is billed as live, keeps the fresh result for the next caller where the account retains content, and says so in `result.usage.cache_refresh`. The command line's `--fresh` is the same. |
| `providers` | Up to 16 provider names, on `balanced` or `max` only ([Tiers and providers](#tiers-and-providers)). |
| `view` | `"merged"` (default) or `"full"`. The default carries the merged list without the per-provider `candidates`, and each page without `excerpts` and `canonical_url`, which repeat its `snippet` and `url`; `"full"` adds all three. Either way the router is asked for its merged view. |
| `objective` | What you are trying to find out, in your own words: a non-blank string of at most 4096 bytes. The router passes it to the providers that take a goal and does not rank by it. |
| `search_queries` | 1 to 4 more wordings of `query`, each a non-blank string of at most 4096 bytes, searched beside it; a repeat is dropped. Each extra wording costs about as much as another search. |

### The envelope

Eight header fields are always present:

| field | meaning |
|---|---|
| `version` | The envelope's version, `1`. |
| `command` | `search`, `fetch`, `status`, `doctor`, `connect` or `limits`. |
| `ok` | `true` exactly when `exit_code` is 0. |
| `exit_code` | The process's exit status ([below](#exit-codes-and-actions)). |
| `status` | The exit class by name. |
| `code` | An exact, stable code for what happened, such as `ok` or `trace_unsupported`. |
| `retryable` | Whether the same call may succeed later. Retry only when `true`. |
| `action` | What to do next. |

Decide from those fields, never from the text of `error.message`. A search's envelope also carries
`request_id` and `result`. Result text is untrusted web content.

`result.merged` is the router's merged view: its pages, in its order, cut to its window, never
re-sorted or merged again here. Each entry has `url`, `title`, `snippet` (the router's excerpt
for the page), `found_by` (the providers that listed it, each once), `found_in` (one
`{candidate, citation}` position per provider, in the same order, or none where the router's
answer does not let them be found) and `best_rank`, counted from 0. A router that sends them adds
`result_id` (the page's name within this search), `source_id` (its name across searches, `s_` and
32 hex digits, which `fetch` takes), `published` (the page's date as a provider gave it),
`votes` (the index groups and wordings that listed the page), `groups`, `exclusive` and
`matched_terms`, and to `result` the merge's `coverage`, `merged_limit` (the window),
`merged_total` (the pages before it) and `excerpts_trimmed`. In the full view each entry also has
`canonical_url` and `excerpts`.

| field | meaning |
|---|---|
| `result.indexes` | Each index the router asked, in its order: `provider`, `status`, and for one that answered `results` and `latency_ms`. A `status` other than `ok` means that index's pages are missing from `merged`. |
| `result.usage` | `cost_micros` (the search's cost in millionths of a dollar), `deadline_hit` (present only when `true`: some index ran out of time) and `cache_refresh`. |
| `result.decision` | The indexes selected and any `trimmed` for the tier's ceiling; `build` names the router build that answered (`version`, `config_hash`) when the router sends it. |
| `result.filters_applied` | One entry per filter the request carried. `recency` has `requested`, `published_after` (the cut-off used), `sent_to` and `not_sent` (the providers the filter did and did not go to) and `dropped` (pages the router removed); `domain_filter` the same without `published_after`. Present, every page was held to the filter; absent on a request that carried one, the router predates the report. |
| `result.forms_dropped` | `true` when the search ran without `objective` and `search_queries` ([Bounds](#bounds)). |

An answer with no merged view gets `result.merged: []`, with the candidates as the router sent them
in the full view; the search is still `ok`. `status`, `doctor`, `connect` and `limits` take `-json`
and answer with the same header.

### Exit codes and actions

| exit | `status` | meaning |
|---|---|---|
| 0 | `ok` | A valid answer. |
| 1 | `transport_error` | Transport failure, timeout or cancellation, or a config that cannot be loaded. |
| 2 | `usage_error` | The command or request was wrong. |
| 3 | `client_error` | The server answered 4xx, or no usable key was found. |
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

### The human form and PowerShell

`jevlin search [-tier fast] [-format json|model] <query>` is for a person at a terminal, and puts
the query on the command line. `-format model` prints a bounded summary and never echoes the
router's error body; `-format json` prints the router's bytes verbatim; `-view merged` with
`-format json` adds the router's merged view to them. Neither is the versioned envelope.

Where the shell is PowerShell, a skill pipes a here-string into the call:

```powershell
$OutputEncoding = [System.Text.UTF8Encoding]::new($false)
@'
{"version":1,"query":"what is proof of authority consensus"}
'@ | & 'C:\Users\you\.jevlin\bin\jevlin.exe' search -config 'C:\Users\you\.jevlin\jevlin.toml' --stdin
```

The first line makes an apostrophe, a quotation mark or any non-ASCII character arrive as
written; without it, Windows PowerShell 5.1 turns every non-ASCII character into `?` on the way
to the program, and the search answers a different question.

### Reading a page in full

`jevlin fetch --stdin` takes one JSON object and answers with one envelope carrying the same
header, with `command` set to `fetch`. It sends `POST /v1/fetch` to the configured router with the
same key, deadline, redirect policy and user agent as a search. The router serves only pages from
this account's own recent search results, and only to an account that can fetch.

| field | value |
|---|---|
| `version` | `1`, required. |
| `pages` | 1 to 5 entries, required. Each is exactly one of `{"source_id": "s_…"}`, a page's `source_id` from a search's `result.merged`, or `{"url": "https://…"}`, an absolute http or https URL. |
| `max_chars` | 1000 to 50000: how many characters of each page to return. Absent, the router's default. |
| `offset` | 0 or more: where in each page's text to start. Pass a page's `next_offset` to read on. |
| `query` | Up to 500 characters, not blank: the router starts a long page's text near the part that matches. |

Every malformed value, and any other field, answers `fix_input` before the router is called. On
success, `result.pages` is the router's pages in its order: `source_id`, `url`, `title`, `status`,
`content`, `offset`, `next_offset` (absent at the end of the text), `content_length`, `truncated`
and `full_length` (present when the upstream cut the page), `reason` (on a failed page:
`upstream_failed`, which a later try may fix, or `unrendered`, a page that needs a browser),
`provider`, `cached` and `cost_micros`; `result.usage` has the call's `cost_micros` and
`latency_ms`. A page's `status` is `ok`, `failed`, `invalid`, or `not_recent`: not among this
account's recent results, so use the host's own fetch tool for it. A page that did not come back
is not a failed call: `ok` stays `true`.

| router answer | `code` | `retryable` | `action` |
|---|---|---|---|
| 403 `fetch_unavailable` | `fetch_unavailable` | `false` | `connect`: this account cannot fetch, and `error.message` says why |
| 404 or 405 | `fetch_not_supported` | `false` | `report`: this router has no fetch |
| 503 `fetch_disabled` | `fetch_disabled` | `false` | `report`: this router has fetch switched off |
| 502, or any other 5xx (another 503 too) | the router's code, else `router_error` | `true` | `retry` |
| 401 | the router's code, else `unauthorized` | `false` | `login` |
| any other status | as for a search | | |

A fetch is one request and is never retried by the client.

### Bounds

`-timeout`, default 1m0s, bounds the whole search: connect, TLS, headers, body and any second
request. A search sends a second request in three cases, each once and under the same deadline:

- the router answers the exact code `trace_unsupported`: the search is sent again without the
  trace;
- a request carrying `objective` or `search_queries` is answered `malformed_json`, by a router that
  predates them or whose body limit they push it past: it is sent again without them, and
  `result.forms_dropped` is `true`;
- a fast search with a stored preset first asks `GET /v1/fast-sets` (within 2 seconds) whether the
  router lists that preset, and sends it as `fast_set` only if so.

## Config

The file is configuration format 1. It begins `schema = 1`, and every section and key after that
line is optional: the value after `#` is what you get by leaving the key out. A fresh
`jevlin setup` writes the schema line, the three endpoint sections and `[search]`'s two
directories, nothing else.

```toml
schema = 1

[router]
url  = "https://api.jevlin.ai"       # where searches, fetches, limits reads and turn ends go
tier = ""                            # fast | balanced | max; unset = the router's default. `agents tier` outranks it; another word is not sent and is named by `agents tier status`

[platform]
url = "https://platform.jevlin.ai"   # the human portal and claim pages; compared against, never dialed

[agents]
url = "https://agents-v1.jevlin.ai"  # where connect sends register and status, a separate host

[search]
state_dir    = "/home/you/.jevlin/state"      # default: <user config dir>/jevlin/state
sessions_dir = "/home/you/.jevlin/sessions"   # per-workspace lineage files; default: beside state_dir
turn_end     = false                          # report a searched turn (see The turn end)

[update]
mode = "apply"                                # apply | notify | off; the daily release check. Another word checks and notifies only, and doctor names it
```

Every URL must be https, or http on loopback, with an ASCII host and no user, query or fragment;
an error names the key (`router.url`, `platform.url`, `agents.url`). `[router] url` is the one
router this installation talks to; there is no other address a search falls back to.
`[platform] url` is never dialed: a printed claim link must point there, at exactly that origin.
A non-default, non-loopback `[platform] url` without `[agents] url` is refused; a loopback one
alone defaults `[agents] url` to it. `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL` and
`JEVLIN_AGENTS_API_URL` replace their URL for a run, after the file and under the same rules; setup
also writes them into a fresh config.

A `schema = 1` file holding any other key or section is refused by name
(`format 1 does not define X; correct or remove it`). A file with no `schema` line that still reads
as format 1 (a key spelled like `schema` in another case or a letter or two off, or a `[router]`
or `[agents]` table, or a `[platform] url`, none of which an older jevlin wrote) is refused saying
the `schema = 1` line is missing. Any other file with no `schema` line is one an older jevlin
wrote, and every command refuses it with "this configuration is from an older jevlin; run
`jevlin setup`" ([guide, Moving from 0.6.x](guide.md#moving-from-06x)). A file naming another
schema number is refused with a message saying which schema this jevlin reads.

## Environment variables

| variable | read by | effect |
|---|---|---|
| `JEVLIN_CONFIG` | every command | The config file, after `-config`. Setup's profile block sets it. |
| `JEVLIN_HOME` | every command, setup | This machine's installation directory, instead of `~/.jevlin`. |
| `JEVLIN_API_KEY` | search, limits, the turn end, fetch | The search key, overriding `credentials.json`. With a `-config` that is not the installation's own file, it is sent only when that config names the installation's router; to use another router, set `JEVLIN_CONFIG` to its config. |
| `JEVLIN_TRACE` | search, hooks | `off` turns the trace and the turn end off entirely: the hooks write no bridge and no lineage file, and search sends none. |
| `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL`, `JEVLIN_AGENTS_API_URL` | every command, setup | Each replaces its URL for the run ([Config](#config)). |
| `JEVLIN_BINARY`, `JEVLIN_SKIP_DOWNLOAD` | npm wrapper | Use a binary already on the machine; install without fetching. |
| `JEVLIN_LAUNCH` | setup, upgrade, uninstall, doctor | Set by the npm wrapper, so the binary knows npm owns it. You do not set it. |
| `JEVLIN_TRACE_BRIDGE` | search | The trace envelope an agent's hook put in front of the command. Written by the hooks. |
| `JEVLIN_HARNESS`, `JEVLIN_LINEAGE`, `JEVLIN_SESSION` | search | Exported by Cursor's hooks: the agent, its lineage file and its hashed session. |

## Files and locks

Under `~/.jevlin` (or `JEVLIN_HOME`) on the default layout:

| path | what it is | written by | safe to delete? |
|---|---|---|---|
| `jevlin.toml` | The config. | setup | No. |
| `credentials.json` | The search key, owner-only, and the router and agents API it is sent to. Refused if a symlink or readable by others. | connect, login | Only to forget the key: `login -forget`. |
| `registration_pending.json` | A registration the platform answered that is not yet saved; finished by the next connect. | connect | No: it holds the new key. |
| `claim.json` | The claim link and code for this installation's agent, while it is unclaimed. | connect | Yes; the next `jevlin connect` mints a fresh link, which ends the old one. |
| `search-default` | `agents prefer`'s choice. | agents prefer | Yes; the skill's default returns at the next install or upgrade that renders it. |
| `search-tier` | `agents tier`'s choice. | agents tier | Yes; `[router] tier`, then the router's default, returns. |
| `search-fast-set` | `agents tier fast <preset>`'s choice, sent as `fast_set`. The skill never sets it. | agents tier | Yes; `agents tier default` removes it. |
| `requests/` | Claude Code only: a search or fetch request holding a `'` or a `$`, written by the agent with its Write tool and read by the call with `--stdin < <file>`. Nothing prunes the folder; `uninstall -purge-state` removes it. | the agent | Yes, with no search running. |
| `update_check.json` | The daily release check's record (below). | upgrade | Yes; the next agent session checks again, though a rollback's hold is forgotten. |
| `bin/jevlin` | A native binary from the installer, and `jevlin.previous` after an upgrade. Setup never writes it. | the installer, upgrade | Through `uninstall -binary`. |
| `state/` | The agent registration (`agent.json`), the trace key (`trace.key`), and connect's lock and resume stamp. | setup, connect, search | No: it is your registration. |
| `sessions/` | Lineage files, one per workspace or Cursor conversation, and the hooks' window state; with `turn_end` on, an empty mark per searched turn and a turn's record until it is sent. | hooks, search | Yes, with no agent running; the hooks write them again. |
| `setup-env.json` | Windows only: what setup changed in your user environment. | setup | No; uninstall reverts from it. |

`credentials.json`, `registration_pending.json`, `claim.json` and `update_check.json` sit in the
installation directory, the state directory's parent, which no agent's sandbox may write. On
Windows, `jevlin setup` gives `<home>` and the directories it keeps under it (`state`, `sessions`
and the like) an access list naming only you, on every run; a state directory elsewhere that
setup or connect creates gets the same. An existing one is otherwise left with the list it has,
because an agent's sandbox is meant to reach it; `doctor`'s `state access` names who else is on
it.

| lock | coordinates | safe to delete? |
|---|---|---|
| `~/.jevlin.lifecycle.lock` | The gate setup and connect pass, and that upgrade and uninstall hold while they run. | Yes, when no jevlin command runs. |
| `setup.lock` | One setup or upgrade at a time. | Yes, when none runs. |
| `state/connect.lock` | One connect at a time. | Yes, when none runs. |
| `bin/<binary>.update.lock` | One upgrade of that binary at a time. | Yes, when none runs. |
| `update_check.lock` | One release check at a time for the installation, whichever copy runs it. | Yes, when none runs. |

The daily release check is started by Claude Code's session start and stop, Cursor's session
start, stop and end, Codex's session start and stop (macOS and Linux), opencode's plugin and Pi's
extension; Hermes and an agent set up by hand start none, and no hook running inside an agent's
sandbox starts one. The hook starts `jevlin upgrade -auto` as a detached child when no check has
run in the last day, and contacts nothing itself. The child records the check before it touches
the network and asks the project's GitHub releases for the latest version, with no credential.
`update_check.json`'s outcome is one of:

| outcome | meaning |
|---|---|
| `started` | A check is running. |
| `current` | This copy is the latest release. |
| `available` | A newer release exists, not installed: under `notify`, for an npm copy, or a new minor. |
| `upgraded` | A patch release was installed; with `refresh = "failed"` the agent files could not be rewritten afterwards. |
| `rolled_back` | `upgrade -rollback` held the release it left: no check installs it, or an older one, again; `jevlin upgrade` by hand lifts the hold. |
| `failed` | The check failed, with the word `upgrade` would print; `manual_intervention` is never overwritten by a check, and lists the copies left beside the binary. |
| `not_release` | A development build, which no check replaces. |

## Doctor's checks

In the order `doctor` prints them:

| check | OK when |
|---|---|
| search key | `credentials.json` holds a key for this installation's router. `NO` when there is none, or the file cannot be used (a link, or readable by others); a search would then need `JEVLIN_API_KEY`. The key's value is never shown. |
| registration | This agent is claimed on the platform, or not registered at all, since a key from `jevlin login` searches without one. `NO` for a registration not yet claimed, one that expired, or a record that cannot be read; the fix is `jevlin connect`. |
| trace key | The state directory holds `trace.key`, or does not hold it yet (the first search that can write the state directory makes it). Only a key that cannot be read is `NO`. |
| up to date | The daily check found this copy current, has not run yet, is turned off, rolled back by you, or finds a development build. `NO` when a newer release is available (a new minor included), when an upgrade could not rewrite the agent files, or when an upgrade could not put the old binary back. `UNKNOWN` when the last check failed, is still running, could not be read, is dated in the future, or ran more than three days ago. |
| binary on PATH | A bare `jevlin` runs this installation's own binary. `NO` names the copy that answers instead, or says nothing on PATH answers. Checked when the installation keeps a binary at its `bin`; when it keeps none there and was not launched through npm, the running binary stands in. |
| npm command on PATH | An npm install only: a bare `jevlin` runs this package's npm command. `NO` when nothing answers, when npm's global `bin` directory is not on PATH, or when another copy stands earlier on it. |
| state access | Windows only: the state directory and its credentials have an access list and an owner, who is you, SYSTEM or Administrators. Anyone else the list names is shown, and is `OK`: an agent's sandbox is expected there. |

`NO` is a fact and a successful diagnosis; `UNKNOWN` is the absence of one. `doctor` exits 0 unless
every check came back `UNKNOWN` (exit 1), the config cannot be loaded (exit 1), or a flag is wrong
(exit 2). Every check is local: `up to date` reads the record the daily check left. It opens
existing state only and creates nothing.

## The trace

| field | carries |
|---|---|
| `v` | `1`. |
| `harness` | Which agent: `claude-code`, `codex`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | The agent's ids, hashed with SHA-256 before they leave the machine. A subagent has a `session_id` of its own. |
| `parent_session_id` | On a subagent's search only: the hashed `session_id` of the agent that started it. Claude Code, Codex and opencode. |
| `window` | Which context window of the session the search was made in, counting compactions. |
| `seq` | A counter of the searches recorded in the lineage file (one per workspace; in Cursor, per conversation). |
| `history` | The assistant text that led to the search, scrubbed, last 32 KiB: from Claude Code an earlier message of the turn, from Cursor its latest thought or reply, from opencode and Pi the text before the search, from Codex and Hermes none. |

The text is scrubbed before it is cut, and an entry too large to scrub whole is omitted whole.
Over 48 KiB, `history` is dropped; an envelope still too large is not sent. Where a hook writes
it, the envelope travels base64url-encoded in `JEVLIN_TRACE_BRIDGE`, written in the syntax of the
shell that runs the command: in PowerShell in front of any command, in a POSIX shell only when
the command's first simple command is the search, so a loop, a list or a pipeline with the search
anywhere else is left exactly as written. Cursor's hooks write a lineage file instead
(`JEVLIN_LINEAGE`). A search with no trace carries a per-shell identity: the hostname and the
parent shell's pid, keyed with the random `trace.key` in `state_dir`, which is never sent; with no
usable key and none it can make, a one-off id. The hashed `session_id` is also sent as the
request's top-level `session_id` and its `X-Session-Id` header while an envelope rides. The trace
is unauthenticated metadata: nothing treats it as proof of origin. `JEVLIN_TRACE=off` sends none.

### What the scrub removes

| kind | removed |
|---|---|
| A credential's shape | API keys, Stripe secret keys, GitHub and AWS tokens, JWTs, a password inside a URL; a PEM private key block whole, markers included (one cut short from its BEGIN marker through the lines that still look like a key's body); email addresses. |
| An `Authorization` or `Proxy-Authorization` header | The credential after `Bearer`, `Basic` or `Token`, whatever its shape, in a header line, `curl -H '…'`, or a JSON or JavaScript key. A variable (`$TOKEN`, `${TOKEN}`) or a placeholder (`<token>`) stays. |
| A secret known by its name | The value of a name with a part `PASSWORD`, `PASSWD`, `PASSPHRASE`, `SECRET`, `SECRETS`, `TOKEN`, `CREDENTIAL`, `CREDENTIALS` or `APIKEY` in any case, the parts split by `_`, `-` and `.` and, in camelCase, the last word (`DATABASE_PASSWORD`, `client-secret`, `accessToken`; `tokenCount` stays). `KEY` and `PASS` count in a name in capitals or of two or more parts (`API_KEY`, `--api-key`, `db_pass`). Also `PGPASSWORD`, `MYSQL_PWD`, and `sig` as a URL query parameter. A bare `key=…` or `pass=…` is left. |
| An environment listing | Every value in a run of lines naming five or more variables (`NAME=value`, `export`, `declare -x`, `typeset -x`), and every value in a PowerShell `Name`/`Value` table (`Get-ChildItem Env:`, and any hashtable PowerShell prints the same way). |
| The bridge | The value of `JEVLIN_TRACE_BRIDGE` in a quoted command, in POSIX, PowerShell and cmd syntax. |
| This machine's names | The hostname's first label wherever it stands as a word; your account name where the text uses it as an account: the value of `USER`, `USERNAME`, `LOGNAME` or `SUDO_USER`, before `@`, in a home path (`/home/`, `/Users/`, `C:\Users\`, `/mnt/c/Users/`, also with separators doubled) and in Claude Code's dashed project folder name. A generic name such as `root` or `ubuntu` is left. |

A secret's value is taken where it is written:

| written | the value |
|---|---|
| after `=` | Wherever the `=` is (`?user=fred&password=…`, `--env=DB_PASSWORD=…`); with blanks around `=` after PowerShell's `$env:` and before a quoted value. |
| after `:` | A key at the start of a line takes the rest of the line (`password: …`); elsewhere only where an object, map, call, statement or code span opens, and only a quoted value that closes on its line. A key inside a string (`input("Password: ")`) takes nothing. |
| after a long flag | The next word, when the flag's last word is a secret's (`--api-key …`, `--password …`); `--key-name`, `--secret-id` and `--token-ttl` keep theirs. A single-dash flag is not read. |
| quoted | To the closing quote, as a shell reads a word; across line breaks only where the name starts its line. A quote that does not close takes the rest of its line. |
| unquoted | To whitespace, `&` or `;`. cmd's `set NAME=…` takes the rest of the line. |

A value that is a reference or a placeholder stays: `${{ secrets.X }}`, a template expression,
`${VAR}`, `$VAR`, `<pad>`, a type name, a size, or a block that only opens (`{`, `[`, `|`). A
reference that carries a literal default (`${VAR:-…}`, `{{ … | default "…" }}`) is a value and
goes.

It is a filter over text a model wrote, not a guarantee. A secret with no telling name and no
known shape passes, and so do, among others: `password: …` in the middle of a sentence;
`NAME = value` unquoted outside PowerShell; JSON escaped inside a quoted string; `env -0` output;
a quoted value broken across lines after a flag; a value Windows PowerShell 5.1 re-quoted as a
native argument. It also takes more than a secret in places: a line that begins `Password: …`
loses the rest of the line, a call at a line's start after a secret's key (`apiKey: getKey(),`)
loses its value, and a one-word line just after a PowerShell table reads as a name with no value,
so the table does not end there and a later line shaped like a row loses its value. Both scrubbers
take time linear in their input, held to a bound by test.

## The turn end

Off unless `[search] turn_end = true`. When a turn in which a jevlin search was served ends, the
agent's end-of-turn hook sends the router's `POST /v1/turns` one record:

| field | carries |
|---|---|
| `session_id`, `turn_id` | The same hashed ids the turn's searches carried. |
| `harness` | Which agent. |
| `status` | `completed`, `interrupted` or `failed`. |
| `final_text` | The assistant's last message of the turn, scrubbed, last 32 KiB. Only on a completed turn, and absent when there is no message to send. |
| `final_chars`, `truncated` | Its length before the cut, and whether it was cut. |
| `user_text` | What you asked in that turn, scrubbed, first 32 KiB. Absent for a turn Claude Code started rather than you (a continuation, a background task's result, the output of a `!` command, a compaction summary). |
| `steps` | The turn in order: what the assistant wrote (scrubbed, capped), and each tool's **name, duration and whether it succeeded**. Never a tool's input or output. A search of ours is marked by its call id. Under Cursor, the searches only. |
| `model`, `usage` | The model, and the turn's token counts. |

Which agents send one, and what each adds, is in the [guide](guide.md#what-leaves-your-machine):
Claude Code (2.1.196 or later), Cursor, opencode, Pi (0.80.4 or later) and Codex where its hooks
are installed (macOS and Linux); Hermes and other agents send none. A search marks its turn only
after the router answers, so a refused or failed search does not count. A turn is reported once.
Under Claude Code, a subagent searches under the turn that launched it, and a background one can
do so after that turn ended; that turn is then reported late, as completed, by the next turn end in
the session. Under Codex and opencode a subagent runs a turn of its own, joined to no turn end. The
record goes only to the router this run is configured with. The hook writes it to an owner-only
file in `sessions/`, and a detached `jevlin turn-end` sends it once and deletes the file, sent or
not. The model's private reasoning and a subagent's own steps are never sent. `JEVLIN_TRACE=off`
turns it off too.

## Security notes

### Credentials

`login` reads an sr- key from stdin or `-key-env VAR`, never an argument, checks it against the
router without spending, and writes `credentials.json` as `0600`, as `connect` does. No key ever
goes into a command line or an agent's config.

The stored key is sent only to the router and the agents API recorded for it when `connect` stored
it, whichever config a command is given. `connect` records both origins (scheme, host and port) in
`credentials.json`, from the registration it finished, and `login` the router its config names. A
file an earlier version wrote without them belongs to the origins of the installation's own config
(`JEVLIN_CONFIG`, else `$JEVLIN_HOME/jevlin.toml` or `~/.jevlin/jevlin.toml`), never of a
`-config` that names another file, and only a `connect` run under the installation's own config
writes them down: if you run with another config, run `connect` once with `JEVLIN_CONFIG` set to
it. A command whose config names another origin stops before any request, with
`key_origin_mismatch` and `fix_input` in machine mode, naming both origins.

The profile `agents install` writes for Codex makes the state and sessions directories writable
and nothing else, so a sandboxed command can read the key, as any process running as you can, but
cannot replace it, finish a registration or change where a claim link points.

## Building from source

```bash
make build      # bin/jevlin
make verify     # build, vet and lint (each incl. Windows), tidy, race, vuln, cross-compile
make quick RUN=TestName   # vet, lint and only the named tests, while you work
```

Go 1.27 or newer; the tests also need Node.js (CI uses 22) to run the embedded opencode plugin.
Without a clone, `go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest` builds the latest
release's source into `$(go env GOPATH)/bin`, or `GOBIN` when it is set.

A source build reports its version as `dev` (`make build` adds the commit), so it is never
replaced: the daily check records `not_release`, and `jevlin upgrade` refuses it. For updates,
install a release archive or the npm package instead. Run `jevlin setup` from where the binary
will stay, because your agents' skills and hooks name its path.
