# Reference

Values to look up. Every flag is in `jevlin help` and `jevlin <command> -h`.

## Commands

| command | what it does |
|---|---|
| `setup` | Everything after the binary: previous installation, config, `connect`, profile, agents. |
| `uninstall` | Removes what setup wrote for one installation; `-binary` and `-purge-state` go further. |
| `upgrade` | Replaces a native binary with a verified release; `-rollback` swaps back. `-auto` is the daily check the hooks start, not a command a person runs. |
| `search` | One web search. `--stdin` for agents (JSON in, JSON out); `<query words>` for a person. |
| `fetch --stdin` | The full text of pages a search returned, for agents (JSON in, JSON out). See [Reading a page in full](#reading-a-page-in-full). |
| `agents install\|status\|uninstall` | Sets up, reports or removes the skills and hooks of each agent. |
| `agents prefer on\|off\|status` | Whether this search or the agent's own is the default. |
| `agents tier fast [preset]\|balanced\|max\|default\|status` | The tier every search carries unless the request names one; `default` unsets it. After `fast`, a preset name (`economy`, `standard`) picks which fast indexes run, and a preset named alone is taken as `fast <preset>` when the router lists it; `status` lists the router's presets with their estimated price per search. |
| `connect` | Registers with the search platform, prints the claim link, polls until claimed. `-json` for one envelope. |
| `login` | Stores a key you already have for searches, read from stdin or `-key-env VAR` and checked against the router first; `-show` says where a search would get its key, `-forget` removes the file. |
| `status` | What this installation has and has not completed. `-json` for one envelope. |
| `doctor` | Whether this installation is ready to search, check by check. `-json` for one envelope. |
| `limits` | This key's spend caps and today's spend against the effective ceiling, read from the router. Reading spends nothing; setting caps is not built yet. `-json` for one envelope. |
| `version`, `help` | The version, and the full usage text. |

A command that reads the config takes `-config <file>`; `setup`, `upgrade` and `uninstall` take
`-home <dir>` instead. Without
`-config`, the config is the first of: `JEVLIN_CONFIG`, the installation's own
(`$JEVLIN_HOME/jevlin.toml`, else `~/.jevlin/jevlin.toml`) when that file exists, and built-in
defaults. A `jevlin.toml` in the working directory is never read
unless you name it, so a cloned repo cannot choose where your keys are sent. `status` and `doctor`
name the file they used. `connect` refuses to run with no config file, and says to run
`jevlin setup`.
`search` and `fetch` refuse a second `-config`, even one naming the same file: the Claude Code
allow rule ends after the installed `-config <file>`, so a second one could otherwise send the
key to another config's router without a prompt. Every command `agents install` renders names a
config; before setup has written the installation's own (`$JEVLIN_HOME/jevlin.toml`, else
`~/.jevlin/jevlin.toml`), a `-config` naming that path reads as built-in defaults, exactly as
no `-config` would.

## The `--stdin` request

One JSON object on stdin, one JSON envelope on stdout. The query never reaches the process
list and needs no shell escaping. A malformed field answers `fix_input` before any router call.

