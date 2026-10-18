# Changelog

One heading per tag, newest first, each beginning `## vX.Y.Z — YYYY-MM-DD`. See
`docs/RELEASING.md` for how a release actually gets cut. `goreleaser`'s auto-generated
changelog (from `git log` between tags) already covers the mechanical commit-by-commit
record — what belongs here is the handful of things a participant or operator should be
told in plain language, that a list of commit subjects wouldn't make obvious on its own.

An entry describes the release it sits under, as that release behaved. A later release
superseding something does not make the older entry wrong, and older entries are not
rewritten to match newer behaviour; the newer entry says what changed.

## v0.4.1 — 2026-10-19

Releases are now signed, and a native install upgrades itself by default. From v0.4.0, run
`jevlin upgrade` once: v0.4.0 only tells you this release exists, and installs it checking the
checksum alone. From then on your copy checks the project's signature on every release and
installs a newer one itself at the daily check; add `[update] mode = "notify"` to your config
to be told instead. An npm install is updated with npm, as before. This release's updater has
not been run on a Windows machine with real-time antivirus protection on; CI's Windows runners
carry none.

- **`jevlin upgrade` installs only releases signed by the project.** A release now carries
  `checksums.txt.sig`, a signature over `checksums.txt` by the project's release key, and the
  updater checks it against the key built into it before it trusts a checksum. Someone able to
  replace a release's files can no longer get a binary of their own installed by publishing a
  checksum that matches it. The automatic upgrade under `[update] mode = "apply"` checks the
  same way. This release itself is installed by the copy you have now, which checks only the
  checksum; the signature is checked from the next upgrade on.
- **A native install now upgrades itself by default.** `[update] mode` defaults to `"apply"`:
  at the daily check, a native install that finds a newer release replaces itself through the
  same verified transaction as `jevlin upgrade`, signature included. To be told instead, set
  `[update] mode = "notify"`. An npm install is still only told, and a release you rolled back
  from is still left alone. A copy on v0.4.0 keeps that release's default and only tells you
  about this one; once `jevlin upgrade` has installed it, it upgrades itself from then on.

## v0.4.0 — 2026-10-18

A daily check for a new release, and four fixes. By default the check only tells you that
a newer release exists; with `[update] mode = "apply"` a native install upgrades itself,
and an npm install never does. The fixes: `status` says when Codex on Linux is holding your
searches back, another installation's epochs no longer show as a delivery fault, a turn a
background subagent searched in is now reported, and the trace scrubber removes your
account name from Claude Code's project-folder names. Upgrading from v0.3.2 needs nothing
else; add `[update]` to a config only once this release is installed.

