// jevlin lineage extension for Pi — installed by `jevlin agents install`.
//
// Auto-discovered from ~/.pi/agent/extensions/ (loader.ts discovers *.ts and
// *.js there; the module default-exports a function taking Pi's ExtensionAPI).
// Before each bash call that runs our search, it prefixes the command with
// JEVLIN_TRACE_BRIDGE=<envelope> carrying Pi's REAL session identity —
// hashed with the same domain-separated SHA-256 as the Go binary, so the raw
// id never leaves the machine — the turn the call was made in, the compaction
// generation of this session branch, and the assistant text that emitted THIS
// tool call. The same shape the Claude Code hook and the opencode plugin
// build; `jevlin search` reads the variable and sends it as the `trace` field
// of the search request.
//
// The scrub, the caps and the recognizer below the marker are spliced in
// from cmd/jevlin/agent_trace_common.js at install time — one
// repository source of truth shared with opencode. Everything the bridge
// carries is redacted and capped HERE, before the envelope is built and
// before the command is touched: the bridge is a process argument, and an
// argument is visible in `ps` the moment the command runs. Handing raw
// assistant text to the binary "to be scrubbed later" would be a disclosure
// that has already happened.
//
// OBSERVER, NEVER AN AUTHORITY. Pi lets a tool_call handler return
// {block: true} to refuse a call, and lets a THROWN error do the same
// (agent-session.ts rethrows as "Extension failed, blocking execution";
// emitToolCall has no try/catch of its own). We must do neither: this
// extension decides whether to attach a trace to a command Pi has already
// decided to run, and recognizing our own search command is not permission
// to run it. So the handler body is wrapped whole and returns nothing on
// every path, including every failure.
//
// FAIL-OPEN: any error leaves the tool call untouched and the search runs
// with its per-shell trace instead. Delete this file (or `jevlin agents
// uninstall`) to remove it; JEVLIN_TRACE=off disables tracing entirely.

// {{TRACE_COMMON}}

// The shell Pi runs its bash tool in on THIS machine, written in by the
// installer from the host's declaration rather than guessed from the
// command text (H-R1).
const HOST_SHELL = "{{HOST_SHELL}}"

// The installation this file belongs to — see the opencode plugin's copy of
// this line. Pi's skill names the installation in the command it teaches, so
// this extension is attributable through its sibling; it carries the line
// anyway, because the two halves are removed independently and a machine can
// have the extension with no skill (dropin-miner#73).
const INSTALL_CONFIG = "{{INSTALL_CONFIG}}"

// The binary of the installation above, for the two processes this extension
// starts: `hook start pi` when a session begins, which starts the flush and,
// once a day, the release check — what Claude Code's session-start hook does
// — and `hook turn pi` when a run settles, which is handed a finished turn
// (see reportTurn). Empty when the file was rendered with none, and then
// nothing is started. Nothing is read back from either.
const JEVLIN_BIN = "{{JEVLIN_BIN}}"

const startSession = async () => {
  if (!JEVLIN_BIN) return
  const { spawn } = await import("node:child_process")
  const args = ["hook"]
  if (INSTALL_CONFIG) args.push("-config", INSTALL_CONFIG)
  args.push("start", "pi")
  const child = spawn(JEVLIN_BIN, args, { stdio: "ignore", windowsHide: true })
  child.on("error", () => {})
  child.unref()
}

// The text parts of one Pi session entry, in the shape the shared
// preparation takes. An assistant message's content is always an array of
// parts (packages/ai/src/types.ts: AssistantMessage.content is
// (TextContent | ThinkingContent | ToolCall)[]), and only `text` parts are
// visible assistant prose — thinking and toolCall parts are not sent.
const assistantTextParts = (entry) => {
  const content = entry?.message?.content
  if (!Array.isArray(content)) return []
  return content
    .filter((c) => c?.type === "text" && typeof c.text === "string" && c.text !== "")
    .map((c) => ({ type: "text", text: c.text }))
}

// A real user turn: a message entry whose role is "user". Pi's user content
// may be a plain string OR an array of parts (UserMessage.content is
// `string | (TextContent | ImageContent)[]`), so this tests the role only.
const isUserTurn = (entry) => entry?.type === "message" && entry?.message?.role === "user"

