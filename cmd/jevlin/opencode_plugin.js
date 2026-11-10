// jevlin lineage plugin for opencode — installed by `jevlin agents install`.
//
// Runs INSIDE opencode's own runtime (nothing extra to install). Before each
// bash call that runs our search, it prefixes the command with
// JEVLIN_TRACE_BRIDGE=<envelope> carrying opencode's REAL session
// identity — hashed with the same domain-separated SHA-256 as the Go binary, so the raw
// id never leaves the machine — plus a compaction generation and the
// assistant text before the call. The same shape the Claude Code hook
// builds; `jevlin search` reads the variable and sends it as the
// `trace` field of /v1/search.
//
// The scrub, the caps and the recognizer below the marker are spliced in
// from cmd/jevlin/agent_trace_common.js at install time — one
// repository source of truth shared with every other JavaScript host. This
// file owns only what is opencode-specific: which events carry the session,
// where its messages come from, and how a tool call is rewritten.
//
// One file serves opencode 1 and opencode 2, which have different plugin
// APIs: see the default export at the end. On opencode 2 the bridge is set
// in the environment of the shell the call starts, and the command is left
// as the model wrote it.
//
// FAIL-OPEN: any error leaves the tool call untouched and the search runs
// with its per-shell trace instead. Delete this file (or `jevlin agents
// uninstall`) to remove it; JEVLIN_TRACE=off disables tracing entirely.

// {{TRACE_COMMON}}

// For hostShell's usability mirror only: whether a shell opencode would
// resolve exists as a file. Static like the shared source's own imports —
// hostShell answers inside a synchronous rewrite.
import { statSync as shellStatSync } from "node:fs"
import { delimiter as shellPathDelimiter, join as shellPathJoin } from "node:path"

// The shell opencode runs its bash tool in on THIS machine, written in by
// the installer from the host's declaration. The bridge is an environment
// assignment, and its syntax is the shell's: a POSIX prefix handed to
// PowerShell is looked up as a program name and the search never runs.
const HOST_SHELL = "{{HOST_SHELL}}"

// The names ShellSelect's META table reads as a POSIX login shell and as
// PowerShell, kept to the shells whose bridge syntax this adapter writes.
const POSIX_SHELL_NAMES = new Set(["bash", "sh", "zsh", "ksh", "dash"])
const POWERSHELL_NAMES = new Set(["pwsh", "powershell"])

// hostShell answers PER CALL on Windows, where the install-time declaration
// alone is wrong for part of the fleet: opencode resolves its shell at
// runtime (packages/core/src/shell/select.ts), and `$SHELL` wins whenever it
// is set and usable — environment() is select(process.env.SHELL) — before
// win() tries pwsh, powershell, Git Bash and cmd. Started from Git Bash,
// which exports SHELL=/usr/bin/bash, opencode runs its bash tool in bash,
// and the PowerShell wrapper the Windows declaration bakes in was read by
// bash as a command named `$env:JEVLIN_TRACE_BRIDGE`: every search failed.
// So the signal opencode itself consults is mirrored here, usability
// included: select.ts runs $SHELL only when executable() resolves it — a
// unix-rooted path named bash resolves to Git Bash's bash.exe through git
// on PATH, any other resolved absolute path must exist as a file, a bare
// name is found on PATH — and falls back to win()'s PowerShell when it
// cannot. A $SHELL named like a POSIX shell that this cannot see the same
// way therefore keeps the installer's declared answer, because answering
// "posix" for a shell opencode will not run hands PowerShell a prefix it
// reads as a program name: the very failure this resolution exists to fix,
// in the opposite direction. What can still miss is a shell opencode's own
// config chose, which this plugin cannot see; that case behaves exactly as
// the install-time declaration always did.
const hostShell = () => {
  if (process.platform !== "win32" || HOST_SHELL !== "powershell") return HOST_SHELL
  const s = process.env.SHELL
  if (typeof s !== "string" || s === "") return HOST_SHELL
  const base = s.split(/[\\/]/).pop().toLowerCase().replace(/\.exe$/, "")
  if (POSIX_SHELL_NAMES.has(base) && usablePosixShell(s, base)) return "posix"
  if (POWERSHELL_NAMES.has(base)) return "powershell"
  return HOST_SHELL
}

