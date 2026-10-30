# AGENTS.md — jevlin

Agent instructions for jevlin: supersearch for coding agents. Loaded every session.

## What this is
jevlin is **supersearch for coding agents**, one call to every search engine in the set with each result attributed: a **no-daemon** Go CLI a coding agent shells out
to (`jevlin search …`) plus a lifecycle hook. Between calls nothing runs — no reverse proxy,
no resident process. The client mines nothing: mining moved off the client to the platform,
which reconciles what the router served, so nothing here records, spools or submits anything
about a search. What the client holds on the participant's own machine is the platform-issued
search key (`credentials.json`), the agent registration and its claim link, and a trace key
that keys the session ids a search carries. Everything a search sends is the query and, when a
host's hook supplied one, the redacted trace (invariant 2).

Until 0.6.1 this client was also the Twilight MINIS mining participant: it recorded each served
search, delivered it to an Authorization Server (AS) under OAuth and DPoP, enrolled in epochs and
held payout keys. All of that was removed in one stripping, not rewritten: the search,
hook, agent-integration, setup, connect, upgrade and uninstall code is the code that shipped,
with the mining calls taken out. That version's line continues as its own binary for the
participants who ran it; this client carries no compatibility with it: its config has no
`schema` line and is refused as an older format (`config.ErrLegacyConfig`), which `jevlin setup`
sets aside as `jevlin.toml.old` before writing configuration format 1, keeping the registration;
and the files it left on a machine are neither read, moved nor named: this client carries no
list of them, and a purge lists whatever it finds left by path (`sayLeftInHome`). Where a rule
below says "an earlier version", that is what it means.

## Authority — the protocol is owned upstream, not here
The search platform's control plane (`platform.jevlin.ai` — `connect`; `pkg/platform`) is a
contract owned upstream, by `search-router`'s own design doc
(`tokendrop-auth-server-design/docs/implementation/search-platform-agent-onboarding-design.md`,
authored for the `search-router` repository and mirrored there — not owned here).
**Conform upward.** A frozen requirement that looks wrong is a **spec escalation** to the design
side — never a local reading chosen because it's easier to build. `pkg/platform` conforms to it:
implementer here, authority elsewhere. The router's search and fetch protocol is `search-router`'s
too; `cmd/jevlin/testdata/search/router` mirrors its fixtures. `pkg/` is the **shared
implementation, owned by this repo** and importable elsewhere; `cmd/jevlin/` is this client's
wrapper. Client-specific decisions (retry shapes, the trace envelope, install/agent behaviour)
have no upstream home — record them in `docs/` here.

## The verify loop
`make verify` = `build vet lint lint-windows tidy race vuln cross`, the checks that take seconds
first. **Green before every push**: CI runs the same checks on every pushed head, and a commit pushed
with others is checked as part of that head. `lint` is golangci-lint v2 (gosec, misspell locale US,
unconvert, depguard, gofmt, goimports); `vuln` is govulncheck. `race` runs every test under the race
detector, split by `tools/testshard` into `RACE_PARTS` processes at once, because cmd/jevlin's tests
run one after another and are nearly all of its time; CI splits the same run into three jobs. There
is no separate `test` stage: CI's test matrix runs every test without the race detector on all four
systems, and that is where pkg/redact's tight linear-time bound is held. While a change is being
made, and before each commit, `make quick RUN=<pattern> PKG=<packages>` vets, lints and runs only
the named tests.

## Hard invariants — a change that violates one is wrong even if it compiles and every test passes
1. **Fail-open around the search.** A failure in anything a search does beside answering — the
   trace, a lineage write, the detached claim resume, the daily release check a hook starts —
   MUST NOT turn a successful search into a client-visible failure, and MUST NOT add latency the
   user notices. The hook emits nothing and exits 0 on any internal error, a panic included
   (`hookMain`); a panic while preparing the trace sends the search with none
   (`searchTraceFailingOpen`). Under Codex's permission profile a process a sandboxed search
   spawns contacts **only hosts the profile lists** (`codexAllowedHosts`: the router and the
   platform agents API): on macOS a detached child outlives the search, and a CONNECT the
   profile denies fails whichever Codex tool call is running at that moment, the search's own or
   a later, unrelated one. A new destination on that path is a new host there, or it breaks this
   invariant.
2. **No prompt/completion content leaves the machine, except through two named channels.**
   Prompt, message, completion, reasoning, and tool payloads MUST NOT be logged, hashed, sampled
   or sent anywhere as a record of a search; nothing about a served search is kept on the
   machine at all. The search-router **trace** is the one
   channel that carries model-influenceable text off the machine (the assistant text just before a
   search, capped and hashed per `trace.go`); it MUST be redacted before egress and is a conscious
   privacy surface, not a default. The **turn end** (`turn_end.go`, `turn_detail.go`, `turn_hosts.go`, `codex_turn_end.go`) is the only other one: for
   a turn that searched, the user's message, the assistant's visible text, and its final message,
   all under the same scrub and caps, with a skeleton of the turn's other tool calls. It is sent
   only when the installation set `[search] turn_end = true`. It MUST stay off by default; MUST
   send nothing for a turn in which no search was SERVED (a proposed, refused or failed search is
   not one — `search` marks the turn after the router answers); MUST go only to the router this run
   is configured with: `[router] url`, `JEVLIN_ROUTER_URL` or the compiled default; there is no
   fallback (`Search.RouterURL` is never nil and never a stand-in); and MUST
   NEVER carry a tool's input or output. Of a tool call it sends the name, the duration and the
   outcome — `turnStep` has no field for more, and one must not be added. A command is read for
   one purpose, to recognize our own search, and is not kept. Thinking blocks and a subagent's own
   records are not sent.
3. **Every credential- or key-bearing `http.Client` has an explicit redirect policy.** No bare
   client on `net/http`'s default (follow-10, body replayed on 307/308). Same-origin bounded
   (`auth.SameOriginRedirects`) or refuse (`http.ErrUseLastResponse`) — per client; the forbidden
   state is *not choosing*. Enforced module-wide by `cmd/jevlin/boundary_test.go`, which walks every
   `http.Client{}` construction rather than trusting a package-scoped review: `credentials.go`'s
   login probe sends the participant's sr- key as a bearer credential to the router, outside the
   scope a review of "the clients that carry a credential to the platform" would enumerate.
4. **Secrets never in argv.** stdin or owner-only (0600) files, never a flag. The one documented
   exception is the search *query* (not a credential), called out where it happens.
5. **Never assemble or call an unadvertised operational URL.** Every host this client dials is
   **configured, not discovered**. The config is configuration format 1 (`pkg/config`): a file
   beginning `schema = 1` with three endpoint sections, `[router] url`, `[platform] url` and
   `[agents] url`, each defaulting to its compiled value in `pkg/config/defaults.go`
   (`DefaultRouterURL`, `DefaultPlatformBaseURL`, `DefaultAgentsAPIURL`), and replaced at
   runtime, after the file, by `JEVLIN_ROUTER_URL`, `JEVLIN_PLATFORM_URL` and
   `JEVLIN_AGENTS_API_URL`. All three are https-or-loopback, with an ASCII host and no userinfo,
   query or fragment, enforced at config load (`config.ParseEndpoint`) — every search sends the
   participant's sr- key in `Authorization` to `[router] url`, and `connect` sends the
   platform-issued key to `[agents] url`, a cleartext-credential exposure over anything else.
   A file that is not format 1 — no `schema` line, another schema number, or a key format 1
   does not define — is refused and never reinterpreted: an older file's endpoints were chosen
   under rules this client no longer has, and guessing what they meant is how a key reaches a
   host it was not issued for. `[platform] url` carries no credential — nothing is ever dialed
   there — it is the portal origin a printed `claim_url` is checked against (invariant 12); the
   human portal and the machine API are separate hosts. The sole exception is the self-updater: `internal/selfupdate`
   contacts only the compiled-in canonical `jevlinai/jevlin-go` GitHub release origin,
   carries no participant credential, follows only its bounded HTTPS GitHub redirect allowlist, and
   no environment variable, config or flag may redirect that origin.
6. **Permissive for the router's response, strict for what this client emits.** The router's
   response shape is not frozen and is decoded permissively (no `DisallowUnknownFields`) on
   purpose — the router owns its product API, and a field it adds must not break a search.
   What this client emits to an agent is the versioned, closed envelope of invariant 15. (Until
   0.6.1 this invariant was the AS wire's strict/permissive asymmetry; the number is kept so
   that references to the later invariants in code and tests stay right.)
7. **Retired with mining.** "Durable before visible; remove only on ack" governed the spool, which
   left with the mining plane. Nothing about a served search is persisted. The number is kept so
   that references to the later invariants stay right.
8. **`pkg/` MUST NOT import `cmd/`.** Keeping `pkg/` wrapper-agnostic is what makes it importable and
   is why this repo owns it and can hand it to another client by import, not copy. Enforced by
   `cmd/jevlin/boundary_test.go`.
9. **The chain SDK module tree is banned.** A lean binary shipped to users must not pull the
   Cosmos SDK's module tree; the code that once needed chain messages is gone, and the rule
   stays so it cannot come back by accident. `go.mod` carries no cosmos-sdk/cometbft dependency.
   Enforced by `TestNoChainImportsAnywhere`.