// Does this entry's assistant message emit the tool call we were handed? A
// tool call inside an assistant message is a content part `{type: "toolCall",
// id, name, arguments}` — the part's field is `id`, and it carries the same
// value as the event's `toolCallId` (agent-session.ts passes toolCall.id as
// toolCallId). Pi drains the assistant message into the session BEFORE
// tool_call fires, so this entry is guaranteed to be on the branch already.
const emitsToolCall = (entry, toolCallId) => {
  const content = entry?.message?.content
  if (entry?.type !== "message" || entry?.message?.role !== "assistant" || !Array.isArray(content)) return false
  return content.some((c) => c?.type === "toolCall" && c.id === toolCallId)
}

// Where a tool call sits on the branch: the assistant message entry that
// emitted it (`owner`), and the user entry that opened the turn it was made in
// (`start`). Either is -1 when it cannot be proven, and then everything that
// hangs on it is left out rather than guessed.
//
// "The latest assistant text" or "the latest user message" would be the wrong
// question and wrong in two ordinary cases: a search made first in a new user
// turn would inherit the PREVIOUS turn's prose, and a tool call replayed from
// an older branch would pick up whatever is newest instead of its own. So the
// scan is anchored at both ends — capped at the assistant message that
// actually emits this toolCallId, floored at the user message that opened that
// turn — and any failure to establish either anchor sends nothing at all.
const locateToolCall = (entries, toolCallId) => {
  const where = { owner: -1, start: -1 }
  if (!Array.isArray(entries) || typeof toolCallId !== "string" || toolCallId === "") return where
  for (let i = entries.length - 1; i >= 0; i--) {
    if (emitsToolCall(entries[i], toolCallId)) {
      where.owner = i
      break
    }
  }
  if (where.owner < 0) return where // no provable owner for this call
  for (let i = where.owner - 1; i >= 0; i--) {
    if (isUserTurn(entries[i])) {
      where.start = i
      break
    }
  }
  return where // start < 0: no turn boundary in view, never cross one blind
}

// historyForToolCall returns the assistant prose that led to THIS tool call,
// already scrubbed and capped — or "" when the relationship cannot be proven.
const historyForToolCall = (entries, where) => {
  if (where.owner < 0 || where.start < 0) return ""
  for (let i = where.owner; i > where.start; i--) {
    const parts = assistantTextParts(entries[i])
    if (parts.length === 0) continue
    const text = prepareTraceHistory(parts)
    if (text === null) return "" // over the complete-source budget: omit whole
    if (text) return text
  }
  return ""
}

// The persisted id of the user entry that opened a turn: what Pi wrote to the
// session file, so a resumed session still names a turn by it, and a fork,
// which copies the entries with their ids, does too — unlike the turnIndex Pi
// counts in memory. Pi makes these ids from eight hex digits of a random
// UUID, unique within one session and no further, which is why the hash below
// takes the session's own id first. "" when the entry carries none.
const entryId = (entry) => (typeof entry?.id === "string" ? entry.id : "")

// The context window's generation, reconstructed from what Pi persisted
// rather than counted in this process. A compaction is a first-class,
// append-only session entry (`{type: "compaction", summary,
// firstKeptEntryId, …}`, session-manager.ts appendCompaction), so counting
// them on the CURRENT BRANCH is correct across every case a process-global
// counter gets wrong: a resumed session (the entries are read back from the
// session file), a restarted extension, several sessions at once (each has
// its own manager), and tree navigation or a fork (getBranch walks parent
// pointers from the current leaf, so a branch that does not contain a
// compaction does not count it).
const windowGeneration = (entries) => {
  if (!Array.isArray(entries)) return "none"
  const n = entries.filter((e) => e?.type === "compaction").length
  return n > 0 ? String(n) : "none"
}

// ── the turn end ────────────────────────────────────────────────────────
//
// With `[miner] turn_end = true`, a turn that searched is reported when it
// ends: what the user asked, what the assistant finally wrote, each tool by
// name, time and outcome, and the model. This file assembles that from the
// session branch, which already holds all of it, and hands it to the binary
// on stdin. The binary decides everything else — whether the installation
// opted in, whether a search was SERVED in this turn, what is capped — so
// this file only ever says what happened.
//
// A tool call is read for its name, and a bash command for one thing: whether
// it is our search. Of a tool call, only the name, the duration and the
// outcome Pi recorded are kept; its arguments and its result are never copied
// into what is sent, and the system prompt, which Pi persists as a message
// entry of its own, is never looked at.

// A number Pi recorded, or 0.
const count = (v) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0)

// Text that leaves this file goes through the shared scrub first, as the
// bridge's does, and is cut by the binary afterwards, never here: the end it
// keeps differs by field. A piece too large to scrub whole is left out whole,
// never sliced, because a slice can cut a secret in half.
const scrubbed = (text) => {
  if (typeof text !== "string" || text === "" || Buffer.byteLength(text) > TRACE_SOURCE_CAP) return ""
  return scrubTraceText(text)
}