const shellIsFile = (p) => {
  try {
    return shellStatSync(p).isFile()
  } catch {
    return false
  }
}

// Git Bash's bash.exe the way select.ts gitbash() finds it: git on PATH,
// then <git>/../../bin/bash.exe. "" when either is missing.
const gitBashExe = () => {
  for (const dir of (process.env.PATH || "").split(shellPathDelimiter)) {
    if (!dir) continue
    const git = shellPathJoin(dir, "git.exe")
    if (!shellIsFile(git)) continue
    const bash = shellPathJoin(git, "..", "..", "bin", "bash.exe")
    if (shellIsFile(bash)) return bash
  }
  return ""
}

const onShellPath = (name) => {
  for (const dir of (process.env.PATH || "").split(shellPathDelimiter)) {
    if (dir && (shellIsFile(shellPathJoin(dir, name)) || shellIsFile(shellPathJoin(dir, name + ".exe")))) return true
  }
  return false
}

// usablePosixShell mirrors select.ts full()+executable() for the POSIX
// names this adapter writes a prefix for: would opencode actually run this
// $SHELL, rather than fall back to PowerShell?
const usablePosixShell = (s, base) => {
  if (/[\\/]/.test(s)) {
    // A path. Unix-rooted bash resolves to Git Bash when git is on PATH
    // (SHELL=/usr/bin/bash is what Git Bash itself exports); otherwise the
    // path itself must exist as a file.
    if (s.startsWith("/") && base === "bash" && gitBashExe() !== "") return true
    return shellIsFile(s)
  }
  // A bare name: bash through Git Bash first, then any of them on PATH.
  if (base === "bash" && gitBashExe() !== "") return true
  return onShellPath(s)
}

// The longest command line a rewritten command may reach on this platform.
// Windows refuses to start a process whose command line is over 32,767
// characters (node reports ENAMETOOLONG), and the envelope cap alone allows
// a bridge of ~65K characters; the headroom under 32,767 covers the host's
// own quoting. 0 is no limit: no POSIX host limit is within the envelope
// cap's reach.
const HOST_COMMAND_CHAR_LIMIT = process.platform === "win32" ? 30000 : 0

// The installation this file belongs to: the config `jevlin agents
// install` resolved when it wrote this. Nothing here reads it — the plugin
// runs no command of ours and needs no config — it is how `uninstall` knows
// whose file this is. Every other artifact names its installation in a
// command it teaches; this one teaches none, so before this line existed an
// uninstall had nothing to match and removed another installation's plugin.
// Two installations share this one path, so it names the one that ran
// `agents install` last, and the other leaves it alone.
const INSTALL_CONFIG = "{{INSTALL_CONFIG}}"

// The binary of the installation above, for the one thing this plugin hands
// it: a finished turn, on stdin (see reportTurn). Empty when the file was
// rendered with none, and then no turn is reported.
const JEVLIN_BIN = "{{JEVLIN_BIN}}"

// startSession hands the binary a session's start: `hook start opencode`,
// which starts, once a day, the release check — what Claude Code's
// session-start and Stop hooks do between them. Nothing is piped and
// nothing is read back. Fire and forget. It runs when this plugin loads
// (opencode started, whatever session it opens), when a top-level session is
// created, and when a top-level session goes idle: a session resumed day
// after day is never created again, and its idle is the one event it still
// sends. The binary's stamp decides whether anything runs, so the extra
// starts cost one process each and no request. A subagent's child
// session is the same person's same day and starts nothing.
const startSession = async () => {
  if (!JEVLIN_BIN) return
  const { spawn } = await import("node:child_process")
  const args = ["hook"]
  if (INSTALL_CONFIG) args.push("-config", INSTALL_CONFIG)
  args.push("start", "opencode")
  const child = spawn(JEVLIN_BIN, args, { stdio: "ignore", windowsHide: true })
  child.on("error", () => {})
  child.unref()
}

// What a tool part's state says about how the call went.
const toolOutcome = (state) => (state?.status === "completed" ? true : state?.status === "error" ? false : undefined)
const toolMillis = (state) => {
  const a = state?.time?.start
  const b = state?.time?.end
  return typeof a === "number" && typeof b === "number" && b >= a ? Math.round(b - a) : 0
}