| field | value |
|---|---|
| `version` | `1`, required. |
| `query` | The search, required. |
| `tier` | `"fast"` (the fast indexes of the stored preset: `economy` asks two, ~$0.0013, `standard` three, ~$0.0053), `"balanced"` (the core arms, 8 results each, a 12-page merged window, ~$0.022) or `"max"` (the core arms, 15 results each, a 24-page window, 2,000-character excerpts, ~$0.027 plus the agent's own tokens). Prices are the arms' own, passed through; `usage.cost_micros` is the real one. Absent, the request carries the participant's tier (`agents tier`, then `[router] tier`), or no tier at all and the router applies the tenant's default. |
| `recency` | `"day"`, `"week"`, `"month"` or `"year"`. A router that applies it says so in `result.filters_applied` (below). Without that report the results may be unfiltered: check each page's `published` date if it must hold. |
| `domain_filter` | Up to 16 bare hostnames. Reported in `result.filters_applied` like `recency`; without the report, check each page's `url` host if it must hold. |
| `max_results` | 1 to 25. A cap. |
| `merged_limit` | 1 to 50: how many pages `result.merged` holds. Absent, the router's window applies (12 pages on `fast` and `balanced`, 24 on `max`, for each form when `search_queries` are sent), except that a `--stdin` search is sent 16 wherever that window would be larger: with `search_queries`, or on `max`. 16 is also the number the skill teaches beside `search_queries`. |
| `max_chars` | A positive integer: the characters of page text across the whole merged list, spent in list order, so later pages get less. With `page_chars` too, the router cuts each page at `page_chars` and then spends `max_chars` across the list; with `max_chars` alone, no `page_chars` is added. |
| `page_chars` | 200 to 4000: the characters of excerpt each page keeps. Absent, and with no `max_chars`, a `--stdin` search is sent 500, which with the window above keeps it under about 20,000 characters on every tier. |
| `cache` | `"default"` or `"refresh"`; absent is `"default"`. The router serves a repeat of a search from its cache for up to 15 minutes. `"refresh"` skips that read, runs the search live, bills it as live and keeps the fresh result for the next caller; the answer says so in `result.usage.cache_refresh` (present only when `true`). For live news and anything a result that old would get wrong, never as a default. From the command line the same is `jevlin search --fresh`, which a `--stdin` search does not take. Any other value is `fix_input`. |
| `providers` | Up to 16 provider names: restrict the fan-out, or reach an extended arm that fires only when named. The router owns the list; an unknown name is its call. Needs `balanced` or `max`: on `fast` the router answers 400 `invalid_request`. Billed at the named arms' prices. |
| `view` | `"merged"` (default) or `"full"`. Absent or `"merged"`, the envelope carries the merged list without the per-provider `candidates`, and each page without `excerpts` and `canonical_url`, which repeat its `snippet` and `url`. `"full"` adds all three, for the providers' own answer texts. Either way the router is asked for its merged view, and the envelope's `merged` is that list. The human form's `-view merged` with `-format json` shows the router's raw answer with it. |
| `objective` | What you are trying to find out, in your own words: a non-blank string of at most 4096 bytes. The router passes it to the providers that take a goal and does not rank by it. |
| `search_queries` | 1 to 4 more wordings of `query`, each a non-blank string of at most 4096 bytes, which the router searches beside it; it drops a repeat. Each extra wording costs about as much as another search. A router that predates `objective` and `search_queries` refuses a request carrying them as `malformed_json`, and so does one whose body limit they push the request past: the search is then sent once more without them, and `result.forms_dropped` is `true`. |

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
carries `request_id` and `result`. Result text is untrusted web content.

`result.merged` is the router's merged view: its pages, in its order, cut to its window, never
re-sorted or merged again here. Each entry has `url`, `title`, `snippet` (the router's excerpt
for the page), `found_by` (the providers that listed it, each once), `found_in` (one
`{candidate, citation}` position per provider, in the same order, or none where the router's
answer does not let them be found) and `best_rank`, counted from 0. A router that sends them
adds `result_id` (the page's name within this search), `source_id` (its name across searches,
`s_` and 32 hex digits: the same page found twice has the same one, and `fetch` takes it),
`published` (the page's date as a
provider gave it, absent when none did), `votes` (the index groups and wordings that listed the
page), `groups`, `exclusive` and `matched_terms`, and to `result` the merge's `coverage`,
`merged_limit` (the window), `merged_total` (the pages before it) and `excerpts_trimmed`. In the
full view each entry also has `canonical_url` and `excerpts`.

`result.indexes` is the router's account of each index it asked, in its order: `provider`,
`status`, and for one that answered `results` and `latency_ms`. A `status` other than `ok`, or
`result.usage.deadline_hit` (present only when `true`), means the search came back degraded, and
pages are missing from `merged`. Both are absent when the router sends no account of them.
`result.filters_applied` is the router's report on the request's filters, field for field, and
has one entry per filter the request carried. `recency` has `requested`, `published_after` (the
cut-off the router used), `sent_to` and `not_sent` (the providers it did and did not pass the
filter to) and `dropped` (the pages the router itself removed for falling outside it);
`domain_filter` has the same but `published_after`. Present, it says every page in the answer
was held to the filter. Absent on a request that carried one, the router predates the report and
the pages may not have been. `result.decision.build` names the router build that answered, its
`version` and `config_hash`, when the router sends it.
`result.forms_dropped` is `true` when the search ran without `objective` and `search_queries`
(above). An answer with no merged view gets `result.merged: []`, with the candidates as the
router sent them in the full view; the search is still `ok`.

`status`, `doctor`, `connect` and `limits` take `-json` and answer with the same header.

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

### Reading a page in full

`jevlin fetch --stdin` takes one JSON object and answers with one envelope carrying the same
header as a search's, with `command` set to `fetch`. It sends `POST /v1/fetch` to the configured
router with the same key, deadline, redirect policy and user agent as a search. The router
serves only pages from this account's own recent search results, and only to an account that
can fetch.

| field | value |
|---|---|
| `version` | `1`, required. |
| `pages` | 1 to 5 entries, required. Each is exactly one of `{"source_id": "s_…"}`, a page's `source_id` from a search's `result.merged` (`s_` and 32 lowercase hex digits), or `{"url": "https://…"}`, an absolute http or https URL. |
| `max_chars` | 1000 to 50000: how many characters of each page to return. Absent, the router's default. |
| `offset` | 0 or more: where in each page's text to start. Pass a page's `next_offset` to read on. |
| `query` | Up to 500 characters, not blank: the router starts a long page's text near the part that matches. |

Every malformed value, and any other field, answers `fix_input` before the router is called. The
router receives the request without `version`, and without any option left out.

On success, `result.pages` is the router's pages as it sent them, in its order: `source_id`,
`url`, `title`, `status`, `content` (the page text, as the router returned it), `offset`,
`next_offset` (where the next slice starts, absent at the end of the text), `content_length`
(the characters of text that can be paged through), `truncated` and `full_length` (present when
the upstream cut the page: `truncated` is `true` and `full_length` is the whole page's length,
more than the router can return), `reason` (on a failed page: `upstream_failed`, which a later
try may fix, or `unrendered`, a page that needs a browser, which asking again will not),
`provider`, `cached` and `cost_micros`. `result.usage` has the
call's `cost_micros` and `latency_ms`, and `request_id` is the router's. A page's `status` is
`ok`, `failed`, `invalid`, or `not_recent`: the page was not among this account's recent Jevlin
results, so fetch only pages a search returned and use the host's own fetch tool for any other
URL. A page that did not come back is not a failed call: `ok` stays `true`.

| router answer | `code` | `retryable` | `action` |
|---|---|---|---|
| 403 `fetch_unavailable` | `fetch_unavailable` | `false` | `connect`: this account cannot fetch, and `error.message` says why (not claimed, zero retention, or no credit); use the host's own fetch tool |
| 404 or 405 | `fetch_not_supported` | `false` | `report`: this router has no fetch; use the host's own fetch tool |
| 503 `fetch_disabled` | `fetch_disabled` | `false` | `report`: this router has fetch switched off; use the host's own fetch tool |
| 502, or any other 5xx (another 503 too) | the router's code, else `router_error` | `true` | `retry` |
| 401 | the router's code, else `unauthorized` | `false` | `login` |
| any other status | as for a search | | |

A fetch is one request and is never retried by the client.

### Bounds

`-timeout`, default 60s, bounds the whole search: connect, TLS, headers, body and the one
retry. If the router answers the exact code `trace_unsupported`, the client retries once
without the trace, under the same deadline. Nothing else causes a second request.

## Config

The file is configuration format 1. It begins `schema = 1`, and every section and key after
that line is optional: the value after `#` is what you get by leaving the key out. A fresh
`jevlin setup` writes the schema line, the three endpoint sections and `[search]`'s two
directories, nothing else.

```toml
schema = 1

[router]
url  = "https://api.jevlin.ai"       # where searches, fetches, limits reads and turn ends go; https only
tier = ""                            # fast | balanced | max; unset = the router's default. `agents tier` outranks it; another word is not sent and is named by `agents tier status`

[platform]
url = "https://platform.jevlin.ai"   # the human portal and claim pages; compared against, never dialed

[agents]
url = "https://agents-v1.jevlin.ai"  # where connect sends register and status, a separate host

[search]
state_dir    = "/home/you/.jevlin/state"      # default: <user config dir>/jevlin/state
sessions_dir = "/home/you/.jevlin/sessions"   # per-workspace lineage files; default: beside state_dir
turn_end     = false                          # default false: report a searched turn's final answer (see The turn end)

[update]
mode = "apply"                                # apply | notify | off; the daily release check (see Files and locks). Another word checks and notifies only, and doctor names it
```

`[router] url` is the one router this installation talks to; there is no other address a search
falls back to. `[search]` names the two directories this installation keeps and the turn-end
switch; `[router] tier` is the tier a search carries when neither the request nor `agents tier`
names one.

`JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL` and `JEVLIN_AGENTS_API_URL` are read by every command,
after the file: each replaces its URL for that run, under the same rules as the file's. Setup also
writes them into a fresh config, so an installation set up with them keeps them. `JEVLIN_CONFIG`
still chooses the file.

**Who can open the state directory, on Windows.** `jevlin setup` gives `<home>\state` an access
list that names only you. A `state_dir` that `setup` (when it is outside the installation) or
`connect` creates gets the same list, with you as its owner, and so does each directory it had
to create on the way to it; if a list cannot be set, what was created is removed rather than
left to be opened as it is. A directory that already exists is left as it is by those commands,
and so is any entry another program has added to it, because an agent's sandbox is meant to
reach this directory: nothing refuses a state directory over who else is on its list. `jevlin
setup` is the exception for `<home>\state` and its siblings: it gives them an owner-only list on
every run, which removes such an entry until the sandbox's own setup adds it again. `doctor`'s
`state access` line says whose the directory is and names anyone else the list admits.

`sessions_dir` defaults beside the state directory, and the state directory's parent is the
installation's own directory, where `credentials.json`, the claim link (`claim.json`), an
unfinished registration (`registration_pending.json`) and the release check's record
(`update_check.json`) are kept: never the state directory, which an agent's sandbox may write.
`[platform] url` is never dialed: a printed claim link must point there, at exactly that origin.
`[agents] url` is where `connect` sends requests. A non-default, non-loopback `[platform] url`
without `[agents] url` is refused; a loopback one alone defaults `[agents] url` to it. Every URL
must be https, or http on loopback, with an ASCII host and no user, query or fragment; an error
names the key (`router.url`, `platform.url`, `agents.url`).

Those sections are the whole file. A `schema = 1` file holding any other key or section is
refused by name (`format 1 does not define X; correct or remove it`), and nothing is guessed about
what the key meant. A file with no `schema` line that still reads as format 1 (a key spelled like
`schema`, in another case, a letter or two off or inside a table, or a `[router]` or `[agents]`
table, or a `[platform] url`, none of which an older jevlin wrote) is a format-1 file with a
mistake in it, refused saying the `schema = 1` line is missing. Any other file with no `schema`
line is one an older jevlin wrote, and every command
refuses it with one message, "this configuration is from an older jevlin; run `jevlin setup`".
It is never reinterpreted, because its endpoints were chosen under rules this client no longer
has. A file naming another schema number is refused with a message saying which schema this
jevlin reads.

Setup loads an existing config first. A format-1 file that loads is left byte for byte. A file
with no `schema` line is set aside as `jevlin.toml.old` (`.old.1`, `.old.2`, ... if that name is
taken), nothing in it changed, and a fresh format-1 file is written in its place, naming the
state and sessions directories, router, portal and agents API the old file named (an endpoint
variable set for the run still chooses its own); your registration and key are kept, and any
other setting you had changed by hand is yours to copy over. A
format-1 file with a mistake in it is refused with the error and its path, and nothing is written.

## Environment variables

| variable | read by | effect |
|---|---|---|
| `JEVLIN_CONFIG` | every command | The config file, after `-config`. Setup's profile block sets it. |
| `JEVLIN_HOME` | every command, setup | This machine's installation directory, instead of `~/.jevlin`. |
| `JEVLIN_API_KEY` | search, limits, the turn end, fetch | The search key, overriding `credentials.json`. With a `-config` that is not the installation's own file, it is sent only when that config names the installation's router; to use another router, set `JEVLIN_CONFIG` to its config. |
| `JEVLIN_TRACE` | search, hooks | `off` turns the trace off entirely: the hooks write no bridge and no lineage file, and search sends none. |
| `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL`, `JEVLIN_AGENTS_API_URL` | every command, setup | Each replaces its URL for the run, after the config file and under the file's own URL rules; setup also writes them into a fresh config instead of the defaults. |
| `JEVLIN_BINARY`, `JEVLIN_SKIP_DOWNLOAD` | npm wrapper | Use a binary already on the machine; install without fetching. |
| `JEVLIN_LAUNCH` | setup, upgrade, uninstall | Set by the npm wrapper, so the binary knows npm owns it. |
| `JEVLIN_TRACE_BRIDGE` | search | The trace envelope an agent's hook put in front of the command. |
| `JEVLIN_HARNESS`, `JEVLIN_LINEAGE`, `JEVLIN_SESSION` | search | Exported by Cursor's hooks: the agent, its lineage file and its hashed session. |

The last three are written by the hooks and the binary itself; you do not set them.

## Files and locks

Under `~/.jevlin` (or `JEVLIN_HOME`) on the default layout:

| path | what it is | written by | safe to delete? |
|---|---|---|---|
| `jevlin.toml` | The config. | setup | No. |
| `credentials.json` | The search key, owner-only, and the router and agents API it is sent to. Refused if a symlink or readable by others. | connect, login | Only to forget the key: `login -forget`. |
| `registration_pending.json` | A registration the platform answered that is not yet saved; finished by the next connect. | connect | No: it holds the new key. |
| `claim.json` | The claim link and code for this installation's agent, while it is unclaimed. | connect | Yes; the next `jevlin connect` mints a fresh link, which ends the old one. |
| `search-default` | `agents prefer`'s choice. | agents prefer | Yes; the skill default returns. |
| `search-tier` | `agents tier`'s choice: the tier a search carries when the request names none. | agents tier | Yes; `[router] tier`, then the router's default, returns. |
| `search-fast-set` | `agents tier fast <preset>`'s choice: the preset a fast search sends as `fast_set`. The skill never sets it. | agents tier | Yes; `agents tier default` removes it. |
| `requests/` | Claude Code only: a search or fetch request holding a `'` or a `$`, written by the agent with the Write tool and read by the call with `--stdin < <file>`. Each file is the request word for word. The call reads it through the shell, so jevlin never sees its name and does not delete it; nothing prunes the folder, and `uninstall -purge-state` removes it. | the agent | Yes, with no search running. |
| `update_check.json` | The daily release check's record: when it ran, what it found, and a release a rollback holds. | upgrade | Yes; the next agent session checks again, though a rollback's hold is forgotten. |
| `bin/jevlin` | A native binary, and `jevlin.previous` after an upgrade. | setup, upgrade | Through `uninstall -binary`. |
| `state/` | The agent registration (`agent.json`), the trace key (`trace.key`), and connect's lock and resume stamp. | setup, connect, search | No: it is your registration. |
| `sessions/` | Lineage files, one per workspace or Cursor conversation, and the hooks' window state; with `turn_end` on, an empty mark per searched turn and a turn's final answer for the moment before it is sent. | hooks, search | Yes, with no agent running; the hooks write them again. |
| `setup-env.json` | Windows only: what setup changed in your user environment. | setup | No; uninstall reverts from it. |

| lock | coordinates | safe to delete? |
|---|---|---|
| `~/.jevlin.lifecycle.lock` | The gate setup, connect and upgrade pass; uninstall holds it, so none starts under it. | Yes, when no jevlin command runs. |
| `setup.lock` | One setup or upgrade at a time. | Yes, when none runs. |
| `state/connect.lock` | One connect at a time. | Yes, when none runs. |
| `bin/<binary>.update.lock` | One upgrade of that binary at a time. | Yes, when none runs. |
| `update_check.lock` | One release check at a time for the installation, whichever copy runs it. | Yes, when none runs. |

**The daily release check.** The hooks start it: a session's start and stop for Claude Code,
Cursor and Codex; for opencode, `hook start` when the plugin loads, when a top-level session is
created and at each of its idles (opencode reports one idle as two events, so the second start
finds the check not due); for Pi, `hook start` when a session starts. Hermes starts none. Each
reads `update_check.json`, beside `credentials.json`, and when no check has run in the last day
starts `jevlin upgrade -auto` as a detached child; the hook itself contacts nothing. A hook
running inside an agent's sandbox starts no check: one whose environment carries Codex's
sandbox markers, or a proxy on this machine's loopback, the only route out of Codex's sandbox,
because the check's request would go through that proxy to a host Codex's profile does not list
and fail the Codex command that is running.

The child takes the installation's check lock in one attempt, records the check before it
touches the network (a check that cannot write its record makes no request), and asks the
project's GitHub releases for the latest version, with no credential. Under
`[update] mode = "apply"`, the default, it replaces a native binary through the same verified
transaction as `jevlin upgrade`, taking the installation's own locks only then and in one
attempt: when a setup, uninstall or upgrade holds them it records `lifecycle_busy` and checks
again the next day. Under `"notify"`, or for a copy npm
installed, which is never replaced, it only records that a newer release exists. A word `mode` does not know counts as `notify`, and
`doctor` names it. It never moves to an older release, never installs a release a person rolled
back from (below), and never replaces a binary that belongs to another installation than the
config that started it. When it installs, it installs only a release of this copy's own minor
version (0.7.0 to 0.7.1, never 0.7.x to 0.8.0): a release of another minor may change what an
installation must hold, as 0.7.0 did with the config, so it is recorded as available, `status`
and `doctor` say it is a new minor release left for you, and `jevlin upgrade` installs it. The
check asks only for the latest release, so once that is another minor it finds no later patch
of this one; `jevlin upgrade -version X.Y.Z` installs one by name.

The record is about one binary, the resolved path it names, at the version its outcome left
there: the release an upgrade installed, the one a rollback restored, the one a check ran as.
A record about another binary or version counts as none. Its outcome is `started`, `current`,
`available`, `upgraded` (with `refresh = "failed"` when the agent integrations could not be
refreshed afterwards), `rolled_back`, `failed` with the failure word `upgrade` would print, or
`not_release` for a development build; `status` and `doctor` read it and nothing else. Two
records outlast a day because a person has to act on them. `jevlin upgrade -rollback` holds the
release it left: no check installs that release, or an older one, again; a newer one it does,
and `jevlin upgrade` by hand lifts the hold. An automatic replacement that could not put the
binary back (`manual_intervention`) is never overwritten by a check: `status` and `doctor` list
every copy that survives beside the binary, read from the binary's directory, until a person's
`jevlin upgrade` succeeds; none is deleted. A record dated in the future, from a clock that was
set back, counts as due, and so does one that found a release under `"notify"` once the mode
is `"apply"` on a native copy. Deleting the record is safe: the next agent session checks again,
though it also forgets a hold.

## Doctor's checks

| check | OK when |
|---|---|
| search key | `credentials.json` holds a key for this installation's router. `NO` when there is none, or the file cannot be used (a link, or readable by others); a search would then need `JEVLIN_API_KEY`. The key's value is never shown. |
| registration | This agent is claimed on the platform, or not registered at all, since a key from `jevlin login` searches without one. `NO` for a registration that is not yet claimed, one that expired, or a record that cannot be read; the fix is `jevlin connect`. |
| trace key | The state directory holds `trace.key`, or does not hold it yet: the first search that can write the state directory makes it, so only a key that cannot be read is `NO`. |
| binary on PATH | A bare `jevlin` runs this installation's own binary. `NO` names the copy that answers instead — an npm shim earlier on PATH is the common one — or says nothing on PATH answers at all. Checked only when the installation keeps a binary at its `bin`. The agents' own files name the binary by full path and are unaffected either way. |
| up to date | The daily release check found this copy current, or has not run yet. `NO` names the newer release and the one command that applies it here. `UNKNOWN` when the last check failed, is still running, could not be read, is dated in the future, or ran more than three days ago, which says the hooks that start it may not be running. |
| state access | Windows only: the state directory and its credentials have an access list and an owner, who is you, SYSTEM or Administrators. Anyone else the list names is shown, and is `OK`: an agent's sandbox is expected there. |

`NO` is a fact and a successful diagnosis; `UNKNOWN` is the absence of one. `doctor` exits
non-zero only when every check came back `UNKNOWN`. Every check but `up to date` is local, and
that one reads the record the daily check left rather than asking anyone. It opens existing
state only and creates nothing: no state directory, no key, no registration.

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
tokens, JWTs, a password inside a URL), the credential after an `Authorization` or
`Proxy-Authorization` header's `Bearer`, `Basic` or `Token` scheme whatever its shape (a header
line, `curl -H '…'`, a JSON or JavaScript key; a variable such as `$TOKEN` or `${TOKEN}` and a
placeholder such as `<token>` stay), a PEM private key block whole, markers included, quoted
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
`ARG`, `const`, `let`, `var`, a word of flags such as `-x`), list markers (`-`, `*`, `+`, `1.`,
`1)`) and quote markers (`>`), and directly after `$`, `$env:` or `${env:`. Only there is the
quote sure to open a value, and even there it does not run past a later secret: a line it
crosses into that assigns a secret before the quote closes (`3. Then set DB_PASSWORD="…"`)
means the quote never closed, its value is the rest of its own line, and the later secret is
read as its own. Anywhere else it closes on its own line, because in `print("password=", pw)` or `"PASSWORD=" + pw` the quote closes a string,
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
home path, including `/mnt/c/Users/` and an account name with a space in it, and the same path
in the dashed form Claude Code names a project's folder with (`-Users-<account>-`,
`-home-<account>-`, `C--Users-<account>-`, every character but a letter or digit written as `-`,
so `first.last` is `first-last` there). Anywhere else your account name is left, because it is
often an ordinary word. The account is the one `USERNAME` names on Windows, `USER` or else
`LOGNAME` elsewhere, or else the home directory's name, and is searched for only when it is
ASCII, so that both scrubbers fold its case alike; a non-ASCII name goes in a home path by the
rule above and nowhere else. A generic name such as `root`, `ubuntu`, `vscode` or `macbook-pro`
is left, because it identifies nobody.