// What a user entry says: Pi's user content is a string or an array of parts,
// and only its text parts are words. An image is not.
const userText = (entry) => {
  const content = entry?.message?.content
  if (typeof content === "string") return scrubbed(content)
  if (!Array.isArray(content)) return ""
  return scrubbed(
    content
      .filter((c) => c?.type === "text" && typeof c.text === "string" && c.text !== "")
      .map((c) => c.text)
      .join("\n"),
  )
}

// turnOf assembles the turn that began at the user entry `startId`, from
// Pi's own record of it: what the user asked, what the assistant wrote, the
// tools it called by name, duration and outcome, and where our searches
// fell. The turn runs to the next user message, or to the end of the branch.
// Returns null when the entry is not on the branch or nothing answered it.
const turnOf = (entries, startId, aborted) => {
  if (!Array.isArray(entries)) return null
  let start = -1
  for (let i = entries.length - 1; i >= 0; i--) {
    if (isUserTurn(entries[i]) && entries[i].id === startId) {
      start = i
      break
    }
  }
  if (start < 0) return null
  let end = entries.length
  for (let i = start + 1; i < entries.length; i++) {
    if (isUserTurn(entries[i])) {
      end = i
      break
    }
  }

  // Pi records, on each tool result, how long the tool ran and whether it
  // failed. Nothing else of a result is read.
  const outcomes = new Map()
  for (let i = start + 1; i < end; i++) {
    const m = entries[i]?.type === "message" ? entries[i].message : null
    if (m?.role !== "toolResult" || typeof m.toolCallId !== "string") continue
    outcomes.set(m.toolCallId, {
      ms: Math.round(count(m.durationMs)),
      ok: typeof m.isError === "boolean" ? !m.isError : undefined,
    })
  }

  const turn = { turn: startId, status: "completed", steps: [] }
  const asked = userText(entries[start])
  if (asked) turn.user_text = asked
  let input = 0
  let output = 0
  let last = null
  for (let i = start + 1; i < end; i++) {
    const m = entries[i]?.type === "message" ? entries[i].message : null
    if (m?.role !== "assistant") continue
    last = m
    if (typeof m.model === "string" && m.model !== "") turn.model = m.model
    const u = m.usage
    if (u) {
      input += count(u.input) + count(u.cacheRead) + count(u.cacheWrite)
      output += count(u.output)
    }
    for (const part of Array.isArray(m.content) ? m.content : []) {
      if (part?.type === "text") {
        const text = scrubbed(part.text)
        if (text) turn.steps.push({ kind: "assistant", text })
      } else if (part?.type === "toolCall" && typeof part.id === "string") {
        const { ms = 0, ok } = outcomes.get(part.id) ?? {}
        // The command is looked at for one thing: whether it is our search.
        if (part.name === "bash" && needsTraceBridge(part.arguments?.command)) {
          turn.steps.push({ kind: "search", call: part.id, ms, ok })
        } else {
          turn.steps.push({ kind: "tool", name: typeof part.name === "string" ? part.name : "tool", ms, ok })
        }
      }
    }
  }
  if (last === null) return null // nothing answered: there is no turn to end
  // The text after the last tool call is the answer; it is sent as such, once.
  let n = turn.steps.length
  while (n > 0 && turn.steps[n - 1].kind === "assistant") n--
  if (n < turn.steps.length) {
    turn.final_text = turn.steps
      .slice(n)
      .map((s) => s.text)
      .join("\n")
    turn.steps = turn.steps.slice(0, n)
  }
  // How it ended is the last answer's own stop reason; the run's abort flag
  // speaks for the last turn on the branch only, since an earlier turn
  // finished before whatever interrupted this one.
  if ((aborted && end === entries.length) || last.stopReason === "aborted") turn.status = "interrupted"
  else if (last.stopReason === "error") turn.status = "failed"
  if (turn.status !== "completed") delete turn.final_text
  if (input > 0 || output > 0) turn.usage = { input_tokens: input, output_tokens: output }
  return turn
}

// reportTurn pipes one finished turn to the binary's `hook turn pi`. On
// stdin: the text is never an argument and never a file of this extension's.
// Fire and forget, the way `hook start pi` is started.
const reportTurn = async (turn) => {
  if (!JEVLIN_BIN) return
  const { spawn } = await import("node:child_process")
  const args = ["hook"]
  if (INSTALL_CONFIG) args.push("-config", INSTALL_CONFIG)
  args.push("turn", "pi")
  const child = spawn(JEVLIN_BIN, args, { stdio: ["pipe", "ignore", "ignore"], windowsHide: true })
  child.on("error", () => {})
  child.stdin.on("error", () => {})
  child.stdin.end(JSON.stringify(turn))
  child.unref()
}