// turnOf assembles the turn that began at the session's LAST user message,
// from opencode's own record of it: what the user asked, what the assistant
// wrote, the tools it called — by name, time and outcome, and nothing of
// what they were given or returned — and where our searches fell. It builds
// only those fields; a tool part's input and output are never read into the
// result. The text is raw here: the binary scrubs and caps every piece
// before anything is written (turn_hosts.go).
const turnOf = (messages) => {
  let start = -1
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.info?.role === "user") {
      start = i
      break
    }
  }
  if (start < 0 || typeof messages[start].info.id !== "string") return null
  const textOf = (parts) =>
    (parts ?? [])
      .filter((p) => p?.type === "text" && typeof p.text === "string" && !p.synthetic && !p.ignored)
      .map((p) => p.text)
      .join("\n")
  const turn = { turn: messages[start].info.id, status: "completed", user_text: textOf(messages[start].parts), steps: [] }
  let input = 0
  let output = 0
  let last = null
  for (const m of messages.slice(start + 1)) {
    if (m?.info?.role !== "assistant") continue
    last = m
    if (typeof m.info.modelID === "string") turn.model = m.info.modelID
    const t = m.info.tokens
    if (t) {
      input += (t.input ?? 0) + (t.cache?.read ?? 0) + (t.cache?.write ?? 0)
      output += t.output ?? 0
    }
    for (const p of m.parts ?? []) {
      if (p?.type === "text" && typeof p.text === "string" && p.text !== "" && !p.synthetic && !p.ignored) {
        turn.steps.push({ kind: "assistant", text: p.text })
      } else if (p?.type === "tool") {
        const ms = toolMillis(p.state)
        const ok = toolOutcome(p.state)
        // The command is looked at for one thing: whether it is our search.
        if (p.tool === "bash" && needsTraceBridge(p.state?.input?.command)) {
          turn.steps.push({ kind: "search", call: p.callID, ms, ok })
        } else {
          turn.steps.push({ kind: "tool", name: p.tool, ms, ok })
        }
      }
    }
  }
  if (last === null) return null
  // The text after the last tool call is the answer; it is sent as such, once.
  let n = turn.steps.length
  while (n > 0 && turn.steps[n - 1].kind === "assistant") n--
  if (n > 0) {
    turn.final_text = turn.steps
      .slice(n)
      .map((s) => s.text)
      .join("\n")
    turn.steps = turn.steps.slice(0, n)
  }
  const err = last.info.error
  if (err) {
    turn.status = err.name === "MessageAbortedError" ? "interrupted" : "failed"
    delete turn.final_text
  }
  if (input > 0 || output > 0) turn.usage = { input_tokens: input, output_tokens: output }
  return turn
}

// reportTurn pipes one finished turn to the binary's `hook turn opencode`.
// On stdin: the text is never an argument and never a file of this plugin's.
// Fire and forget — the binary decides whether the installation opted in,
// whether a search was served in this turn, and what may be sent.
const reportTurn = async (turn) => {
  if (!JEVLIN_BIN) return
  const { spawn } = await import("node:child_process")
  const args = ["hook"]
  if (INSTALL_CONFIG) args.push("-config", INSTALL_CONFIG)
  args.push("turn", "opencode")
  const child = spawn(JEVLIN_BIN, args, { stdio: ["pipe", "ignore", "ignore"], windowsHide: true })
  child.on("error", () => {})
  child.stdin.on("error", () => {})
  child.stdin.end(JSON.stringify(turn))
  child.unref()
}