It is a filter over text a model wrote, not a guarantee. A secret with no telling name and no
known shape, in ordinary prose, passes, and so do these: `password: …` in the middle of a
sentence; `NAME = value` unquoted outside PowerShell; a value inside a quote that opened before
another word (`echo "export PASSWORD=a b"` keeps `b`); a lowercase cmd `set` name, or a `set`
written after other words, keeps the tail of a value with spaces; a quote that Windows
PowerShell 5.1's native-argument quoting moved — a prompt handed to a program as a
command-line argument reaches it re-quoted, `set "API_TOKEN=your token"` as
`API_TOKEN=your" token` and the next setting as `"DB_PASSWORD=correct horse battery` with no
closing quote — keeps the value's tail after its first word, because the moved quote reads as
one that closes a string or never opens one (pass the prompt on stdin, which keeps the quotes
where they were typed, or use Git Bash or the serve API; 5.1 itself reads piped non-ASCII in
the ANSI code page, so a prompt with accents wants Git Bash or the serve API either way); a flag's value of lowercase
letters only, or after a flag whose last word is not a secret's (`--auth …`); a flag's quoted
value broken across lines; a quoted value after `=` in the middle of a line that runs across
lines keeps every line after the one its quote opens on, which is the whole value when the quote
ends its line (`docker run -e API_TOKEN="` and a line break); a value quoted across lines keeps
its lines from the first one that assigns a secret inside it (`PASSWORD=…`), where it ends; a secret of capitals and digits
after `$` (`password: $ECRET123`), read as a variable; a Spring default that starts with a digit
(`${DB_PASSWORD:1234…}`), read as bash's substring; JSON
escaped inside a quoted string, such as a `curl -d "{\"password\": \"…\"}"` body;
NUL-separated `env -0` output; a PowerShell table row whose name fills its column, and the rows
after a one-word line that follows a table, which reads as a name with no value; and a Windows
name with spaces at the very end of a path keeps all but its first word. In return, a line that
begins `Password: …` loses the rest of the line, prose that writes a header with its scheme
(`send Authorization: Bearer token`) loses the word after it, a call at a line's start (`apiKey: getKey(),`)
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

