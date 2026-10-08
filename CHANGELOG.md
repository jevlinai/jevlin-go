# Changelog

One heading per tag, newest first, each beginning `## vX.Y.Z — YYYY-MM-DD`. See
`docs/RELEASING.md` for how a release actually gets cut. `goreleaser`'s auto-generated
changelog (from `git log` between tags) already covers the mechanical commit-by-commit
record — what belongs here is the handful of things a participant or operator should be
told in plain language, that a list of commit subjects wouldn't make obvious on its own.

An entry describes the release it sits under, as that release behaved. A later release
superseding something does not make the older entry wrong, and older entries are not
rewritten to match newer behaviour; the newer entry says what changed.

## Unreleased

This repository starts from `twilight-project/dropin-miner` at
`9c547ab0c809244fbb7c6b2138ab61212583f2b3` — the v0.3.0 release plus its pre-import
hygiene PR — imported as one commit. Everything before that is in
[dropin-miner's changelog](https://github.com/twilight-project/dropin-miner/blob/main/CHANGELOG.md).
The first release replaces this heading with its own.

- **The registration journal moved beside `credentials.json`.** `connect`
  used to keep `registration_pending.json` in the state directory, which a
  Codex-sandboxed command can write, so a forged journal could make the next
  `connect` swap your stored platform key for someone else's. It now lives in
  the jevlin home, and one left in the state directory is discarded unread.
  A registration an older build left half-finished is not resumed; run
  `jevlin connect` again. If that build was replacing an expired registration
  and `connect` now refuses with "expired registration is no longer known to
  the platform", move `agent.json` out of the state directory and run
  `jevlin connect` once more: it rebuilds the record from the platform.
- **Files an agent's sandbox leaves in jevlin's directories no longer reach
  your own files.** Codex's sandbox may write jevlin's state, intake, sessions
  and spool directories. A link left there could make a hook, a flush or
  `connect -resume`, which run outside the sandbox, write into a file of yours
  elsewhere, and a FIFO could stall a search. jevlin now never opens an existing
  name there for writing, never waits on what is at a name it reads, refuses a
  link at a lock's name, and keeps the spool's quarantine from leading outside
  the spool. The window state is no longer kept in the shared temporary
  directory when no sessions directory is configured. Two layouts change:
  a `spool_dir` that is itself a symlink is refused (mining stops, with a
  message, until it names the real directory), and `agents install` gives
  Codex no writable directories at all when one of them would contain your
  `jevlin.toml` or the folder holding `credentials.json`; give `state_dir`,
  `intake_dir`, `sessions_dir` and `spool_dir` directories of their own.
- **A hook allows a search only under the binary path its skill shows.** Cursor,
  Claude Code and Codex used to allow any path that reached the jevlin binary when
  the hook resolved it, and on Linux `/proc/self/exe` is jevlin to the hook but
  the shell itself to the shell that runs the command. A search naming the binary
  any other way than the path the hook was started by now waits for approval.
- **Setup adopts only what is yours.** A set-aside installation another
  account owns is no longer offered, and adoption leaves behind any file or
  folder in it that is not yours. On Windows, owner-only access now also makes
  you the owner: another owner could otherwise open the folder up again.
- **Wallet output no longer passes a node's escape sequences through.** Error
  messages, transaction logs and transfer senders that a chain node sends are
  cleaned the way search results are before `jevlin wallet` or `jevlin earnings`
  prints them, and a balance that is not a plain number is refused rather than
  printed. A malicious node can no longer rewrite the terminal or its clipboard.
- **Service-document endpoints must live on the AS's origin.** Discovery now
  refuses a `/.well-known/twilight-mining` document whose endpoints,
  `authorization_server` or `participation_resource` point at another host, a
  different port, plain http, or carry userinfo, so a tampered document cannot
  steer the access token, the participation capability or the provider
  verification key elsewhere. The mining plane stays closed until the AS serves
  a same-origin document; search is unaffected.
  `enrollment_authorization_template` is still held to the provider allowlist
  instead.
- **`search` takes `-config` once.** The Claude Code allow rule ends after
  the installed `-config <file>`, and whatever follows it runs without a
  prompt. A second `-config` used to win, so an appended one could send your
  key to another config's router. `search` now refuses a repeated
  `-config`, even one naming the same file, before reading any config.
- **A `jevlin.toml` in the working directory is no longer read on its own.**
  Without `-config` or `JEVLIN_CONFIG`, every command now uses the
  installation's config (`$JEVLIN_HOME/jevlin.toml`, else
  `~/.jevlin/jevlin.toml`) or built-in defaults. Before, a `jevlin.toml` in a
  cloned repo could name its own router or platform host and receive your
  `sr-` key from `login`, `search`, `limits` or `connect`. To use a config
  in the current directory, pass `-config ./jevlin.toml`.