// opencode 1: the hooks it calls as `server`, and what its client and
// events look like there. Unchanged since before opencode 2 existed.
const jevlinV1 = async ({ client }) => {
  // sessionID -> how many times this session's context window has compacted.
  const compactions = new Map()
  // sessionID -> the session that started it, or "" for one nothing started.
  // opencode's task tool runs a subagent as a child session whose `parentID`
  // is the session that called it; a tool call names only its own session,
  // so the parent is learned from the session's creation event when this
  // plugin saw it and asked for once when it did not.
  // sessionID -> the id of the user message whose turn a search of ours was
  // last threaded into, and -> the one whose turn was last reported. A turn
  // is reported only if it searched, and once.
  const searched = new Map()
  const reported = new Map()
  // sessionID|turn of a report under way. opencode can say a session went idle
  // twice in a row (both events below), and the first is still waiting on a
  // lookup when the second arrives; this keeps it to one report.
  const reporting = new Set()
  const parents = new Map()
  const parentOf = async (sid) => {
    if (parents.has(sid)) return parents.get(sid)
    let parent = ""
    try {
      // opencode's client does not throw on an HTTP error: it answers with
      // `error` and no `data`. Only a session it actually returned — one that
      // carries this id — says anything about a parent, so only that is
      // cached. Anything else is asked again at the next search.
      const info = (await client.session.get({ path: { id: sid } }))?.data
      if (info?.id === sid) {
        if (typeof info.parentID === "string") parent = info.parentID
        parents.set(sid, parent)
      }
    } catch {
      // Not cached: a session that could not be read now may be readable at
      // the next search. This one goes out without a parent.
    }
    return parent
  }
  startSession().catch(() => {})
  return {
    event: async ({ event }) => {
      try {
        // The session went idle: the turn is over. Reported only when a search
        // of ours was threaded into it, so a turn that never searched costs
        // no read of the session and starts no process.
        // opencode says so twice over: `session.idle`, which its source marks
        // deprecated, and `session.status` with an idle status, which replaces
        // it. Either is taken; a turn is reported once whichever arrives.
        if (event?.type === "session.idle" || (event?.type === "session.status" && event?.properties?.status?.type === "idle")) {
          const sid = event?.properties?.sessionID
          const turnId = typeof sid === "string" ? searched.get(sid) : undefined
          // A subagent runs as a child session. Its turn is not the user's,
          // and a subagent's own records are not sent (hard invariant 2), so
          // only a session known to have no parent reports one. A parent this
          // plugin could not learn counts as one: the turn is left unreported,
          // to be tried again at the next idle.
          const key = sid + "|" + turnId
          if (turnId && reported.get(sid) !== turnId && !reporting.has(key)) {
            reporting.add(key)
            try {
              await parentOf(sid)
              if (parents.get(sid) === "") {
                const messages = (await client.session.messages({ path: { id: sid } }))?.data ?? []
                const turn = turnOf(messages)
                if (turn && turn.turn === turnId) {
                  reported.set(sid, turnId)
                  await reportTurn({ session: sid, ...turn })
                }
              }
            } catch {
              // A turn that could not be read is left unreported, to be
              // tried again at the next idle; it does not cost the session
              // start below.
            } finally {
              reporting.delete(key)
            }
          }
          // A session known to be top-level starts the binary's session
          // start at every idle; one whose parent is not yet known is left to
          // its creation or the plugin's load, never asked about here.
          if (typeof sid === "string" && parents.get(sid) === "") await startSession()
        }
        if (event?.type === "session.created" || event?.type === "session.updated") {
          const info = event?.properties?.info
          if (typeof info?.id === "string" && info.id) parents.set(info.id, typeof info.parentID === "string" ? info.parentID : "")
          if (event.type === "session.created" && !(typeof info?.parentID === "string" && info.parentID)) await startSession()
        }
        if (event?.type === "session.compacted") {
          const sid = event?.properties?.sessionID ?? event?.properties?.info?.id
          if (typeof sid === "string" && sid) compactions.set(sid, (compactions.get(sid) ?? 0) + 1)
        }
      } catch {
        // fail-open
      }
    },
    "tool.execute.before": async (input, output) => {
      try {
        if (!input || input.tool !== "bash") return
        const cmd = output?.args?.command
        if (!needsTraceBridge(cmd)) return
        const sid = input.sessionID
        if (typeof sid !== "string" || sid === "") return
        const gen = compactions.get(sid) ?? 0
        const env = { v: 1, harness: "opencode", session_id: traceHash(sid), window: gen > 0 ? String(gen) : "none" }
        if (typeof input.callID === "string" && input.callID !== "") env.call_id = traceHash(sid + "|" + input.callID)
        // A subagent names the session that started it, by the id that
        // session's own searches carry.
        const parent = await parentOf(sid)
        if (parent && parent !== sid) env.parent_session_id = traceHash(parent)
        // The assistant text before this call, from the session's messages.
        try {
          const messages = (await client.session.messages({ path: { id: sid } }))?.data ?? []
          // The turn is the user message this search answers to: the last one
          // in the session. Its id, hashed with the session's, is the turn id
          // — the same one the turn end is reported under.
          for (let i = messages.length - 1; i >= 0; i--) {
            const info = messages[i]?.info
            if (info?.role === "user" && typeof info.id === "string" && info.id !== "") {
              env.turn_id = traceHash(sid + "|" + info.id)
              searched.set(sid, info.id)
              break
            }
          }
          for (let i = messages.length - 1; i >= 0; i--) {
            const m = messages[i]
            if (m?.info?.role !== "assistant") continue
            const text = prepareTraceHistory(m.parts ?? [])
            if (text === null) break
            if (text) {
              env.history = [{ role: "assistant", text }]
              break
            }
          }
        } catch {
          // no history is fine; the ids still thread the search
        }
        if (traceOff()) return
        const shell = hostShell()
        const bridge = traceBridge(env, bridgeBudgetFor(cmd, shell, HOST_COMMAND_CHAR_LIMIT))
        if (bridge === null) return
        // H-R4: a bridge already on the command is not ours until we put it
        // there. Every assignment we can prove standalone is removed and
        // this call's own is prepended; a command carrying one we cannot
        // remove with certainty is left exactly as it is.
        const rewritten = withTraceBridge(cmd, bridge, shell)
        if (rewritten === null) return
        output.args.command = rewritten
      } catch {
        // fail-open: the search runs untraced rather than not at all
      }
    },
  }
}