Off unless `[search] turn_end = true`. The trace carries what the assistant wrote before a search;
this carries what it concluded. When a turn ends, the agent's end-of-turn hook sends the router's
`POST /v1/turns` one record:

| field | carries |
|---|---|
| `session_id`, `turn_id` | The same hashed ids the turn's searches carried. |
| `harness` | Which agent. |
| `status` | `completed`, `interrupted` or `failed`. |
| `final_text` | The assistant's last message of the turn, scrubbed of secrets, last 32 KiB. Only on a completed turn, and absent when there is no message to send; the status is sent either way. |
| `final_chars`, `truncated` | Its length before the cut, and whether it was cut. |
| `user_text` | What you asked in that turn, scrubbed of secrets, first 32 KiB. Claude Code, Cursor, opencode, Pi and Codex. Absent for a turn Claude Code started rather than you: a continuation or loop tick, a background task's result, a message from another session, the output of a `!` command, or the summary it writes when it compacts the conversation. |
| `steps` | The turn in order: what the assistant wrote (scrubbed, capped), and for each tool it called the tool's **name, duration and whether it succeeded**, where the agent records them. Never a tool's input or output. A search of ours is marked by its call id. Claude Code, opencode, Pi and Codex in full; under Cursor, the searches only. |
| `model`, `usage` | The model, and the turn's token counts. Claude Code, Cursor, opencode, Pi and Codex. |