10. **Retired with mining.** `mining_decision.json` was the one runtime authority on whether
    mining was on. This client reads no such file: a search is a search, and the envelope
    carries nothing about mining (invariant 15). The number is kept so that references to the
    later invariants stay right.
11. **A claim or console URL is printed, never opened.** `connect.go` imports no `os/exec`.
    Structural, and tested.
12. **`[platform] url` is compared against, never dialed.** It is the origin a platform-supplied
    URL must match (`validatePlatformURL`): every byte printable ASCII, no user before the host,
    and the URL must read back as exactly the text given, so what is checked is all of what is
    printed. The origin must be the portal's own exactly: there is no retired-portal successor
    and no alias, so a link on any other host, a portal this installation once named included,
    is refused, and nothing widens the match. Every request goes to `[agents] url`. The claim link lives in `claim.json` beside
    `credentials.json` (`auth.ClaimRecord`), bound to its agent and written only from the
    platform's answer or the registration journal beside it: by a foreground run, or by a resume
    finishing that journal (which, in a sandbox, cannot write there and leaves it to the next
    foreground run), never in the sandbox-writable `agent.json`; it is checked again before it is printed
    (`platform.ValidateStoredClaimURL`), because `[platform] url` may have changed since.
13. **Registration is one journaled transaction.** A complete `register` response is journaled
    (`registration_pending.json`) before `agent.json` or the credential is written, and finished
    from the journal on the next run, never by a second `register`. The journal is consulted before
    anything else. It lives beside `credentials.json`, never in the state dir: a sandboxed agent can
    write the state dir, and the journal can authorize replacing the credential. A
    `registration_pending.json` found in the state dir is discarded unread, and once the journal's
    key is in `credentials.json` an `agent.json` naming another agent is set aside rather than
    allowed to stop publication. Before a foreground `connect` prints, polls or replaces anything
    it asks `GET /v1/agents/me` with the stored credential: a lost or unreadable record, or one
    naming an agent the credential does not belong to, is rebuilt from the answer, with nothing
    local changed before a valid answer; the credential is the authority, because it lives outside
    every writable root and `agent.json` does not. Unreadable includes a record holding a character
    a terminal acts on or an agent id that names a route (`LoadAgentRegistration`,
    `auth.ValidAgentID`). A `/me` failure shows no claim link. `-force` does not rebuild; it bypasses
    recovery and authorizes deliberate replacement where local state would otherwise refuse a
    fresh registration. Separately and without `-force`, an ordinary foreground `connect` may
    replace a registration the platform has positively verified as expired; that replacement
    changes the platform agent and its key. `-resume` never registers, rebuilds or replaces.
14. **Retired with mining.** The payout path, its send journal and its lock left with the
    mining plane. The number is kept so that references to the later invariants stay right.
15. **The machine protocol is versioned, closed and typed.** `search --stdin`, `fetch --stdin`,
    `status -json`, `doctor -json`, `connect -json` and `limits -json` emit one envelope with the mandatory header (`version`,
    `command`, `ok`, `exit_code`, `status`, `code`, `retryable`, `action`), a closed action set
    (`none`, `retry`, `fix_input`, `connect`, `login`, `check_access`, `report`) and exit classes
    0 through 4. Classification is by typed errors and exact codes, never by message text
    (one retry, licensed by two exact codes: `trace_unsupported`, and `malformed_json` to a request
    carrying `objective` or `search_queries`; a router 401 is `login`, never `connect`).
    The search envelope carries no `mining` object and nothing about earning: a search is a
    search. Machine mode never invents a participant decision. The mandatory header and its semantics are what is versioned
    (`machineVersion` in `machine.go`): changing the required header's shape or the meaning of
    `ok`, `status`, `code`, `retryable`, `action` or `exit_code` requires a protocol version
    change; command-specific payload additions that preserve that contract do not.
16. **Trace text follows one pipeline, and it scrubs before it cuts.** The shared
    `agent_trace_common.js` order is: complete bounded source → scrub → UTF-8 byte accounting →
    32 KiB history tail → envelope → 48 KiB envelope check → base64url. A source entry too large
    to scrub whole is omitted whole, never sliced, because a slice can cut a secret in half. Raw
    host history never enters a command line; hosts that expose less (Hermes) send less; the hook
    fails open and exits 0 on any internal error. **The bridge that carries the envelope is written
    in the syntax of the shell that will run the command** (`bridge.go`, mirrored in
    `agent_trace_common.js` and pinned by `TestBridgeGuardsAgree`), taken from the host's
    declaration — or, for Claude Code, from the tool its payload names, since that host runs two,
    and for opencode on Windows from the `$SHELL` signal opencode itself resolves its shell by,
    usability mirrored per call by its plugin (`opencode_plugin.js`), since that host settles its
    shell only when it starts.
    A POSIX prefix handed to PowerShell is looked up as a program name, and the search does not run.
    **And only onto a command that syntax can carry it to**: the POSIX assignment prefix binds to
    the first simple command and is a syntax error before a compound one, so it is written only
    when that first command is provably the search (`posixSearchLeadsRe`, one regex with the JS
    copy); a loop, a list or a pipeline with the search elsewhere is left byte-identical, and that
    search runs on its local fallback identity instead of not running at all.
    **Provenance: only a bridge an adapter wrote for this call may carry that adapter's harness** —
    it removes every assignment it can prove standalone in a declared shell's syntax and writes its
    own, and leaves a command carrying one it cannot remove exactly as it found it. The trace is unauthenticated metadata either way:
    the binary cannot prove where an environment variable came from, and nothing treats a harness
    value as evidence of origin. **A search believes its host's channel, not whatever variable it
    finds** (`searchTrace`): a host that exported `JEVLIN_LINEAGE` has declared the lineage file
    as its channel and writes no bridge, so a bridge beside it is dropped unread — never decoded,
    never a fallback when the declared file is missing — and reported on stderr only, because the
    machine envelope is what the model reads and a model that knows the variable is one of the ways
    a foreign bridge arrives. With `JEVLIN_SESSION` exported, a lineage file — declared, or
    found by the walk — is used only when it holds that session; a shell that exports none is
    served exactly as before, and Cursor keys its lineage file by conversation as well, so two
    conversations sharing one workspace do not share a file. **A host started by another host as a
    shell command inherits the outer host's declared channel, and so carries the outer session and
    label.** That is chosen, not a
    gap: the exported environment is the declaration, and the inner search is work the outer
    session asked for.
17. **Verified replacement and explicit destruction.** `jevlin upgrade` fetches only from the
    compiled-in canonical `jevlinai/jevlin-go` GitHub release origin — the one exception
    to invariant 5 — over HTTPS, with no participant credential, following only its bounded GitHub
    redirect allowlist, under the frozen size bounds and one three-minute operation deadline that
    also bounds replacement. Every asset the verifier requires is verified before the archive is
    inspected, and only the one root executable is taken out of it. The staged candidate must run
    and report exactly the release's version before it is installed, and the canonical path itself
    must run and report it again before the displaced binary is committed as the one-level
    `.previous`. Before that commit, a recoverable failure restores the prior binary and leaves
    `.previous` byte-identical; if restoration itself fails, the operation returns
    `manual_intervention`, reports every surviving copy, and cleanup preserves every reported path.
    Upgrade never moves to an older release: only `upgrade -rollback` does, by validating a fresh
    copy of that one local `.previous` — never running it in place — with no network, and swapping
    one level. An npm-managed copy is never replaced. The daily check the hooks start,
    `upgrade -auto` (`cmd/jevlin/update_check.go`), is one release lookup under its own check
    lock, and only for a replacement this same transaction under the same locks, taken in one
    attempt. It is started only by a hook, never by a search or anything a search spawns, and never
    by a hook whose environment says it runs inside a sandbox (Codex's markers, or a proxy on
    loopback), so the one host it reaches stays off the sandboxed path invariant 1 fences. Its
    record lives beside `credentials.json`, where no sandbox writes, and it never undoes a person:
    a release `upgrade -rollback` left is held, a `manual_intervention` waits for a person's
    upgrade, and a binary another installation owns is that installation's to update. Participant state is removed only by
    `uninstall -purge-state` after an interactive typed confirmation, the installation path, that `-yes` cannot supply and a non-terminal cannot reach: protection against
    accidents and non-interactive automation, not against a program driving a terminal.