// ── opencode 2 ──────────────────────────────────────────────────────────
//
// opencode 2 has a different plugin API and does not run an opencode 1
// plugin; it still loads this same file from the same directory, and takes
// the default export's `setup`. Everything below was written against a real
// opencode 2.2.6 session, not its documentation alone. What differs:
//
//   - the shell tool is named `shell` (a model that calls `bash` is repaired
//     to it), and its call arrives as one event: tool, sessionID, messageID,
//     id (the call id) and a mutable `input`;
//   - a session's messages are a flat list by `type`. A user message has
//     `text`; an assistant message has `content`, whose tool entries carry
//     `name`, `id`, `state` and `time`;
//   - a turn's end is `session.execution.succeeded`, `.failed` or
//     `.interrupted`. `session.idle` is declared and never sent;
//   - a client call rejects on error where opencode 1 answered `{error}`.
//
// The bridge travels differently too. opencode 2 lets a plugin set the
// environment of the shell a tool call starts (`create.before`), which is
// what the bridge is: an environment variable. So the command the user is
// shown, and the one their permission rules match, is the one the model
// wrote, with no prefix and no shell syntax to get right. That hook is told
// the command and not which call it belongs to, so the call's hook leaves
// the bridge under the command's exact text for it to collect.

// How long a prepared bridge waits for its shell to start. A call the user
// is asked to approve sits at the prompt for as long as they take; past this
// it runs untraced rather than under a bridge prepared for something else.
const V2_PENDING_MILLIS = 10 * 60 * 1000
const V2_PENDING_MAX = 64

// The longest bridge an environment variable may carry. Windows caps one
// variable at 32,767 characters; nothing a POSIX host imposes is within the
// envelope cap's reach, and null is traceBridge's "no limit".
const V2_BRIDGE_CHAR_LIMIT = process.platform === "win32" ? 30000 : null

const v2IsShellTool = (name) => name === "shell" || name === "bash"