- **Codex searches carry their session.** `agents install` writes Codex's
  hooks into `~/.codex/hooks.json` on macOS and Linux, and a search Codex runs
  then reaches the router with its session, turn and call, and a subagent's
  search names the session that started it. Codex runs hooks only once you
  approve them in Codex, and jevlin never approves them for you;
  `agents status` says what Codex has on record. Only the search the skill
  shows is changed and allowed. No assistant text travels with a Codex
  search, and Windows gets no hooks yet. A Claude Code hook run by Codex now
  stands aside, as it does for Cursor.
- **Opt-in: Codex's turn end.** With `[miner] turn_end = true`, a Codex turn
  in which a jevlin search was served is reported when it ends, as Claude
  Code's is: what you asked, what the assistant wrote along the way and its
  final answer, scrubbed, and each tool call by name and outcome, read from
  Codex's own session file. Never a tool's input or output, and nothing of a
  subagent's own turn.
- **The trace scrubber covers secrets that have no shape.** Assistant text sent
  with a search, and a turn's final answer, already lost API keys, tokens,
  emails and the name in a home path. It now also loses Stripe secret keys, a
  PEM private key block whole (quoted, assigned or neither), and the value of a
  secret known by its name: `DATABASE_PASSWORD=…`, `--api-key=…`, a camelCase
  `accessToken=…` or `.npmrc`'s `_authToken=…`, `$env:API_TOKEN = '…'` and
  `${env:API_TOKEN}`, `password = "…"`, YAML's `password: …` at a line's start,
  JSON's `"client_secret": "…"`, also after `;` or in a code span (never a key
  inside a string, such as a prompt's `"Password: "`), a reference's literal
  default (`${DB_PASSWORD:-…}`, Spring's `${DB_PASSWORD:…}`, a template's
  `default "…"`), a value that starts
  with `$` and mixes lowercase letters and digits, `curl --api-key …` and
  `--secret-string …`, cmd's `set NAME=…` to the end of the line, an Azure SAS
  `sig=`, and a password chained behind another setting in a URL or a
  connection string. A quoted value is read as a shell reads it, so a password
  with spaces, PowerShell's `'it''s'`, POSIX's `'\''`, Python's triple quotes
  and `-e "DB_PASSWORD=a b c"` all go whole; it runs across lines only where
  its name starts the line, after nothing but declaration words (`export`,
  `const`, `ENV`, `local -r`) and list markers, so a string that ends in
  `PASSWORD=` or `--password ` never reaches into the line after it. It
  also loses every value in a pasted environment listing or PowerShell's
  `Get-ChildItem Env:` table, rows with no value and wrapped values included,
  the trace bridge's own value in a quoted command,
  this machine's hostname wherever it appears as a word, and your account name
  where the text uses it as an account: `USER=…`, `name@host`, a home path, also
  with doubled separators and a Windows name with spaces. Your account name in
  ordinary prose is left, since it is often a word. The opencode and Pi
  adapters scrub in time linear in the text on every input found slower, each
  held to a bound by test, so no run of blanks or repeated token holds up a
  search. A filter, not a guarantee: a secret with no telling
  name and no known shape still passes, and `docs/reference.md` names the forms
  it does not cover.
- **Opt-in: the conversation around a search.** With `[miner] turn_end = true`,
  when a turn in which jevlin searched ends, the router is sent that turn: what
  you asked, what the assistant wrote and concluded, and which tools it called
  — each tool's name, time and outcome, never its input or output. A session
  can then be read from the question to the answer. Off by default. Scrubbed
  and capped like the trace; nothing for a turn with no served search, and
  nothing unless `miner.router_url` names the router. Claude Code and opencode
  send the whole turn; Cursor sends your prompt, the searches, the reply and
  the model (its hooks carry nothing between tool calls). Cursor gains one hook
  entry for this, so run `jevlin agents install` again; it keeps nothing unless
  `turn_end` is on. Not live-smoked under Cursor or opencode: the fields come
  from Cursor's documentation and opencode's SDK types, not from a running host.
- **opencode searches carry their turn.** A search made under opencode now says
  which of your messages it answers to, so a session's searches group into
  turns on the router as they do for Claude Code.