18. **A prompt answers only to a line the participant typed.** A read that ends without one — an
    interrupt, a closed stdin, a console read the terminal aborted — is not the visual default and
    not the opposite of it; it is the absence of an answer, and the operation stops there, records
    nothing, writes nothing, sends nothing, and exits non-zero. `-yes` answers what it already
    answers and never turns an interrupt into an answer. `prompt.go` owns the rule and the two
    readers that apply it (`promptBufio` over the shared `bufio.Reader` a command's prompts take
    turns on, `promptSetup` over `readSetupLine`); a bare `line, _ := br.ReadString('\n')` at a
    question is the defect, not a shortcut: at the mining question an earlier version asked, a
    discarded read error made an interrupt an empty line, an empty line was not `y`, and a
    decision nobody made was written — and then `connect` registered. The question is gone; the
    rule is not.
    A typed refusal and an unanswered question are different outcomes and must stay
    distinguishable by exit code, which is why the ones that change nothing either way
    (`agents install`'s `Proceed?`) still differ there.
    **One exception, deliberate:** when `agents install` or `setup` writes nothing else — an
    unanswered question, no terminal without `-yes`, an unanswered or declined `Proceed?` or
    agents question, a declined Codex switch — it still commits the plan's safety writes
    (`commitSafetyOnly`), and nothing else; a typed no then still exits 0 and no answer non-zero. A safety write
    only ever removes jevlin's own open network from Codex's config (`codexSafeForm`: the old
    block's `network_access = true` line, or our profile's own network while the proxy is off),
    so it changes no setting of the participant's and decides nothing for them; leaving it would
    keep every command Codex runs able to reach any host, which is the one outcome nobody could
    have chosen. The run says it closed that block, and still exits non-zero.
19. **A sandboxed agent's files never redirect or stall this client's own.** The state dir and
    the sessions dir are writable roots of Codex's sandbox
    (`codexSandboxRoots`: the permission profile's filesystem entries, on Windows the roots-only
    block's `writable_roots`), while hooks and connect's resume work in them
    from outside it. Live on Codex 0.160.0's macOS sandbox, a command there could leave a symlink
    out of the root, a hard link to a participant's file outside it, and a FIFO; having emptied a
    top-level root, it could not `rmdir` it. A root inside another root can be renamed away and
    replaced, though. So in those directories no
    existing name is opened for writing (`fsx.CreateNew`; a replacement is an exclusively
    created, unpredictably named temporary file renamed into place), no read waits on what is
    there or reads past a bound (`fsx.ReadRegular`, `ReadRegularNoFollow` where a check on the
    name came first), and no lock follows a link (`fsx.OpenLock`). The premise is
    that the installation's own directory (credentials.json, the registration journal, the claim
    record, the update stamp) and the config's directory are writable from no sandbox: `agents install`
    grants Codex nothing when a layout would put either inside a root (`codexRootsProblem`), and nothing here can help if Codex's
    own workspace is the installation or above it. `pkg/fsx/confined.go` owns the operations.
    `cmd/jevlin/writable_roots_guard_test.go` resolves every file's imports and classifies each
    reference to a file operation in os, io/ioutil, syscall, x/sys/unix, x/sys/windows and
    pkg/fsx by (file, function, call) and count, with the reason it is safe, and fails on a new
    one until someone classifies it: the forbidden state is not choosing. `writable_roots_test.go`,
    `codex_sandbox_test.go` and `pkg/fsx/confined_test.go` plant the links and FIFOs at the exact
    names, and swap a file between a check and its read through the test seams in `readSecret` and
    `readCredentials`. What the records say is answered in three parts. A record that could make
    this client act on, or show, its writer's say-so does not live there: the registration
    journal and the claim record sit beside `credentials.json`, and a journal an
    older version left in the state dir is discarded unread. Every record
    a command prints is held on load to what this client writes: a string holding a control,
    format or line-separator character (`internal/termtext`, Unicode Cc, Cf, Zl and Zp, found by
    reflection over every string `encoding/json` fills in `pkg/auth/record_text.go`) makes
    `agent.json` corrupt and any other record unreadable; an identifier a record carries (an
    agent id, a scope, a time) is a token, never a sentence (`pkg/auth/agent_id.go`,
    held on the platform's wire as well, before anything is journaled); no message repeats the
    agent id `agent.json` held; and a decode error repeats none of
    the record (`auth.decodeError`'s `Error`, and `decodeProblem` beneath it, choose the client's
    own words and quote nothing, for the state dir's records and the journal and claim record
    beside the credential alike; the update stamp's `errUpdateStampForeign` is one fixed
    sentence). And `agent.json` naming an agent the stored
    credential does not belong to is rebuilt from the platform (invariant 13).
    `cmd/jevlin/state_record_escape_guard_test.go` builds an installation through the real
    commands, plants each class of character, one at a time, in every string of every record it
    leaves, runs every command that prints, and fails
    on a file nobody has classified; it then plants a visible sentence at every scalar of every
    record, which no command may repeat. Within those shapes a
    record still says what its last writer chose.

## Subsystems and where their rules live
A subsystem's authority is one file, and a subsystem nobody has watched fail is a hypothesis — so
each line names the file that owns the rule and the test that proves it. Where a participant can
read about the behaviour, the line ends with a pointer to that section of `docs/guide.md`,
`docs/agents.md` or `docs/reference.md`, and a change to the behaviour updates that section too.

- **The registration journal and the rebuild** — `cmd/jevlin/connect.go` owns the order
  (journal, publish, clear); `pkg/auth/store.go` owns the journal and the agent record;
  `pkg/platform/client.go` owns `Register`, `Status`, `Me` and `ClaimCode`. Its guards, in
  `agent_onboarding_test.go`, named exactly because a trailing ellipsis is not a test:
  `TestPendingRegistrationRecoveryPublishesWithoutRegister` (the journal is finished, never
  re-registered), `TestConnectRebuildsClaimedRegistrationFromThePlatform` (the `/v1/agents/me`
  rebuild), `TestForegroundConnectReplacesExpiredRegistrationAndPropagatesNewIdentity` (the
  no-flag replacement of a positively-verified expired identity),
  `TestForgedStateDirJournalCannotReplaceCredential` (a journal in the state dir, which a
  sandboxed command can write, is discarded unread and publishes nothing),
  `TestConnectRefusesCorruptRegistrationWithExistingPlatformCredential` (the refusal that
  `-force` exists to override), and the claim-code re-mint pair —
  `TestForegroundConnectMintsAFreshClaimLinkForALostOne` and `TestResumeNeverMintsAClaimLink`
  (`remintClaimLink`: minting kills the old code, so only a deliberate foreground connect asks,
  the fresh link is persisted before it is printed, and every failure is exactly the old
  no-link dead end). The journal can hold a platform key, so `-purge-state` removes it with
  the credential (`registration_journal_purge_test.go`). A record that names an agent the
  stored key does not belong to is rebuilt by a foreground connect from `/v1/agents/me` before
  anything is printed or replaced, never by the resume or under `-force`:
  `registration_unknown_agent_test.go`, against a stub that answers status only for the key's
  own agent, as the router does. The claim link is `claim.json` beside the credential
  (`pkg/auth/claim_record.go`): `claim_link_test.go` plants another agent's on-origin link in
  `agent.json` and holds connect, status and both JSON reports to never printing
  it, a claimed agent to showing none, a `/me` failure to showing none (in `connect -json` too,
  `TestConnectJSONCarriesNoLinkTheRunWithheld`), and a record naming `me` to being rebuilt;
  `TestTheRebuildNamesOnlyThePlatformsAgent` holds every message to the id `/me` returned, and
  `TestForceOnARecordNamingAnotherAgentAsksForDifferentInput` machine mode's `-force` to
  `fix_input`. `TestAJournalWinsOverAnAgentRecordNamingAnotherAgent`,
  `TestAJournalPublishesOverWhateverIsAtTheAgentRecordsName` (a loose-mode record, a link, a
  directory, a FIFO) and `registration_set_aside_test.go` hold a planted record or old evidence
  to never wedging recovery. For participants:
  [guide, The claim link](docs/guide.md#the-claim-link).
- **The prompt rule** — `cmd/jevlin/prompt.go` owns what counts as an answer and the two
  readers that ask; every prompt in the binary goes through one of them.
  `prompt_abort_test.go` drives each real command to each real question, under both an interrupted
  read and a closed stdin, and asserts the non-zero exit, the unwritten decision and the
  uncontacted platform — and, in the same file, that a typed refusal still declines and exits 0.
  The one thing an unanswered or declined question still writes is invariant 18's exception, the
  safety write that closes jevlin's own open network in Codex's config; `codex_unit_test.go`
  (`TestARunThatWritesNothingStillClosesTheOldBlock`, `TestTheOldBlockIsClosedWhateverTheAnswer`,
  `TestATypedNoAtProceedClosesTheOldBlockAndSaysSo`) and `setup_test.go`
  (`TestSetupClosesTheOldBlockWhenItsAgentsQuestionGoesUnanswered`,
  `TestSetupClosesTheOldBlockOnATypedNo`) hold that it writes that and nothing else.
- **Which installation the profile and the agents belong to** — `cmd/jevlin/setup.go` owns it
  (`otherInstallation`, `leftForOtherInstallation`). This machine's installation is
  `$JEVLIN_HOME`, else `~/.jevlin`; an explicit `-home` naming any other directory is a
  separate installation, and for it the profile step and the agents step are skipped — named as
  skipped, asked before `-yes`, `-with` or the terminal are, because none of them changes whose
  profile and whose agents these are: the documented way to make a disposable installation must
  not repoint the participant's real agents at it. The closing line and
  uninstall's restore hint both name what does configure such an installation, and
  `JEVLIN_HOME` as the way to say a directory elsewhere IS the machine's own.
  `setup_other_home_test.go` snapshots the whole sandbox, since the claim is about what is not
  touched.
- **Whose content is inside our marked region** — `cmd/jevlin/codex_region.go` owns the reading
  (`readCodexRegion`), the placement and the removal; `agents.go` owns the line scan it rests on
  (`markedRegion`, `splitMarkedBlock`, and `markerSpan`, which takes a marker only as a whole line
  outside a multi-line string); `targets.go` owns what uninstall does with it
  (`planCodexConfigRemoval`). On macOS and Linux the region is a Codex permission profile
  (`codex_profile.go`): `default_permissions`, `[features.network_proxy]`, and
  `[permissions.jevlin]` with `extends`, its filesystem roots and network domains. On Windows it
  is the old `[sandbox_workspace_write]` table with `writable_roots` alone, written where it is
  found (`planCodexSandbox`), because no profile has been seen to work in Codex's Windows sandbox.
  It is `ownership_match.go`'s rule one level finer: that rule decides whether a region is *this
  installation's* by what the renderer wrote into it (its roots, `codexRootsOwner`); this decides
  which lines *inside* it are ours the same way, from the headers the renderer writes and its one
  key. Neither is about position: Codex writes into the region — it appends tables to the end of
  the file, `codex features enable` writes `[features]` inside the markers, app-server's
  `config/value/write` puts a root key directly after our `default_permissions` (the captures are
  in `cmd/jevlin/testdata/codex/`, as `cmd/jevlin/testdata/hermes` keeps Hermes' own output). So a
  root key that is not ours moves to just above the region, a table to just below it, and nothing
  of anybody's is ever deleted, on install and on uninstall alike. A region that will not decode,
  or whose own part is not exactly what the renderer writes, is left alone and reported. The line
  scan is **not trusted**: before a byte is written, the result must decode to the participant's
  own content plus exactly what the edit adds (install) or the original minus exactly our keys
  (uninstall), so a header the scan misses — Codex keys folder trust by path, and
  `[projects.'/home/u/work [1]']` is exactly what a loose pattern misses — costs a refusal, never a
  key. **And the decoder is not trusted either**: BurntSushi/toml accepts shapes TOML 1.0 and Codex
  refuse (a header extending an inline table, or reopening one made by dotted keys), so every write
  to Codex's config is parsed again with go-toml/v2 (`codex_strict.go`, `planCodexWrite`) and
  refused, naming the participant's colliding table, if Codex would not load it; a leading UTF-8
  byte-order mark, which Codex accepts, is set aside to read and kept on write (`readCodexConfig`); `codex_strict_test.go` holds
  that check to thirteen verdicts taken from Codex itself. The grammar and the nets are tested
  independently on purpose (`marked_block_sections_test.go` first, because getting the split wrong
  in the removing direction destroys a participant's settings). Codex's record of hook approvals,
  one `[hooks.state."<hooks.json>:<event>:<i>:<j>"]` table per hook, is one of those tables
  (`TestUninstallKeepsCodexsHookTrustInsideOurBlock`,
  `TestInstallMovesCodexsHookTrustOutOfOurBlockRatherThanDeletingIt`); this client only ever reads
  it, for `agents status` (`codexApprovalLines`), and never writes one: approving is the
  participant's review of commands that run outside Codex's sandbox
  (`TestTheClientNeverWritesCodexHookTrust`).
  **What Codex deletes with a table.** Codex's editor reads the comments above a table as that
  table's, and `codex features disable network_proxy` deletes them with it; with our proxy table
  first, that took our begin marker (captured in `cmd/jevlin/testdata/codex/`). So the proxy table is
  written last, below our own profile tables, and a region that has lost a marker anyway — a lone
  end marker with a run of our tables above it, or a lone begin marker with one below it — is still
  ours (`damagedCodexRegion`): install repairs it, status names it, uninstall removes it whole.
  **Where the region sits is the file's to say.** `default_permissions` is a bare key, and TOML
  gives a bare key to whatever table precedes it, so the region goes **before the file's first
  table header**, or last in a file with none; found there, it is written where it is; found below
  a header, it is moved up. No line of the participant's moves relative to another.
  `insertBeforeFirstHeader` splices the block in at a line boundary with no byte of its own and
  uninstall takes exactly its bytes out, so install then uninstall, and uninstall then install,
  are byte-identical for every file whose last line ends in a newline, CRLF included
  (`codex_block_position_test.go`).
  **The participant's own profile is extended, never edited** (`codex_plan.go`). Codex merges a
  profile with the one it `extends` — both profiles' roots and domain lists apply, anything
  neither lists stays closed (seen live on 0.158.0 Linux and 0.160.0 macOS) — so when
  `default_permissions` names a profile of theirs, ours extends it, and only the
  `default_permissions` line's value changes. Codex then applies every domain the chain allows
  once ours turns the network on, whatever the chain's own `enabled` says, so the question, the plan
  and status name every host the merged profile allows (`codexAllowedFor`), and say a yes opens
  every host where the chain allows `"*"`. If their profile's network is already open (on, with
  the proxy off), ours adds only its roots: no hosts and no proxy table, which would cut their
  network down to ours. The other lines that can change are `":workspace"` (rewritten the same
  way), a bare `sandbox_mode = "workspace-write"` (commented out) and `network_proxy = false`
  (rewritten to true, never shadowed by a second definition, a duplicate key Codex refuses).
  Nothing is ever written inside a participant's table. All of it is one question showing every
  line, changed only on a typed yes (with no terminal, the by-hand text is the exact lines a yes
  writes, marks and markers included, so a file finished by hand is this installation's); a typed no installs nothing for Codex and exits 0; no line
  stops the command, exit 2 (invariant 18); `-yes` answers none of it, in `agents install` and in
  `setup`; with no terminal `agents install` exits 2 and prints the lines, and a dry run prints
  the question and plans the yes. Each changed line carries a trailing comment naming its config
  (`codexMark`, refused for a path with a control or line-separator character) and keeps the line
  it replaced.
  **Our own network is never left open.** No operation may leave our profile active with a network
  of its own and network_proxy off (`openByUs` in `codex_unit_test.go`, asserted after install,
  reinstall and uninstall of every starting state under every answer). Whenever install does not
  write the full profile — a typed no, no answer, no terminal, the upgrade re-render, a refusal —
  our existing block still takes its safe form (`codexSafeForm`): the old block loses its one
  `network_access = true` line and keeps every other line, a participant's `exclude_*` keys
  included; our profile with its own network and the proxy off loses its network. That write is
  marked `safety`; beside a full write that replaces an open block it goes in the plan's
  `fallback`; and the two are the only writes committed when a run stops without an answer
  (invariant 18's exception). Uninstall is **one unit**: the region, attributed by its roots, and the lines it
  implies go together, whichever config path the marks spell; a region left keeps its lines; a
  line whose value is no longer what install wrote is left as the participant has it, without our
  mark; and the
  region never goes while `default_permissions` would still name it. Without a region, a mark is
  this installation's when it names the same config file through any link.
  `codex_states_test.go` drives every starting state through the real commands to goldens under
  `cmd/jevlin/testdata/codex/states/` and back to the participant's bytes; `codex_consent_test.go`
  and setup's own tests run the question under a typed yes, a typed no, an empty line, an
  interrupt, a closed stdin, `-yes`, no terminal and a dry run.
  **The proxy is part of the profile.** Without `features.network_proxy` Codex does not enforce
  the domain list and the sandbox network is open to every host; `codex features disable
  network_proxy` deletes our table (the capture is in `cmd/jevlin/testdata/codex/`), so install
  restores it and `agents status` says, under Codex's row whatever this installation's state
  there, that every command Codex runs can reach any host (`codex_status.go`). Status reads only
  `~/.codex/config.toml`, and says so: a trusted project's or `-p` profile's config can turn the
  proxy off where status cannot see. Status also compares the profile's hosts with all three the
  config now names; a loopback host is never listed (`codexAllowedHosts`). Supported Codex is
  0.158.0 or newer (`codexSupported`), the versions it was verified on; older versions are not
  supported: below 0.131 Codex refuses the file and does not start, and 0.131 to 0.157 is
  unverified, which the region's own comment says. No file records Codex's version, and this
  client does not run codex to ask.
  For participants: [agents, Codex](docs/agents.md#codex).
- **Our entry in a Hermes `hooks:` block we did not write** — `cmd/jevlin/hermes_install.go`
  owns it (`findHermesOwnEntry`, `hermesRunIsRenderedExactly`), in the file that already owns the rule it
  follows: there is no YAML parser, so a false-positive refusal is cheap and an ambiguous mutation
  is not. An entry is ours only when the file's structure can be vouched for by the same scan
  install trusts, the entry sits exactly where the renderer puts one, its command is this
  installation's under `ownership_match.go`'s rule and ends in exactly the renderer's words, and —
  the net, as in the Codex block — the lines about to go are byte for byte lines `hermesHookLines`
  produces. What goes is a contiguous suffix of the rendered mapping (two, three or four lines, so
  no key is left with a null where Hermes expects a list); every other line is copied as read.
  Found, removable and mentioned are three different answers: a live hook of ours in a form this
  client will not edit — above all the form **Hermes itself** writes, since `save_config` reloads
  the file and dumps it through PyYAML, dropping our markers and folding the command — is counted
  by install and status and named with its lines by uninstall; a command named somewhere the
  structured find will not read earns a sentence and never an edit or an "already set up".
  "Found" is a claim about what Hermes will run — install turns it into "already set up", status
  into "installed" — so every rule about the block itself runs before it is made, and a rule that
  trips answers at the mention tier: one `pre_tool_call:` key, nothing at list depth that is not a
  list entry, no line at a depth a parser would reject, and our own entry made only of lines a
  parser would accept where they stand. Install's refusal then carries the warning that the file
  already names the command, so the paste advice never lands silently beside a live hook.
  `hermes_own_entry_test.go` pairs each removed shape with neighbors one step away that must come
  back byte-identical, and takes its command from a real install rather than a typed string;
  `cmd/jevlin/testdata/hermes/*.resaved.yaml` is the real output of Hermes' dumper (`resave.py`), never typed;
  and `hermes_differential_test.go` runs 2,875 generated files through the real uninstall, with
  `cmd/jevlin/testdata/hermes/oracle.py` to ask PyYAML what each meant before and after — the only judge of
  a by-line YAML edit that is not the code that made it.
- **What the client writes into a participant's files** — `cmd/jevlin/setup_config.go`
  renders `jevlin.toml`, fresh, in configuration format 1; `agents.go`, `setup_env.go` and
  `hermes_install.go` render the blocks that go into a host's own config. Every byte any of them
  contributes is ASCII: Windows PowerShell 5.1 reads a file with no byte-order mark in the ANSI
  code page, so an em dash reaches a participant as `â€”`, and in a PowerShell
  script a mis-decoded quotation mark ends a string early and the file stops parsing.
  `generated_config_ascii_test.go` renders each artifact from ASCII inputs and refuses a byte
  above 0x7F — from ASCII inputs, because a participant whose home is `C:\Users\José` is not
  this client's doing.
  **How it writes them**: a host's file is the participant's own settings, and since the upgrade
  re-renders it unattended under a deadline that kills the child, an in-place write could be
  stopped between truncating and writing and leave it empty (issue #37). So `realAgentOps`
  writes through `fsx.ReplaceFile` (`pkg/fsx/replace_file.go`): a `.tmp-` file beside the file,
  synced and renamed over it, after following a link to the file it names (a relative link read
  from the real directory it sits in, as the kernel reads it), so a link stays a link; an existing file keeps its mode, and one the in-place write could not have written
  (read-only, another account's) is refused. A FIFO is refused before anything is opened. A
  rename still makes a new inode, so a hard link keeps the old bytes. And `commitPlan` holds a
  host's removals when one of its writes failed, and `uninstall -binary` keeps the binary when
  any change to a host's files failed (`keepBinary`), because the hooks left in the file run it.
  `pkg/fsx/replace_file_test.go` (stages, links, modes, the staged name, a read-only file),
  `replace_file_unix_test.go` (the FIFO), `replace_file_windows_test.go` (the file held open)
  and `cmd/jevlin/host_files_test.go` (the real install and uninstall) hold it. On Windows any
  open handle refuses the rename, whatever it shares (seen on the CI runners, PR #120), so the
  publication alone is retried for about a second for a sharing violation or access denied,
  as `moveAside` is; `WriteFileAtomic`, on the search path, never waits.
  For participants: [reference, Config](docs/reference.md#config).
- **What a destructive run may leave behind** — `cmd/jevlin/lifecycle.go` owns the
  exclusion: which operation locks it takes, which of those files it created, and the rule that
  `release` removes exactly those and only when the operation never proceeded (`proceeded()`).
  `excludeForUpgrade` opts out, in its own words, at its own construction, because **an ordinary
  operation cannot remove its own lock file.** Removing one
  is safe only while the GATE is held, because the gate is what every contender passes before it
  opens an operation lock — with it held nobody can be between opening that file and locking it,
  so unlinking the name cannot strand a contender on an inode that no longer has one. An ordinary
  operation has released the gate by the time it holds its own lock (connect gives it up before
  its poll loop), and taking the gate back at the end is the reverse of the one lock order, which
  `TestSetupConnectsUnderItsOwnAdmission` exists to forbid. So the destructive exclusion is the
  only place a lock file can be removed, and the locks an upgrade, a setup or a connect leaves are
  named instead: `uninstall`'s `sayLeftoverLocks` lists the gate, `setup.lock`, `connect.lock`,
  the binary's update lock and the release check's `update_check.lock` that still exist, as
  safe to delete, in both modes.
  `lock_leftover_test.go` holds it to the invariant either way round — a lock still on disk is
  named, one the run removed is not — and scopes its search to that section, because a purge plan
  prints the full path of everything it removes. `lifecycle_created_locks_test.go` guards the
  created-lock rule.
- **The state directory's access list, and the Windows ACL helpers** — a sandbox's entry on
  `state\` is intended, so no open of the store and no load of a secret reads an access list,
  refuses one, or resolves an account name: that would put a DACL read on the search path of
  every installation that uses a sandbox (invariant 1). The
  store and `doctor` leave such an entry as they find it; `jevlin setup` does not: it gives
  `<home>` and `state` and `sessions` beneath it a protected owner-only list on
  every run (`directories()`), which removes an entry a sandbox's setup added to any of them, until
  that setup adds it again. That is setup's long-standing behavior and not this change's to alter. `pkg/auth/perm.go` says so where `posixModes` is read;
  `perm_reads_test.go` parses every source file in the package, Windows-tagged ones included,
  and fails on a descriptor read, an account lookup or any `winacl` call but `RestrictToOwner`;
  `perm_windows_test.go` adds `BUILTIN\Users` to the directory and to each of
  `CredentialFiles()` (all five, `agent.json` and `trace.key` included) and goes through each way
  in. The one thing the store does is at creation: every directory `OpenStore`
  makes on the way to a `state_dir` (`MkdirAll` can make several, each inheriting its parent's
  list) gets `setup`'s own protected owner-only DACL and owner (`restrictStateDir`, the same
  function), and all of them are removed again if any step fails, so the next run restricts
  directories it creates instead of opening ones it did not. `store_create_test.go` holds that wiring on every OS
  through `restrictCreatedStateDir`, a variable for the reason `secretCheckedHook` is one: the
  real call does nothing on POSIX, so deleting it there would otherwise change no result.
  What a participant is told is `doctor`'s `state access` (`cmd/jevlin/state_acl.go`): it asks
  for the directory and `auth.CredentialFiles()` by name, never lists the directory, names other
  principals as information (`OK`) and calls only a NULL DACL, an owner other than the user,
  SYSTEM or Administrators, or no owner on record a problem (`NO`). It reads nothing through a link, and a check on a
  name is not what guarantees that, because the name is in a directory a sandboxed command can
  write and can be replaced between the check and the read: `winacl.Read` opens the object once
  with `FILE_FLAG_OPEN_REPARSE_POINT`, refuses a reparse point on the handle's own attributes
  (`ErrReparsePoint`) and reads the descriptor from that handle, so what was checked is what is
  read. The `Lstat` before it only classifies early. `stateCheckedHook` is the seam between the
  two, and `state_acl_windows_test.go` swaps a credential for a junction there. What one handle
  per object does not close is a directory replaced after it was read and before its credentials
  were. Invariant 19 records that a top-level root could not be removed from inside Codex's macOS
  sandbox; nothing here establishes that for a Windows sandbox.
  Only `doctor.go`, the state access files (`state_acl.go` and its
  per-platform halves) and `setup_env_windows.go` (the owner's name in an adoption refusal) may
  reference `inspectStateAccess`, `stateACL`, `winacl.Read` or `winacl.PrincipalName`: naming a SID
  can reach a domain controller, so `acl_reach_test.go` fails on a call from anywhere a search or a
  hook runs, `loadTraceKey` included, and `perm_reads_test.go` holds `pkg/auth` to calling nothing
  from `winacl` but `RestrictToOwner`.
  `judgeStateDescriptor` is pure, so `state_acl_test.go` runs on every OS, including through
  `cmdDoctor` with a fake backend; `state_acl_windows_test.go` runs the real reader. The reader,
  the namer and `RestrictToOwner` live in `internal/winacl` because `pkg/` cannot import `cmd/`
  (invariant 8) and an earlier version's payout check needed the same ones; the package imports nothing of the
  module and judges nothing, and its descriptor-string parser is tested on every OS.
- **Where the stored key may go** — `cmd/jevlin/credentials.go` owns it (`keyService`,
  `resolveAPIKey`, `platformKey`): the key in `credentials.json` is sent only to the router and
  the agents API origins it records, which connect publishes from the registration journal
  (`keyBindingOf` when the journal is saved, never a stand-in for either, and a
  connect under another agents API than the journal's is refused) and fills in for an older
  file only on a foreground run under the installation's own config (`recordKeyBinding`: a
  recorded origin never changes); a file without them belongs to the installation's own config
  (`installationConfigPath`), never to a `-config` naming another file. search, limits, the
  turn end and fetch hold `JEVLIN_API_KEY` to the installation's router the same way
  (`envKeyRefusedFor`). Every
  Claude Code allow rule names a config, because `resolveEntry` always resolves one and
  `loadConfig` reads the installation's unwritten default path as defaults
  (`unwrittenDefaultConfig`); `binEntry.implicit` lets an install replace what an earlier
  version rendered without one. `key_binding_test.go` holds each sender to zero connections
  for another origin. For participants:
  [reference, Credentials](docs/reference.md#credentials).
- **The search protocol** — `search.go` owns the request and the fail-open trace;
  `search_protocol.go` owns the deadline, the bounded read and the one retry with the two codes
  that license it; `search_machine.go` owns search's classification and `result.merged`, which is
  the router's merged view mapped field for field and never a merge of this client's
  (`search_router_merge_test.go`, against the router's own fixtures in
  `cmd/jevlin/testdata/search/router`); `machineResultOf` owns what each `view` carries: the
  default, `merged`, says each page's text once (no candidates, no `excerpts` or
  `canonical_url`), because the envelope is what the model reads, and `full` may only grow
  (`TestTheFullViewOnlyAddsToWhatV043Sent`, against the v0.4.3 capture in
  `cmd/jevlin/testdata/search/full-a0ff6ca`); every key the router's fixtures send is either carried or
  named as not carried, with the reason (`TestEveryKeyTheRouterSendsIsDecided`);
  `skill_render.go`'s `skillTeachesQueryForms` is the switch for the skill's query-forms
  paragraphs, on since the router took the forms (`TestTheQueryFormsAreTaughtAsShipped`);
  `fetch.go` owns `fetch --stdin`: its strict request, the one POST to `/v1/fetch` through
  search's `routerClient` and `postRouter`, and the three answers only a fetch gets
  (`fetch_unavailable` is `connect`; a 404 or 405 is `fetch_not_supported` and a 503 with the
  router's code `fetch_disabled` is `fetch_disabled`, both `report`, while any other 503 stays a
  retry), guarded by
  `fetch_test.go`;
  `machine.go` owns the header, the action set
  and the exit/status correspondence; `report_json.go` owns `status`/`doctor`/`connect` JSON.
  `search_protocol_test.go` and `report_json_test.go` guard them.
  For participants: [reference, The envelope](docs/reference.md#the-envelope).
- **The trace bridge** — `agent_trace_common.js` owns the pipeline and is spliced into both
  JavaScript hosts (`opencode_plugin.js`, `pi_extension.ts`) by `renderAgentScript`;
  `hermes_hook.go` is Hermes' own, and sends less because its host payload carries less.
  `trace_boundaries_test.go`'s `TestEveryJSHostRendersTheSharedTraceSource` keeps the splice
  honest; `pi_extension_test.go` and `hermes_hook_test.go` guard the two adapters.
  For participants: [reference, The trace](docs/reference.md#the-trace).
- **Who is calling a Claude-format hook** — `hook.go` owns it: `claudeEntryEvent` names the five
  entry points `agents install` writes for Claude Code and the event each is installed under, and
  `runByAnotherHost` decides from the **payload**, never the environment. Cursor loads
  `~/.claude/settings.json` and runs those hooks with its own payload; a payload carrying
  `cursor_version`, or a `hook_event_name` that is not byte for byte the installed event, gets
  nothing written, nothing spawned, nothing printed and exit 0 — one release check per Cursor
  session start instead of two, and no Cursor command rewritten with a `claude-code` bridge. A payload that
  names no event is not evidence and is served as before. The three real Cursor payloads are in
  `cmd/jevlin/testdata/hook/`. `hook_caller_test.go`'s
  `TestOneCursorEventIsServedOnceWithBothHostsInstalled` is the double run,
  `TestLineageStandsDownForCursorWhateverTheToolName` holds the gate to the payload rather than
  the tool name, and `TestTheCallerGateNamesExactlyTheEventsTheInstallWrites` holds the event table to
  `claudeHooks`' own output so a wrong entry cannot silence a hook inside Claude Code itself.
  Codex sends Claude Code's own event names, so for a Claude Code entry `claudeEntryStandsDown`
  adds one rule to that payload test: a transcript that is one of Codex's rollout files
  (`isCodexRollout`) is Codex's, and gets nothing
  (`TestClaudeFormatHooksStandDownForTheRealCodexPayloads`, over the payloads captured in
  `cmd/jevlin/testdata/hook/`). Codex has entry points of its own, `hook codex <Event>`, in
  `codex_hook.go`: the command line names the caller, because no payload field does, and
  `codexEntryStandsDown` reads the payload only for contradiction — Cursor's signals, no event
  name, not an object, or a non-empty transcript that is not a rollout file; a null one is not
  evidence. `codexEntryEvent` is held to `codexHooks`' output by
  `TestTheCodexGateNamesExactlyTheEventsTheInstallWrites`, and
  `TestCodexEntriesStandDownForAnotherHostsPayload` and `TestWhatContradictsACodexEntry` hold the
  gate. Codex's PreToolUse answers only this installation's exact rendered search, with the bridge
  and an allow, and its exact rendered fetch, with an allow and the input as it came, and is
  silent for anything else (`TestCodexRewritesAndAllowsOnlyTheRenderedSearch`,
  `TestCodexAllowsTheRenderedFetchAsIs`). Claude Code's lineage hook allows the rendered fetch the
  same way (`allowRenderedFetch`), and Cursor's recognizer knows it as a form
  (`renderedFormsForShell`); `fetch_allow_test.go` holds all three to the exact command.
- **Whose lineage file a search may use** — `search.go`'s `searchTrace` owns the channel rule and
  the declared file's session guard; `lineage.go`'s `lineageForCwd` owns the walk, which with a
  session exported climbs past a file of another session to the searching session's own, and
  without one takes the nearest file of the searching harness. `lineage.go`'s `replaceViaTemp` is the one writer
  behind the lineage files and the window state: a failed write or rename removes
  its temporary file, which is `<file>.<pid>-<random>.tmp` and created exclusively (hard
  invariant 19), and the sweep takes only such a file (or the `<file>.<pid>.tmp` earlier versions
  wrote) of another pid older than `lineageMaxAge` — any lineage file's for a lineage write, only
  its own for the other two, since the stamp shares the state dir and the window state can live in
  a plugin root. Guards:
  `search_channel_test.go`, `lineage_session_test.go`, `lineage_declared_session_test.go` and
  `temp_cleanup_test.go`, all asserted on the bytes the router receives or the files left behind.
- **Setup** — `setup.go` owns the order (binary, previous
  installation, owner-only directories, adoption, config, connect, environment, agents).
  `setup_adopt.go` owns what counts as an installation and adoption by bundle — the
  identity (`state/`, `credentials.json`, and beside it the registration journal and the claim
  record) moves whole or not at all, and a destination
  identity is a typed conflict that stops setup non-zero before anything moves and before
  connect runs (`TestSetupStopsOnAnIdentityConflictBeforeConnect`). `setup_config.go` owns TOML quoting and
  the config policy (`planSetupConfig`: no file → a fresh format-1 file; a format-1 file that
  loads → byte-identical; a file with no `schema` line → set aside as `jevlin.toml.old` (`.old.1`,
  ... via `asideName`) and a fresh file written, never migrated; anything else → refused with
  the error and its path, nothing written). `setup_env.go` owns the one-well-formed-block
  profile rule and the Windows `setup-env.json` delta journal, over backends in
  `setup_env_unix.go` and `setup_env_windows.go`; `setup_targets.go` owns `-with`, which
  reaches every target kind. `setup_test.go` drives setup in-process against a sandbox
  and asserts the ownership set on every case, `TestSetupSecondRunIsANoOp` byte for byte;
  `TestSetupFilesNeverImportOSExec` keeps setup from running a process. The package's
  `TestMain` (`testmain_test.go`) refuses to run any test unless `os.UserConfigDir()` and
  the home directory resolve under a temporary test root.
  For participants: [guide, Setup](docs/guide.md#setup).
- **The install registry** — `targets.go` owns the interface, the kinds, the views and the
  slice; `agents.go` owns plan execution; the goldens prove a target's plan cannot drift
  silently, and the structural test proves the public ID set. `Detect` answers with **the
  signal** that made a host count — `detectCommand` for the command it is launched by,
  `detectConfigDir` for the directory it keeps — never a bare yes, because setup's "Found on
  this machine" and `agents status` both print it, and a host that is present under a name or in
  a directory the check did not look for must be arguable from what was printed. A config
  directory is evidence on the same footing as a command: it is where the skill and hooks go, so
  if it is there this client writes there either way.
  `host_detect_test.go` holds each host's signal to a literal and drives `-with cursor`,
  uninstall, plain setup — because uninstall removes by what was installed while setup restores by
  what is detected, and anything installed with `-with` must survive that round trip. A removal
  carries its host (`agentRemove.surface`) for the same reason a write does: `printPlan` groups by
  host, and a host with only files to delete must print under its own heading. `targets.go` also
  owns **the
  shell declaration**: per host and per OS, the set of shells that run its tool calls and what
  runs its hook commands, each cell established by documentation, source or a live run —
  `host_shells_test.go` holds the declaration to a written-out table so no cell moves without a
  reviewed diff. A hook cell nothing has established writes no hook at all: Codex's Windows cell
  is unknown, so its install there writes the skill and the block, says why in a note and does not
  refuse (`TestCodexOnAnUndeclaredHookRunnerWritesNoHooksAndExitsZero`). `shell_commands.go` renders every command for a declared shell and owns the
  quoting of the paths in it (never Go's `%q`, which is neither shell's); `skill_render.go`
  renders one form per shell a host runs, and the Bash form, with a line in the install plan,
  for a cell nobody has established. `host_exec_test.go` is what makes any of it
  true: it runs the rendered strings through real shells — bash, sh, Git Bash, both PowerShell
  editions through `-EncodedCommand`, `cmd`, and Hermes' own splitter — against a test build
  with loopback-only stubs, and compares the query the router received with the one that was
  sent.
  For participants: [agents, one section per host](docs/agents.md).
- **Lifecycle coordination** — `cmd/jevlin/lifecycle.go` owns the gate `H.lifecycle.lock`
  (a sibling of the installation, never inside it and never deleted), the one lock order
  (gate → `setup.lock` → `connect.lock`; for `uninstall -binary` the same sequence
  then `<resolved binary>.update.lock`, the same update-lock identity an upgrade holds after
  gate → `setup.lock`; the daily check takes its own `update_check.lock` first, in one attempt,
  and the upgrade's locks after it only to replace the binary, also in one attempt, so it can
  neither wait nor deadlock), how setup and connect pass the gate
  (a person's command waits at most five seconds; the detached child, `connect -resume`, which
  names itself by that flag and not by anything `spawnDetached` sets in its environment, makes
  one attempt and exits 0 recording nothing) and the exclusion a destructive operation holds:
  the gate, then every operation lock, located from the config only once the gate is held.
  `lifecycle_test.go`'s `TestConnectCannotStartUnderAHeldExclusion` proves an operation starting
  after the check meets the gate before it reads or writes anything; `TestSetupHoldsSetupLockThroughItsWholeRun` proves setup
  excludes a destructive operation until its closing message.

- **Uninstall** — `cmd/jevlin/uninstall.go` owns the order (plan everything, confirm,
  then integrations, environment, revocation, state, binary), what "this installation's" means
  for a skill, hook or profile block, the purge set and its target guard, and the typed
  confirmation `-yes` cannot supply; `setup_env.go` owns the profile-block removal and the
  compare-and-revert of the Windows user environment; `ownership.go` owns the one npm/native
  classifier setup, `uninstall -binary` and upgrade consult. `uninstall_test.go`'s
  `TestPurgeRefusesEveryConfirmationButTheExactOne`, `TestDefaultUninstallPreservesEveryParticipantByte`
  and `TestWindowsUninstallRevertsOnlyWhatSetupStillOwns` guard them. What a purge says it left
  is `sayLeftInHome`'s: after removing its own set, which includes the configs setup set aside
  (`jevlin.toml.old`, ...), it lists by full path every entry still in the installation
  directory as not this version's to remove, so a file an earlier version left is named without
  this client knowing its name; `uninstall_leftovers_test.go` guards it.
  **`ownership_match.go` owns which installation an integration belongs to**: one of this
  installation's binaries AND this installation's resolved config, because two installations
  share a binary whenever the second was made by running the first's copy, and matching on the
  binary alone would remove both installations' integrations from either one's uninstall. It is
  the profile block's rule, applied everywhere. The spellings come from the same per-shell
  renderers install writes with, never a hand-kept list, plus the `%q` and bare forms Claude
  Code's allow rules are written in and Hermes' bare POSIX path; the config is compared with
  `samePath`, because `%q` hands Windows doubled separators naming the same file.
  Codex's marked region names directories rather than a config, so `removeOurSandboxBlock`
  attributes it by its roots being this installation's (`codexRootsOwner`); a line of the
  participant's that install changed names the config in its mark instead, and goes back with the
  region it belongs to. **Every artifact this client
  writes names the installation that wrote it** — `TestEveryArtifactNamesTheInstallationThatWroteIt`
  — including the JavaScript adapters, through their `INSTALL_CONFIG` line. Neither teaches a
  command, and each runs the binary only to hand it something — a session's start to `hook start`,
  a finished turn to `hook turn` — so without that line neither would name its installation's
  config, and a disposable installation's purge could delete the main installation's copy. An
  artifact naming no
  installation is left and reported, never deleted on the assumption it is ours — with the file
  named and the one `agents install` that would stamp it, so the dead end self-heals.
  **The rule holds at install time too.** A host has one skill directory, and opencode and Pi one
  adapter file; a hook file holds a list, which is why hook entries need nothing more. An
  `agents install` for a second installation that overwrote the first's skill would make the
  later `agents uninstall` remove a skill that did by then name its config: the removal would be
  correct and the damage done at install. `leaveToItsOwner` (`agents.go`) sits in the two planners
  every writer of those files goes through — `agents prefer` included — and `buildUninstallPlan`
  takes another installation's files out of a host's removal, both through one reading,
  `foreignOwner`, and both in `uninstall`'s own sentence (`leftForeign`). Here the **config**
  decides and the binary does not: an installation whose binary moved still names its config and
  must be able to refresh its own skill. `agents status` asks the same question through
  `foreignHost` and says `belongsTo` — uninstall's own sentence without the removal verb — rather
  than "installed", because `Status` answers from the file existing and a host another
  installation set up would read as this one's. `agents_skill_ownership_test.go` guards it on real
  files, with `TestUninstallingOneInstallationLeavesAnothersIntegrations`.
  **A hook is owned per handler, not per group** (`entryOwnership`, `agents.go`): Claude Code's
  `/hooks` adds a participant's handler to the group already under the same matcher, often ours,
  and deciding ownership per group took that handler with ours on install, on uninstall and on an
  upgrade's re-render (issue #32). Out of a mixed group only our handler comes; the group keeps
  its place, its other handlers and its matcher, ours gets a group of its own on install, and the
  plan says so. `agents_hook_handler_test.go` drives it through the real commands for Claude Code
  and Codex, and holds Codex's renumbering note and its unknown-approval line to the shared group.
  **Codex numbers its hooks by place.** It keys an approval by an entry's index in its event's
  list, so `planHooksMerge` rewrites a Codex entry of ours where the first one stands
  (`replaceInPlace`) instead of appending it, and appends only to an event that has none of ours:
  no hook of anyone else's is renumbered by a refresh of ours (`TestAReplacedCodexHookKeepsItsIndex`),
  and a second install writes nothing (`TestASecondCodexInstallWritesNothing`). Uninstall says
  when removing ours moves another hook up a place.
  **An allow rule has one current spelling.** Adding a rule only when its exact text is absent
  means a rule whose text has changed is never seen as the same rule and stays beside its
  replacement for ever. `mergeAllowRules` replaces the rules `ruleIsOurs` recognizes — any
  spelling this client writes, for this installation's binary and config — with the
  current set, as a set, because `claudeAllowRules` writes three prefix forms of one permission
  and there is no pairing between old spellings and new. A set already equal to the current one
  is left exactly as it lies, so a second install still writes nothing. The removing half
  needed no change: `planHooksRemove` already asked `ruleIsOurs`.
  `agents_allow_rules_test.go` guards both, and takes its fixtures from `resolveEntry` rather
  than typing a config path — `resolveEntry` puts the path through `filepath.Abs`, so a typed
  POSIX path is a *different installation* on Windows.
  **One function reads a rendered path**, `unquoteRenderedPath`, and both halves of an
  attribution go through it: a helper that read a double-quoted word only through
  `strconv.Unquote` would fail a cmd-rendered Windows path on `\U`, and in the binary half that
  failure is OPEN, the more dangerous direction — so the two halves must never have separate
  readers. `ownership_test.go`'s `TestUninstallingOneInstallationLeavesAnothersIntegrations`
  drives two installations sharing a binary on each CI OS, plain and `-purge-state`, and asks the
  production matcher over decoded JSON rather than scanning bytes — a raw native path never
  appears in a JSON-escaped file. `TestARenderedPathIsReadBackWhicheverShellQuotedIt` pins every
  renderer's spelling as a unit case, so it needs no Windows runner to surface. **Naming a path is one
  reading too**: `describeOther` shows the DECODED reading, the last `unquoteRenderedPath`
  returns, because a double-quoted word yields the literal one first and a JSON-quoted Windows
  path is then spelled with every separator doubled. Matching was never affected — `samePath`
  reads them all — so only the sentence can be wrong;
  `TestTheInstallationNamedInAMessageIsTheDecodedReading` pins it as a unit case.
  **`namedInArtifact` is the one place that decides HOW an artifact is read** — a file that
  decodes as JSON is decoded, anything else is scanned as rendered text — and production and the
  tests both go through it: a hook file escapes a command twice, by the shell and then by JSON,
  so on POSIX a byte scan is right by luck and on Windows it reads `\"C:\\Users\\…\"` as a lone
  backslash. Never scan encoded bytes for a structure that can be decoded.
  `TestNoHookFileCanEnterTheAttributionPlan` pins the invariant that keeps `attributeRemoved`
  away from a hook file: it runs against the agnostic plan, whose probe command begins with a NUL
  byte and so prefix-matches no rendered command, so `planHooksRemove` never sets changed and
  never reaches its removal. Both halves are asserted, because either alone passes while the
  invariant is broken — and breaking it leaves a hook file of ours behind running a binary that
  is gone.
  For participants: [guide, Removing it and coming back](docs/guide.md#removing-it-and-coming-back).

- **Replacement and rollback** — `internal/selfupdate/replace.go` owns both transactions: on POSIX
  a durable same-directory copy, the candidate renamed over the binary, the canonical path run and
  validated, and only then the copy committed as `.previous`; on Windows the probed sequence
  (binary aside, candidate in, validate, aside replaces `.previous`) with `previous_in_use` when a
  process still runs `.previous`. `rollback.go` owns restoring a validated copy of `.previous`
  with no network. `cmd/jevlin/upgrade_locks.go` owns the gate, `setup.lock` and
  `<binary>.update.lock` an upgrade holds, and `cmd/jevlin/upgrade.go` owns the command:
  the launch classifier first, one operation deadline over everything after it, success only
  after the transaction commits, `Prepared.DiscardAfterInstall` as the only cleanup, and failure
  classes from typed kinds. `upgrade_test.go`'s
  `TestUpgradeCarriesOneOperationDeadlineThroughEveryStage` and
  `TestUpgradeCommandPrintsSuccessOnlyAfterTheCanonicalPathValidates` guard the command.
  `cmd/jevlin/upgrade_rerender.go` owns what follows a committed replacement: the
  host integrations **this installation owns** — `ownedHosts`, which is `ownership_match.go`'s
  rule read off the files through uninstall's own attribution, because `Status` calls a skill
  installed when the file merely exists — are rendered again by a **child process, the binary now
  at the launch path**, since the process running `upgrade` is the version being replaced and its
  tables are the old ones. Never before the transaction commits, never as a condition of the exit
  code, never a host that is another installation's or names none, and never a host that was not
  set up (the child is handed `-client` per owned host). `upgrade_rerender_test.go` records which
  version was at the path when the render was asked for — the observable that tells after-commit
  from before — in `TestAnUpgradeThatIsRolledBackLeavesTheHostFilesAsTheyWere` and
  `TestRollbackReRendersWithTheBinaryRolledBackTo`. `replace_test.go`'s
  `TestAFailedSecondUpgradeLeavesPreviousByteIdentical` and `acceptance_test.go`'s
  `TestReplacementAcceptanceWithTheRunningImage` (real processes, on every CI runner including
  Windows arm64) guard them. Two bounded retries, and no others. `candidate.go`: a candidate that
  has not answered `version` is asked once more, and only on a timeout decided by the package's
  own clock — a wrong version, a malformed line, a byte on stderr or a failure to start is
  evidence and is refused on one call — the frozen five-second budget is not raised, and two
  timeouts are `replacement_failed` (`retry`), never `candidate_invalid`
  (`candidate_retry_test.go`, whose evidence rows answer correctly on their second call so a retry
  would show as an acceptance). `replace.go`'s `moveAside`: the Windows move-aside waits about a
  second, five attempts, only for the sharing-violation and access-denied errors of a file
  something else is briefly holding (`transientlyHeld`, constant false off Windows); a read-only
  directory fails earlier, at `reserve` (`move_aside_test.go` for the policy on every OS,
  `rename_windows_test.go` for a real forced hold on the Windows runners, which CI runs without
  `-v` — to see them run, push a throwaway branch with a verbose step as a draft PR and close it
  unmerged).
  For participants: [guide, Upgrading](docs/guide.md#upgrading).

## Testing discipline — learned the hard way; hold them
- **A test's name is not its assertion.** A green test can encode the bug.
- **A review's reproduction is a vulnerability *demo* (green when vulnerable), not a guard.** A guard
  is green-on-fixed / red-on-vulnerable. Adopting one: invert polarity, give it a real assertion (not
  `t.Log` on both branches), drive the **package's own** code (never a bare `http.Client` — that tests
  `net/http`), and rename it so a green test isn't still called `…LeaksToken`.
- **Injection-check every non-obvious guarantee:** revert the guard, confirm red (naming the
  `file:line`), restore. A guarantee nobody has watched fail is a hypothesis.
- **A source-reading test can't be injection-checked with `go test -overlay`** (overlay is compile-time;
  the test parses the real file). Disk-edit + restore instead.
- **Count the right observable:** connections dialed, not handler invocations; bytes on the wire, not
  "a call was made." Prove a destructive outcome unreachable by enumerating the input domain.
- **Distrust hand-written provider fixtures**; a test that would run against a stale fixture should
  refuse, not invent a value.
- **A mutation is evidence only if it landed.** Commit the known-good implementation *before* any
  mutation, so there is a baseline to restore from. Apply the mutation and confirm with `git diff
  --stat` that it actually changed the file; a mutation that did not apply, or that does not
  compile, proves nothing and must not be reported as though it did. Restore only from committed
  bytes — never by retyping what you think was there — and rerun green. Report the baseline SHA,
  the mutation, the test that went red, and the failing line.
- **Scope the search wide before trusting a targeted fix.** `credentials.go`'s bare client (invariant
  3) survived a redirect-guard review aimed at the AS-facing clients because that review's scope was
  "the clients that carry a credential to the AS" — true, but too narrow; the login probe carries a
  credential to the *router*. The module-wide `cmd/jevlin/boundary_test.go` sweep found it where the targeted
  review could not, on the first run. Prefer a structural, scope-agnostic guard over a review's
  enumerated list whenever the two disagree about where the boundary is.

## Import boundaries (`cmd/jevlin/boundary_test.go`)
- `pkg/` ⊄ `cmd/` (invariant 8): the `cmd/` root may reach a `pkg/` package's dependency graph, but no
  `pkg/` code imports the wrapper.
- No package in the module graph reaches the chain application or the Cosmos/CometBFT SDK
  (invariant 9) — `TestNoChainImportsAnywhere`.
- `internal/selfupdate` ⊄ `cmd/`, `pkg/auth`, `pkg/platform`: the self-updater fetches public release
  assets and holds no participant credential, so it reaches neither the wrapper nor the key and
  authorization store nor the platform client — `TestSelfupdateImportsNoWrapperAndNoCredential` and
  the `selfupdate-boundary` depguard rule. Its one HTTP client (`NewHTTPClient`) is invariant 3's
  explicit choice: it follows a redirect only over HTTPS, at most five hops, and only to
  `api.github.com`, `github.com`, `objects.githubusercontent.com` and
  `release-assets.githubusercontent.com`; anything else is refused with advice to reinstall. The
  release origin is compiled in and no environment variable redirects it. `checksums.txt` is trusted
  only under `checksums.txt.sig`, an Ed25519 signature by a compiled-in release key
  (`SignedVerifier`, the default); `SHA256Verifier` alone is integrity, not authenticity, and is
  never a production verifier.
- State YOUR forbidden edges here and nowhere else. Don't import another repo's edges; a boundary with
  no argument you can state should not exist.

## Conventions
- Prose over bullet-soup in docs: state the rule, then the reason.
- Commits are **single-purpose**; the message carries the reasoning — what was rejected and why. **No
  `Co-Authored-By` or `Claude-Session` trailers.**
- **An issue body has a shape**, because a defect is read by
  whoever fixes it months later: provenance first (where it was seen — the soak and its condition,
  the platform, the version or the CI run and head), then `**What happened.**` with the evidence in
  a fenced block and the code named (`lineagePath`, `lineage.go`) — the reproduction in it, or a
  `**Steps to reproduce.**` list when there is one — then `**Expected.**`, which is what the fixer
  implements against, `**Likely cause.**` where there is one, and `**Why it matters.**` in the
  participant's terms. It closes with `Severity:` — `cosmetic`, `minor` or `must-fix` — and the
  path it is on, because that is what triage sorts by. The order is the bug form's, so a triager
  reads every issue in one order: outside reporters use the form, its order matches GitHub's
  default template, and a terminal filer can follow any order. Someone filing from a browser uses
  one of the two forms under `.github/ISSUE_TEMPLATE/`: `bug_report.yml` asks for the same things
  in the same order, with the affected area and logs as fields of their own, and applies `bug`;
  `feature_request.yml` has its own shape (motivation, proposal, alternatives) and applies
  `enhancement`. `gh issue create` renders neither form, so an issue filed that way carries the
  shape by hand and passes `--label bug` or `--label enhancement`, the label the form would have
  applied.
- The flow: bug and feature work starts from an issue where one applies — release-only work and
  documentation maintenance need not invent one; branch from `main` as it stands, never from an
  unmerged branch; open a PR with the template filled in; all eight CI checks green (the `test`
  matrix on Linux, macOS, Windows and Windows arm64, plus `race`, `cross`, `lint` and `vuln`);
  merge through GitHub, which produces a merge commit; and tag only when cutting a release, per
  `docs/RELEASING.md`.
- `unsafe` is admitted in exactly one file, `cmd/jevlin/setup_env_windows.go`, where the
  Windows environment broadcast must hand `SendMessageTimeoutW` a string's address;
  `TestOnlyTheEnvironmentBroadcastImportsUnsafe` fails on any other import, so a second use is an
  exception argued in review, never a quiet import.
- **Tests never contact a real host.** The router and the platform are `httptest`
  stubs on loopback, in every test. A test that reaches a real service passes or fails with
  that service's state rather than the code's, and sends whatever it carries to a host that
  keeps it. The PR template asks about it; `internal/networkfence`'s `Guard`, called from the
  `TestMain` of every package whose tests build a network client, refuses any non-loopback
  dial, so a test that tries fails rather than connects.
- **A reviewed commit is never amended or rebased.** A correction is a new commit that says
  what it corrects, so a review always has a fixed object: the head a reviewer examined is
  still in the history, and what changed since is exactly the commits after it.
- **Nothing here carries an issue or version number from the predecessor project.** A reader
  of a README, a guide, a template or a code comment cannot open an issue in the repository
  this one was imported from, or place one of its release numbers, so the text states the
  rule, and the defect it answers, in its own words. Comments that once pointed at such an
  issue now describe the defect instead; the captured host payloads under `testdata` name
  this client's binary, with the rewrite said where a test loads them.
- **`npm/package.json`'s version and `CHANGELOG.md`'s release headings move only in a release
  PR**, so the commit to tag is unambiguous: the one where the version, the heading and the
  tag agree. Between releases a change a participant should hear about may add a bullet under
  `## Unreleased`, which the release PR turns into its own heading.
- Keep diffs scoped. **Don't copy a rule you can't explain** — an inherited rule without its argument is
  one the next person deletes.