Only for a turn in which a jevlin search was served: `search` marks the turn after the router
answers, so a search you refused, or one that failed, does not count, and a turn with no served
search sends nothing. Under Claude Code, a subagent searches under the turn that launched it, and
one running in the background can do so after that turn has ended; that turn is then reported
late, by the next turn end in the session, with what you asked, its steps and its last message as
the transcript holds them. Only a subagent's search makes a turn late: a turn you interrupted
after the agent itself searched sends nothing, but one in which a subagent searched is reported
late, as completed, since the hook cannot tell it from a turn that ended. A turn is reported once:
a search under a turn already reported does not report it again. Under Codex and opencode a
subagent runs a turn of its own, and its search is joined to no turn end. Only to the router
this run is configured with (`[router] url`, `JEVLIN_ROUTER_URL` or the compiled default); there is
no other address it falls back to. With it on, what you typed in a searched turn is sent; what your tools read and wrote never is: not a file's contents, not a command, not its output. The model's private reasoning and a subagent's own steps are not sent either. The hook writes the record to an owner-only file in
`sessions/` and a detached `jevlin turn-end` sends it once and deletes the file, sent or not.
`JEVLIN_TRACE=off` turns it off too. Claude Code (2.1.196 or later), Cursor, opencode, Pi (0.80.4 or later), and Codex
where its hooks are installed (macOS and Linux); other agents send none. Codex's turn is read from
Codex's own session file when the turn ends; a subagent's turn sends nothing of its own, and the
calls that started and awaited it appear as tool calls. A project set to retain no content keeps the status and not the text.