// turnOfV2 is turnOf for opencode 2's message list: the same fields, built
// the same way, from the turn that began at the LAST user message. `status`
// is how the execution ended, which opencode 2 says in the event's name.
const turnOfV2 = (messages, status) => {
  let start = -1
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.type === "user") {
      start = i
      break
    }
  }
  if (start < 0 || typeof messages[start].id !== "string") return null
  const turn = { turn: messages[start].id, status: "completed", user_text: typeof messages[start].text === "string" ? messages[start].text : "", steps: [] }
  let input = 0
  let output = 0
  let seen = false
  for (const m of messages.slice(start + 1)) {
    if (m?.type !== "assistant") continue
    seen = true
    if (typeof m.model?.id === "string") turn.model = m.model.id
    const t = m.tokens
    if (t) {
      input += (t.input ?? 0) + (t.cache?.read ?? 0) + (t.cache?.write ?? 0)
      output += t.output ?? 0
    }
    for (const p of m.content ?? []) {
      if (p?.type === "text" && typeof p.text === "string" && p.text !== "") {
        turn.steps.push({ kind: "assistant", text: p.text })
      } else if (p?.type === "tool") {
        const a = p.time?.ran ?? p.time?.created
        const b = p.time?.completed
        const ms = typeof a === "number" && typeof b === "number" && b >= a ? Math.round(b - a) : 0
        const ok = p.state?.status === "completed" ? true : p.state?.status === "error" ? false : undefined
        // The command is looked at for one thing: whether it is our search.
        if (v2IsShellTool(p.name) && needsTraceBridge(p.state?.input?.command)) {
          turn.steps.push({ kind: "search", call: p.id, ms, ok })
        } else {
          turn.steps.push({ kind: "tool", name: p.name, ms, ok })
        }
      }
    }
  }
  if (!seen) return null
  let n = turn.steps.length
  while (n > 0 && turn.steps[n - 1].kind === "assistant") n--
  if (n > 0) {
    turn.final_text = turn.steps
      .slice(n)
      .map((s) => s.text)
      .join("\n")
    turn.steps = turn.steps.slice(0, n)
  }
  if (status !== "completed") {
    turn.status = status
    delete turn.final_text
  }
  if (input > 0 || output > 0) turn.usage = { input_tokens: input, output_tokens: output }
  return turn
}