- **A daily check for a new release.** Once a day, when an agent session starts or ends (Claude
  Code, Cursor and Codex; opencode's plugin and Pi's extension now start the flush at a
  session's start too; Hermes does not), the hooks start a detached check against the project's
  GitHub releases. By default it only tells you: `jevlin status` and `jevlin doctor`'s new
  `up to date` line say what it found and the one command that installs it. With
  `[update] mode = "apply"` a native install installs a newer release itself, the way
  `jevlin upgrade` does: verified, run before and after, the old binary kept as
  `jevlin.previous`. An npm install is never replaced this way. The default is to tell until
  releases are signed. The check contacts nothing but GitHub, sends no credential, and is
  never started from inside an agent's sandbox. It never undoes you: after
  `jevlin upgrade -rollback` the release you left is not installed again, only a newer one, and
  if an automatic upgrade could not put the old binary back, status and doctor list what
  survives and no check runs until you upgrade. Its record is `update_check.json` beside your
  credentials. `mode = "off"` stops the check. An older jevlin refuses a config that names
  `[update]`, as it refuses any key it does not know, so add it only once this release is
  installed.
- **On Linux, jevlin now says when your Codex searches are not being delivered.** Codex's Linux
  sandbox ends the flush a search starts, so evidence from Codex waits for a jevlin run outside
  it, which Codex's hooks provide once you approve them. With mining on and neither `SessionStart`
  nor `Stop` approved, `agents status`, `status` and `agents install` now say so, and `status`
  (and `status -json`, under `delivery`) counts the searches waiting and when the oldest was made.
- **A Claude Code turn whose background subagent searched is now reported.** With `turn_end` on, a
  subagent launched in the background searches under the turn that launched it, usually after
  that turn has ended, so the turn's end had already found nothing to report and the search was
  never joined to a turn end. The next turn end in the session now reports that turn, once, with
  what you asked and its last message. Only a subagent's search does this: a turn you interrupted
  after the agent's own search still sends nothing. A Codex or opencode subagent's search no
  longer leaves a mark that nothing takes.
- **Running jevlin on two machines no longer shows a delivery fault on the one not earning.**
  When another installation of yours holds an epoch, this one's searches for it can never be
  delivered. They were retried until they were quarantined, and `status` reported a delivery
  fault for the whole time. They now wait without a fault, and are dropped once the epoch is
  over, as before. A fault already shown about them clears once they are all that is left, or
  once they are dropped.
- **The trace scrubber removes your account name from a Claude Code project folder's name.**
  Claude Code names a project's folder after its path with every separator turned into `-`
  (`-Users-<account>-work-proj`), and that name turns up in ordinary assistant text: under
  `~/.claude/projects/` and in a background task's output path. The scrubber removed the account
  from `/Users/<account>/` but left it in the dashed spelling of the same path. With the trace or
  the turn end on, it is now removed there too, in both scrubbers; a dashed name that is not your
  account, or one with no home folder in front of it, is left.

## v0.3.2 — 2026-10-17

A fix for installing from npm: `jevlin setup` refused every global npm install, so the README's
first install path did not work. A native install is unaffected and needs nothing from this
release beyond the skill's new wording.

- **`jevlin setup` works from a global npm install.** Until now it refused every one, with
  "setup must run from a global npm install … npm could not say where this copy is
  installed": the npm launcher misread where npm puts a scoped package. Run
  `npm install -g @jevlinsearch/jevlin@latest`, then `jevlin setup`. If you got past it by
  running the binary with `JEVLIN_LAUNCH=npm:global`, the installation it set up is the same
  one and needs nothing redone.
- **The skill and setup call the router the Jevlin search router.** An agent's installed skill
  picks up the new description at the next install, setup or upgrade.

## v0.3.1 — 2026-10-17

The rewards service moved to a new address on 16 October, and an installation set up before
then stops earning until it is told. The first entry says the one line to change, and what an
installation from v0.1.0 or earlier has to do instead. A new installation is set up with the
new address, and searching is unaffected either way.

- **The rewards service moved, and an installation set up before the move has to be told.**
  It is now at `https://rewards-testnet.jevlin.ai`, and a new installation is set up with that
  address. An installation set up earlier keeps `https://rewards.nyks.dev` in its config,
  because jevlin never rewrites your config, and since the move it refuses the old address's
  answers as coming from a different service: searches work, but nothing earns, and
  `jevlin doctor` says it cannot reach the authorization server, which is "off-origin". To fix
  it, change one line in your config's `[mining]` table to
  `as_url = "https://rewards-testnet.jevlin.ai"`, then run `jevlin flush` and `jevlin doctor`.
  Your authorization, wallet and payout address carry over.
  If you use Codex, also run `jevlin agents install -client codex` and answer yes, so that
  Codex's sandbox lets the flush reach the new address.

  An installation set up with v0.1.0 or earlier needs more than that line. Its config also
  names `router-api.nyks.dev` and the nyks.dev platform, whose key the jevlin.ai router does
  not accept, and when this was checked the moved service rejected a search served by the old
  router. To earn again it registers on jevlin.ai: keep the wallet's recovery phrase or move
  its funds out, remove the installation with `jevlin uninstall -purge-state`, and set this
  release up fresh.
- **The wallet's default chain node is `https://rpc-testnet.twilight.org`**, the same node under
  its new name. This needs nothing from you; `-node` and `JEVLIN_WALLET_NODE` still override it.

## v0.3.0 — 2026-10-16

Two fixes to how the client lives inside a coding agent's hooks. Nothing changes on the
search or rewards side, and `jevlin upgrade` from v0.2.0 is the first upgrade this
repository's updater performs against a previous release of its own.

- **A hook of yours in the same group as jevlin's is no longer removed with it.** Claude Code's
  `/hooks` adds a hook to the group already under the same matcher, which can be the one jevlin
  wrote. `agents install`, `uninstall` and the re-render an upgrade runs used to treat that whole
  group as jevlin's, replacing or deleting your hook without a word, and an uninstall could
  delete the file. Now only jevlin's own hook is touched, yours stays where it was, and the plan
  says so. Affects Claude Code and Codex.
- **The turn end no longer sends text Claude Code wrote as your message.** With
  `[miner] turn_end = true`, a turn Claude Code started itself sent what it wrote as what you
  asked: a background task's result (`<task-notification>`, which names the task's output file
  under your account), a continuation or loop tick, a message from another session, the output
  of a `!` command, or the summary written when the conversation is compacted. Such a turn is
  now reported with its searches and no user text.