## Security notes

### Credentials

`login` reads an sr- key from stdin or `-key-env VAR`, never an argument, checks it against the
router without spending, and writes `credentials.json` as `0600`, as `connect` does. No key ever
goes into a command line or an agent's config.

The stored key is sent only to the router and the agents API recorded for it when `connect`
stored it, whichever config a command is given. `connect` records both origins (scheme, host
and port) in `credentials.json`, from the registration it finished, and `login` the router its
config names. A file an earlier version wrote without them belongs to the origins of the
installation's own config (`JEVLIN_CONFIG`, else `$JEVLIN_HOME/jevlin.toml` or
`~/.jevlin/jevlin.toml`), never of a `-config` that names another file, and only a `connect`
run under the installation's own config writes them down: if you run with another config, run
`connect` once with `JEVLIN_CONFIG` set to it. A command whose config names another origin stops
before any request, with `key_origin_mismatch` and `fix_input` in machine mode, naming both
origins, or saying the installation's config names none.

`credentials.json`, the registration journal (`registration_pending.json`), the claim link
(`claim.json`) and the release check's record (`update_check.json`) live in the installation
directory, the parent of the state directory, and outside every directory Codex's sandbox may
write: the profile `agents install` writes for Codex makes the state and sessions directories
writable and nothing else. A sandboxed command can read the key, as any process running as you
can, but cannot replace it, finish a registration or change where a claim link points.

## Building from source

```bash
make build      # bin/jevlin
make verify     # build, vet and lint (each incl. Windows), tidy, race, vuln, cross-compile
make quick RUN=TestName   # vet, lint and only the named tests, while you work
```

Go 1.27 or newer; the tests also need Node.js (CI uses 22) to run the embedded opencode plugin.