export default function (pi) {
  // sessionId -> the user entries whose turn a search of ours was threaded
  // into since the session's run last settled, oldest first. A turn that never
  // searched is not in it, so it costs no read of the branch and starts no
  // process. And `reported`: the turns already handed over, so a later run
  // that searches under the same user message (one an extension started with
  // a message of its own) does not hand it over again.
  const searched = new Map()
  const reported = new Set()

  pi.on("session_start", async () => {
    try {
      await startSession()
    } catch {
      // fail-open, and never a thrown error: Pi treats one as a reason to
      // block (see the header). A session that could not start the check
      // is checked at the next one.
    }
  })

  pi.on("tool_call", async (event, ctx) => {
    try {
      if (!event || event.toolName !== "bash") return
      const cmd = event.input && event.input.command
      if (!needsTraceBridge(cmd)) return

      const sm = ctx && ctx.sessionManager
      const sid = sm && typeof sm.getSessionId === "function" ? sm.getSessionId() : ""
      if (typeof sid !== "string" || sid === "") return

      // One read of the branch serves both the window generation and the
      // history: both are properties of THIS session branch as persisted.
      let entries = []
      try {
        entries = sm.getBranch() || []
      } catch {
        entries = [] // malformed or unavailable host state: ids still thread
      }

      const env = { v: 1, harness: "pi", session_id: traceHash(sid), window: windowGeneration(entries) }
      const callId = event.toolCallId
      if (typeof callId === "string" && callId !== "") env.call_id = traceHash(sid + "|" + callId)
      // The turn is the user entry this call was made under, by the id Pi
      // persisted for it. The turnIndex on turn_start/turn_end is no use: it is
      // an in-memory counter that restarts with each agent run and is never
      // written down, so two unrelated searches could share one. A call whose
      // turn cannot be proven carries none, as it carries no history.
      const where = locateToolCall(entries, callId)
      const turn = entryId(entries[where.start])
      if (turn !== "") env.turn_id = traceHash(sid + "|" + turn)

      const text = historyForToolCall(entries, where)
      if (text) env.history = [{ role: "assistant", text }]

      const bridge = traceBridge(env)
      if (bridge === null) return // oversized even without history: no bridge
      // H-R4: remove every bridge assignment we recognize, prepend our own,
      // and leave a command carrying one we cannot remove untouched.
      const rewritten = withTraceBridge(cmd, bridge, HOST_SHELL)
      if (rewritten === null) return
      event.input.command = rewritten
      // Only a search that goes out carrying its turn is a reason to report it.
      if (turn !== "") {
        const turns = searched.get(sid) ?? []
        if (!turns.includes(turn)) turns.push(turn)
        searched.set(sid, turns)
      }
    } catch {
      // fail-open: the search runs untraced rather than not at all. Nothing
      // is returned on any path — a truthy return is how a Pi extension
      // blocks a call, and this one is never a permission authority.
    }
  })

  // The run is over and Pi will not continue it by itself: report the turns
  // that searched. Not agent_end. One prompt can run several loops — Pi
  // retries a failed request, continues after compacting a full context, and
  // runs work queued meanwhile — and agent_end closes each of them, the first
  // often on an error with no answer; the turn would be reported failed, and
  // the binary reports a turn once. agent_settled fires once, after the last
  // of them, and says whether the run was aborted.
  pi.on("agent_settled", async (event, ctx) => {
    try {
      const sm = ctx && ctx.sessionManager
      const sid = sm && typeof sm.getSessionId === "function" ? sm.getSessionId() : ""
      if (typeof sid !== "string" || sid === "") return
      const turns = searched.get(sid)
      if (!turns) return
      let entries
      try {
        entries = sm.getBranch()
      } catch {
        return // unreadable now: left for the next run to settle
      }
      searched.delete(sid)
      for (const id of turns) {
        if (reported.has(sid + "|" + id)) continue
        const turn = turnOf(entries, id, !!event && event.aborted === true)
        if (!turn) continue
        reported.add(sid + "|" + id)
        await reportTurn({ session: sid, ...turn })
      }
    } catch {
      // fail-open: an observer that could not report has changed nothing. A
      // thrown error is how an extension fails a run; this one never does.
    }
  })
}