## v0.2.0 — 2026-10-14

The search platform's hosts move to `jevlin.ai`, and claim links now come from
`platform.jevlin.ai`, which v0.1.0 refuses: an installation on v0.1.0 cannot show its claim link
until it upgrades (`jevlin upgrade`, or `npm install -g @jevlinsearch/jevlin@latest` for an npm
install). This release also explains the rewards service's region refusals and adds a
participant-level search band.

- **A region refusal from the rewards service is explained, not reported as a broken
  credential.** When the rewards service refuses mining from your address (`REGION_RESTRICTED`,
  `REGION_INELIGIBLE`, or the `REGION_UNVERIFIED` hold for a VPN, proxy or Tor exit), `status`,
  `doctor`, `jevlin join` and the flush say so in plain words, searches recorded meanwhile wait
  instead of failing delivery, and search is unaffected. See
  [the guide](docs/guide.md#when-your-region-is-refused).
- The search platform moved to `jevlin.ai`: the router is `api.jevlin.ai` (was
  `router-api.nyks.dev`), the portal and claim pages are `platform.jevlin.ai` (was
  `platform.nyks.dev`), and the agents API is `agents-v1.jevlin.ai` (was `agents-v1.nyks.dev`).
  The defaults, the help text, the docs and the Codex sandbox allowlist a fresh install writes
  all name the new hosts. The authorization server (`rewards.nyks.dev`) and the chain RPC
  (`rpc.nyks.dev`) are Twilight's and are unchanged.
  **Upgrade note:** `jevlin setup` writes these URLs into `~/.jevlin/jevlin.toml` and never
  rewrites a config it finds there, so running it again changes nothing: an existing
  installation keeps the old hosts, which still answer today. A config that names only the old
  `base_url` keeps loading, with the old agents API implied from it as before. Claim and console
  links now come on `platform.jevlin.ai`, and an installation configured for `platform.nyks.dev`
  accepts them. To move an existing installation to the new hosts, edit `router_url`,
  `base_url` and `agents_api_url` in that file, then run `jevlin agents install` so a Codex
  sandbox allowlist names them too.
- `jevlin agents tier fast|balanced|max|default|status`, and `/jevlin tier <band>` in every
  agent that has the skill: the band a search carries when the request names none, saved
  beside the config as `search-tier` and read by `search` on its next request. `[miner] tier`
  in the config is the same setting one layer down; the request's own `tier` outranks both,
  and with none set the request carries no tier and the router applies the default for the
  key. The skill now tells the agent to leave `tier` unset rather than calling `fast` the
  default, and never to send `max`. `agents status` shows the band in force; purge removes
  the file. Setting a band asks first in Cursor, as it does in Claude Code and Codex, and a
  `[miner] tier` this version cannot use is ignored and named by `agents tier status` rather
  than stopping the config from loading.

## v0.1.0 — 2026-10-13

This repository starts from `twilight-project/dropin-miner` at
`9c547ab0c809244fbb7c6b2138ab61212583f2b3` — the v0.3.0 release plus its pre-import
hygiene PR — imported as one commit. Everything before that is in
[dropin-miner's changelog](https://github.com/twilight-project/dropin-miner/blob/main/CHANGELOG.md).
This is the first release of jevlin-go, published on npm as `@jevlinsearch/jevlin`; there is
nothing earlier to upgrade from, so `jevlin upgrade` has not been exercised against a
previous release.

- **Codex commands no longer get open network from jevlin; only the search hosts do.**
  `agents install` used to set `network_access = true` in Codex's sandbox, which let every
  command Codex runs reach any host. On macOS and Linux it now writes a Codex permission profile
  that allows only the router, the authorization server and the platform's agents API, enforced
  by Codex's network proxy, and the next install (or `upgrade`) replaces the old block. Supported
  with Codex 0.158.0 or newer; older versions are not supported, and below 0.131 Codex refuses
  the file and does not start until you upgrade it or remove jevlin's block. A Codex setting of
  your own that the profile would change is asked about first, and your own profile is extended,
  never edited. On Windows the network is no longer opened at all: a search there needs Codex's
  approval to reach the router. The Windows block has not been live-smoked: its tests run on
  the Windows CI runners, against no real Codex.

- **Windows: a state directory jevlin creates is owner-only.** A
  `[mining] state_dir` that `setup` (for one outside the installation),
  `connect`, `enroll`, `mining enable` or `mining disable` creates now
  gets a protected access list that names only you, and you as its owner, as
  `jevlin setup` gives `<home>\state`, instead of inheriting its parent's, and
  so does every directory it had to create on the way to it. If a list cannot
  be set, what was created is removed rather than left to be opened as it is
  next time. Nothing refuses a state directory afterwards: an
  agent's sandbox is meant to be able to reach it, so another principal on its
  list is not an error. `doctor` has a new Windows-only `state access` line:
  it says whether the directory and its credentials are yours, names anyone
  else their access lists admit, and calls only a directory or credential with
  no access list, or with an owner who is not you, SYSTEM or Administrators
  (or none on record), a problem.
- **The payout address moved beside `credentials.json`.** `connect` and
  `mining enable` used to keep the address you chose in the state directory,
  which a Codex-sandboxed command can write, and the background resume declared
  whatever address it found there: a planted one could become your first,
  effective payout address. It now lives in the jevlin home. With none on file
  there, the address recorded and declared comes only from your config's
  `[mining] payout_address` or, for a `payout.json` an older version left in
  the state directory, when it is this installation's own wallet address. Any
  other address there is never recorded, declared or repeated: `status` says
  it is not used and to run `jevlin mining enable` at a terminal, which on an
  installation already enabled now asks only for the address, not the enable
  question an Enter would have answered No. An agent in Codex's sandbox
  can still reach the rewards service with this installation's access in the
  moments before your address is in force; `jevlin payout show` says which
  address is.
- **The per-shell trace id no longer gives away your hostname.** A search with
  no hook used to send a plain hash of the hostname and parent pid, which the
  router could match against guessed hostnames. It is now keyed with a random
  `trace.key` that `setup` and `connect` create in `state_dir`, so ids are
  unique to each installation and a search in a sandbox that can't write there
  still keeps one id. A search with no usable key that can't make one sends a
  one-off id each time. Run `jevlin setup` again to give an existing
  installation its key; a search makes one itself wherever it can write.
- **Setup refuses a binary that someone else could replace.** `setup`,
  `agents install` and `agents prefer` record the binary's path in your
  agents' hooks and skills, and setup puts its directory on PATH. They now
  refuse a binary under the temp directory, or one that sits in a directory
  every user can write or another user owns. A directory writable by `admin`,
  `wheel`, `sudo` or `root`, as a Homebrew prefix is, is accepted; one
  writable by any other shared group, such as Debian's `staff` on
  `/usr/local`, gets a warning and setup goes on. On Windows only the
  temp-directory rule is checked. Move a refused binary somewhere only you
  can write, such as `~/.local/bin`, and run it from there.
- **Cursor on Windows asks before a PowerShell search with an apostrophe in
  it.** The participant picks Cursor's terminal there, and Git Bash reads the
  PowerShell form's here-string as an ordinary quoted string that an
  apostrophe in the request ends, running whatever follows. Cursor's hooks now
  allow that form only when the request holds no `'`; one that holds it waits
  for your approval and reaches the router without Cursor's label. The skill
  says to write an apostrophe as `\u0027`, which reaches the router as the
  same query.
- **The registration journal moved beside `credentials.json`.** `connect`
  used to keep `registration_pending.json` in the state directory, which a
  Codex-sandboxed command can write, so a forged journal could make the next
  `connect` swap your stored platform key for someone else's. It now lives in
  the jevlin home, and one left in the state directory is discarded unread.
  A registration an older build left half-finished is not resumed; run
  `jevlin connect` again. If that build was replacing an expired registration,
  `connect` finds the new agent from your stored key and rebuilds the record
  from the platform.
- **The claim link lives beside `credentials.json`, and `connect` asks whose
  key it is first.** The link you are told to open used to come from
  `agent.json`, in the state directory an agent's sandbox can write: a
  planted record could keep your agent's id and carry another agent's link,
  on the platform's own address. The link now lives in `claim.json` in the
  jevlin home, written only from the platform's answer or the registration
  journal beside your key, and bound to its agent. `connect`, `status` and
  `mining enable` print only that one, only while it is exactly a link on
  `platform.base_url`, and only for an unclaimed agent: as the platform has
  just said, for `connect`, and as the registration on file says, for
  `status` and `mining enable`, which do not ask. Before printing anything, a `jevlin connect`
  you run asks the platform which agent your stored key belongs to: a record
  naming another agent is rebuilt from the answer, a claimed agent shows no
  link, and if the platform cannot answer no link is shown at all. A link an
  older version kept in `agent.json` is not read; the first `connect` after
  upgrading mints a fresh one. The background resume never rebuilds, and
  `-force` does not either: it says so and to run `connect` without it.
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
- **Nothing an agent's sandbox writes into the state directory reaches your
  terminal as an escape sequence, or as jevlin's own words.** An `agent.json`
  with a control, format or line-separator character in any field, or an
  identifier (agent id, scope, slot name, time) that is not a plain token, such
  as a sentence or the route `me`, is treated as damaged: `status` says it cannot be read and to
  run `jevlin connect`, which rebuilds it from the platform. The payout
  records must hold real payout addresses, a held-binding note is shown only
  for the address this installation would declare, with a reason only when it
  is one jevlin knows, and never over what the rewards service just answered;
  no message repeats the agent id `agent.json` held; a health record's detail is written without such characters and
  refused with them, and a record that does not decode, in the state
  directory or beside `credentials.json`, is reported without repeating its
  text. What `jevlin flush` prints is filtered the same way,
  since its errors name files an agent's sandbox can write. A registration
  interrupted mid-publish is finished whatever the state directory holds at
  `agent.json` (a record naming another agent, a link, a directory), which is
  set aside. `connect -json -force` on a record naming another agent answers
  `fix_input` (`force_does_not_rebuild`) rather than telling a caller to retry.
- **`jevlin mining enable` never turns mining off when it is on.** With mining
  already enabled it asks only for a missing payout address, at a terminal,
  and without one records `[mining] payout_address` or says it is missing; it
  used to write the config's `[mining] enabled` without a terminal, which a
  config made at a terminal does not set, turning mining off with no output.
- **`jevlin payout set` records the address it declared** beside your key, so
  `status` and the background resume follow your change instead of naming the
  old address.
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
- **A bridge the hooks did not write is removed only when its value is one.**
  Before replacing a `JEVLIN_TRACE_BRIDGE` assignment already on a command, the
  hooks and the opencode and Pi adapters now require its value to be base64url,
  the only shape a real bridge has. A value that opened a quote used to be cut
  at its first blank, which could turn text the shell would have read as a
  quoted string into commands it ran. Such a command is now left exactly as
  written.
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
  (`npm install -g @jevlinsearch/jevlin`), `jevlin version` prints `jevlin X.Y.Z`, the release
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