- **A claim link is only shown if it points at the platform.** `connect`, `status`
  and `mining enable` now check the claim link stored in `agent.json` against
  `platform.base_url` before printing it, the same check a freshly registered
  link gets. A link that fails is not shown; a foreground `connect` mints a
  fresh one in its place.
- **A lost claim link is replaced, not mourned.** For a registration rebuilt from
  the platform that is still unclaimed with no claim link, a foreground
  `jevlin connect` now asks the platform for a fresh link and prints it. The old
  code is dead the moment a new one is minted, so only a connect you run does
  this — never a background resume — and where the platform cannot mint one, the
  old one-line notice remains.
- **The search request reaches more of the router.** The `--stdin` request
  accepts `providers` (up to 16 names, to trim the fan-out or reach an extended
  arm that fires only when named), and `view: "merged"` now also travels to the
  router, which adds its own `merged` and `indexes` to its raw answer. The
  envelope's `merged` stays this client's own merge; the human form's
  `-view merged` with `-format json` shows the router's.
- **A traced search names its session twice.** The hashed session id the trace
  envelope already carried is now also sent as the request's top-level
  `session_id` and `X-Session-Id` header, so the router can group quick
  reformulations of one conversation's queries. Same identifier, no new content:
  it is sent only while a trace envelope rides, and `JEVLIN_TRACE=off` still
  sends none of it. `connect` also names the build (`jevlin/<version>`) when it
  registers an agent.
- **The trace prefix no longer edits a command it cannot carry.** The bridge's
  POSIX prefix binds to the first command of a line, so the hooks now write it
  only when that command is the search itself. Before, a loop around a search
  was rewritten into a bash syntax error and the whole command failed, and a
  `cd … && jevlin search …` sent the bridge to `cd`, splitting one
  conversation's searches across unrelated sessions. Such commands are now left
  exactly as written; the search still runs, with the per-shell identity.
- **A subagent's searches name their parent.** Under Claude Code and opencode, a
  search made by a subagent now carries `parent_session_id` in its trace: the hashed id of
  the session that started it. The router could already tell that two sessions
  searched during the same turn, but not which one delegated to the other. It is
  an id, hashed like the rest; no agent name or task text is added.
- **`jevlin limits` reads your spend caps.** The three caps and today's spend
  against the effective ceiling, straight from the router; reading spends
  nothing and works even at the ceiling. Setting caps is not built yet. A
  search refused with a 402 now also says which money problem it is and the one
  thing that clears it, instead of a bare status line.
- **Installed through npm, or from a release archive.** The `install.sh`,
  `install.ps1` and `setup.sh` scripts are gone. Without Node, download the archive
  for your OS from the releases page, verify it against `checksums.txt`, put the
  binary on PATH and run `setup`.
- **Renamed to jevlin.** The binary and the npm package are `jevlin`
  (`npm install -g jevlin`), `jevlin version` prints `jevlin X.Y.Z`, the release
  archives are `jevlin_<version>_<os>_<arch>`, and `jevlin upgrade` fetches only from
  `jevlinai/jevlin-go`'s releases. The npm wrapper's own knobs are `JEVLIN_BINARY`
  and `JEVLIN_SKIP_DOWNLOAD`.
- **A new home, config and environment.** The installation lives in `~/.jevlin`
  with its config in `jevlin.toml`, the lifecycle lock is `~/.jevlin.lifecycle.lock`,
  and every variable the client reads, exports or documents is `JEVLIN_*`
  (`JEVLIN_HOME`, `JEVLIN_CONFIG`, `JEVLIN_API_KEY`, the trace channel's
  `JEVLIN_TRACE_BRIDGE`, `JEVLIN_LINEAGE`, `JEVLIN_SESSION` and `JEVLIN_HARNESS`, the
  wallet's and setup's overrides). The skill is `/jevlin`, in a `jevlin/` directory
  under each host, and opencode's and Pi's adapters are `jevlin.js` and
  `jevlin.ts`. The word *mining* stays wherever it was.
- **A security policy and a NOTICE.** `SECURITY.md` says how to report a vulnerability
  privately and what happens next. `NOTICE` carries the copyright line and credits the
  packages derived from `tokendrop-proxy`, and ships in every release archive and in the
  npm package.
- **The documentation is four documents, each fact in one of them.** `README.md` is a
  short introduction, and the npm page with it. `docs/guide.md` walks through setup,
  earning, upgrading and removing; `docs/agents.md` covers each coding agent and its
  known limits; `docs/reference.md` holds the envelope, config, environment, files and
  `doctor`'s checks. `docs/PARTICIPANT.md` is gone, replaced by the guide.
