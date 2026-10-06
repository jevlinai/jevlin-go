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
// FAIL-OPEN: any error leaves the tool call untouched and the search runs
// with its per-shell trace instead. Delete this file (or `jevlin agents
// uninstall`) to remove it; JEVLIN_TRACE=off disables tracing entirely.

// {{TRACE_COMMON}}

// The shell opencode runs its bash tool in on THIS machine, written in by
// the installer from the host's declaration. The bridge is an environment
// assignment, and its syntax is the shell's: a POSIX prefix handed to
// PowerShell is looked up as a program name and the search never runs (dropin-miner#68).
const HOST_SHELL = "{{HOST_SHELL}}"

// The installation this file belongs to: the config `jevlin agents
// install` resolved when it wrote this. Nothing here reads it — the plugin
// runs no command of ours and needs no config — it is how `uninstall` knows
// whose file this is. Every other artifact names its installation in a
// command it teaches; this one teaches none, so before this line existed an
// uninstall had nothing to match and removed another installation's plugin
// (dropin-miner#73). Two installations share this one path, so it names the one that ran
// `agents install` last, and the other leaves it alone.
const INSTALL_CONFIG = "{{INSTALL_CONFIG}}"

// The binary of the installation above, for the one thing this plugin hands
// it: a finished turn, on stdin (see reportTurn). Empty when the file was
// rendered with none, and then no turn is reported.
const JEVLIN_BIN = "{{JEVLIN_BIN}}"

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

export const JevlinLineage = async ({ client }) => {
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
            } finally {
              reporting.delete(key)
            }
          }
        }
        if (event?.type === "session.created" || event?.type === "session.updated") {
          const info = event?.properties?.info
          if (typeof info?.id === "string" && info.id) parents.set(info.id, typeof info.parentID === "string" ? info.parentID : "")
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
        const bridge = traceBridge(env)
        if (bridge === null) return
        // H-R4: a bridge already on the command is not ours until we put it
        // there. Every assignment we can prove standalone is removed and
        // this call's own is prepended; a command carrying one we cannot
        // remove with certainty is left exactly as it is.
        const rewritten = withTraceBridge(cmd, bridge, HOST_SHELL)
        if (rewritten === null) return
        output.args.command = rewritten
      } catch {
        // fail-open: the search runs untraced rather than not at all
      }
    },
  }
}