const jevlinV2 = async (ctx) => {
  const compactions = new Map()
  const searched = new Map()
  const reported = new Map()
  const reporting = new Set()
  const parents = new Map()
  // command text -> the bridges prepared for calls of it that have not
  // started their shell yet, oldest first.
  const pending = new Map()
  const prune = (now) => {
    for (const [cmd, queue] of pending) {
      while (queue.length && now - queue[0].at > V2_PENDING_MILLIS) queue.shift()
      if (!queue.length) pending.delete(cmd)
    }
    while (pending.size > V2_PENDING_MAX) pending.delete(pending.keys().next().value)
  }
  const parentOf = async (sid) => {
    if (parents.has(sid)) return parents.get(sid)
    let parent = ""
    try {
      const info = await ctx.session.get({ sessionID: sid })
      if (info?.id === sid) {
        if (typeof info.parentID === "string") parent = info.parentID
        parents.set(sid, parent)
      }
    } catch {
      // Not cached: asked again at the next search.
    }
    return parent
  }

  await ctx.tool.hook("execute.before", async (event) => {
    // A throw here fails the user's tool call, which opencode 1 never did.
    try {
      if (!event || !v2IsShellTool(event.tool)) return
      const cmd = event.input?.command
      if (!needsTraceBridge(cmd)) return
      const sid = event.sessionID
      if (typeof sid !== "string" || sid === "") return
      const gen = compactions.get(sid) ?? 0
      const env = { v: 1, harness: "opencode", session_id: traceHash(sid), window: gen > 0 ? String(gen) : "none" }
      if (typeof event.id === "string" && event.id !== "") env.call_id = traceHash(sid + "|" + event.id)
      const parent = await parentOf(sid)
      if (parent && parent !== sid) env.parent_session_id = traceHash(parent)
      try {
        const messages = (await ctx.session.context({ sessionID: sid })) ?? []
        for (let i = messages.length - 1; i >= 0; i--) {
          const m = messages[i]
          if (m?.type === "user" && typeof m.id === "string" && m.id !== "") {
            env.turn_id = traceHash(sid + "|" + m.id)
            searched.set(sid, m.id)
            break
          }
        }
        for (let i = messages.length - 1; i >= 0; i--) {
          const m = messages[i]
          if (m?.type !== "assistant") continue
          const text = prepareTraceHistory(m.content ?? [])
          if (text === null) break
          if (text) {
            env.history = [{ role: "assistant", text }]
            break
          }
        }
      } catch {
        // no history is fine; the ids still thread the search
      }
      if (traceOff()) return
      // H-R4: a command that already names the bridge is not carrying ours.
      // The environment cannot override an assignment written into the
      // command, so that one case is rewritten as opencode 1's is: every
      // assignment that can be proved standalone is removed and this call's
      // own is prepended, or the command is left exactly as it is.
      if (cmd.includes("JEVLIN_TRACE_BRIDGE")) {
        const shell = hostShell()
        const bridge = traceBridge(env, bridgeBudgetFor(cmd, shell, HOST_COMMAND_CHAR_LIMIT))
        if (bridge === null) return
        const rewritten = withTraceBridge(cmd, bridge, shell)
        if (rewritten !== null) event.input.command = rewritten
        return
      }
      const bridge = traceBridge(env, V2_BRIDGE_CHAR_LIMIT)
      if (bridge === null) return
      const now = Date.now()
      prune(now)
      const queue = pending.get(cmd) ?? []
      queue.push({ bridge, at: now })
      pending.set(cmd, queue)
    } catch {
      // fail-open: the search runs untraced rather than not at all
    }
  })

  await ctx.shell.hook("create.before", (shell) => {
    try {
      const queue = pending.get(shell?.command)
      if (!queue) return
      const now = Date.now()
      let next = queue.shift()
      while (next && now - next.at > V2_PENDING_MILLIS) next = queue.shift()
      if (!queue.length) pending.delete(shell.command)
      if (!next || !shell.env) return
      // The shell's own environment may turn tracing off where this
      // process's does not: opencode 2 runs plugins in a long-lived service.
      if (["off", "0", "false"].includes(String(shell.env.JEVLIN_TRACE ?? "").trim().toLowerCase())) return
      shell.env.JEVLIN_TRACE_BRIDGE = next.bridge
    } catch {
      // fail-open
    }
  })

  const ended = async (sid, status) => {
    if (typeof sid !== "string" || sid === "") return
    const turnId = searched.get(sid)
    const key = sid + "|" + turnId
    if (turnId && reported.get(sid) !== turnId && !reporting.has(key)) {
      reporting.add(key)
      try {
        await parentOf(sid)
        if (parents.get(sid) === "") {
          const messages = (await ctx.session.context({ sessionID: sid })) ?? []
          const turn = turnOfV2(messages, status)
          if (turn && turn.turn === turnId) {
            reported.set(sid, turnId)
            await reportTurn({ session: sid, ...turn })
          }
        }
      } catch {
        // left unreported, to be tried again at the session's next end
      } finally {
        reporting.delete(key)
      }
    }
    if (parents.get(sid) === "") await startSession()
  }

  const abort = new AbortController()
  const types = ["session.created", "session.deleted", "session.compaction.ended", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted"]
  ;(async () => {
    try {
      for await (const event of ctx.event.subscribe({ types, signal: abort.signal })) {
        try {
          const sid = event?.data?.sessionID
          if (typeof sid !== "string" || sid === "") continue
          if (event.type === "session.created") {
            const parent = typeof event.data.parentID === "string" ? event.data.parentID : ""
            parents.set(sid, parent)
            if (!parent) await startSession()
          } else if (event.type === "session.compaction.ended") {
            compactions.set(sid, (compactions.get(sid) ?? 0) + 1)
          } else if (event.type === "session.deleted") {
            for (const m of [compactions, searched, reported, parents]) m.delete(sid)
          } else if (event.type === "session.execution.succeeded") {
            await ended(sid, "completed")
          } else if (event.type === "session.execution.interrupted") {
            await ended(sid, "interrupted")
          } else if (event.type === "session.execution.failed") {
            await ended(sid, "failed")
          }
        } catch {
          // fail-open, one event at a time
        }
      }
    } catch {
      // the subscription ended; searches still carry their ids
    }
  })()
  startSession().catch(() => {})
  return () => abort.abort()
}

// One file for both opencodes. opencode 1 calls `server`; opencode 2 calls
// `setup` and ignores `server`. A default export is what both load: a named
// export beside it would be loaded a second time by opencode 1.
export default { id: "jevlin", setup: jevlinV2, server: jevlinV1 }
