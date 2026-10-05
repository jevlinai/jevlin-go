// jevlin: shared trace preparation for the JavaScript hosts.
//
// ONE source of truth. Every host whose lineage channel is a JavaScript or
// TypeScript artifact we install — opencode's in-process plugin, Pi's
// auto-discovered extension — is RENDERED from its own template with this
// file spliced in where that template carries the trace-common marker
// (renderAgentScript in agents.go). The installed files stay standalone,
// with no import of ours to resolve at runtime and no package manager in
// the picture, but the scrub and the caps below exist exactly once in the
// repository.
//
// That is the point. A second, weaker copy of this logic in a second
// adapter is the failure this file exists to prevent: the adapter is what
// builds the bridge, so an adapter that scrubs late — or not at all — puts
// raw assistant text into a process argument before the Go binary ever
// sees it. The order here is not negotiable:
//
//	complete bounded source -> scrub -> UTF-8 byte accounting ->
//	32 KiB history tail -> envelope -> 48 KiB envelope check -> base64url
//
// Never truncate before scrubbing: a slice can cut a secret in half and
// leave both halves unrecognizable to the redactor. An entry too large to
// scrub whole is omitted whole instead.
//
// Everything here fails open. A host adapter that throws leaves the tool
// call exactly as it found it and the search runs with its per-shell
// trace, which is the whole fallback contract: tracing must never be the
// reason a search does not run.
import { createHash } from "node:crypto"
import { hostname as traceHostname, userInfo as traceUserInfo } from "node:os"

// The trace hash: domain-separated SHA-256 over a PUBLIC prefix, truncated
// to 16 bytes and hex-encoded — the same derivation as traceHash in
// trace.go, so a host id hashed here and one hashed in Go agree. It is not
// keyed and is not a secret: it exists so a stable identifier can leave the
// machine without the host's own id leaving with it.
const TRACE_PREFIX = "tokendrop-trace-v1|"
// The visible-text cap: what may ride as history, after scrubbing.
const TRACE_HISTORY_CAP = 32 * 1024
// The complete-source budget: an entry larger than this is omitted whole
// rather than sliced, so redaction always sees a complete entry. Matches
// hookTailBytes in hook.go, which prepareTraceText applies on the Go side.
const TRACE_SOURCE_CAP = 256 * 1024
// The whole-envelope cap (compact JSON). Above it history is dropped
// first; an envelope still above it is dropped whole. A trace must never
// be the reason a search fails, and must never push a valid search
// invocation past the host's command-length limit.
const TRACE_ENVELOPE_CAP = 48 * 1024

// Our search command, by bare name or any path, optionally quoted,
// optionally .exe. Pinned by test to searchCommandRe in hook.go — the one
// recognizer every host goes through — so no adapter can quietly loosen
// into a substring match. This is lineage-injection detection: it decides
// whether we thread a trace onto a command the host has ALREADY decided to
// run. It is not an authorization decision and must never become one.
const SEARCH_RE = /(?:^|[\s;&|(]|\$\()\s*(?:&\s*)?(?:[A-Za-z]:)?["']?(?:[^\s"']*[\\/])?jevlin(?:\.exe)?["']?\s+search(?:\s|$)/

// SEARCH_LEADS_RE anchors SEARCH_RE's invocation: the search is the first
// simple command of the line, leading plain NAME=value assignments aside —
// the only place a POSIX assignment prefix reaches. One rule with
// posixSearchLeadsRe in bridge.go, pinned by TestBridgeGuardsAgree and
// TestTheLeadsGuardsAreOneRegex.
const SEARCH_LEADS_RE = /^\s*(?:[A-Za-z_][A-Za-z0-9_]*=[^\s"'\\]*\s+)*(?:[A-Za-z]:)?["']?(?:[^\s"']*[\\/])?jevlin(?:\.exe)?["']?\s+search(?:\s|$)/

const TRACE_BRIDGE_ENV = "JEVLIN_TRACE_BRIDGE"

const traceHash = (raw) => createHash("sha256").update(TRACE_PREFIX + raw).digest("hex").slice(0, 32)

// Mirror pkg/redact.TraceText for complete source entries before the bridge
// is capped. The Go consumer applies its own preparation again. Every step
// below is one function in pkg/redact/redact.go, in the same order, and
// TestSharedTraceSourceRedactionBoundaries holds the two to the same bytes.
//
// The character classes are written out ([\t\n\f\r ] rather than \s, [^\n]
// rather than .) because JavaScript's shorthands are wider than Go's, and a
// rule that fires in one language and not the other is a parity bug.
const TRACE_REDACTED = '[REDACTED]'

// scrubCommon: credentials with a recognizable shape.
// Start URL/email scans at token boundaries to avoid rescanning long words.
const scrubTraceCommon = (text) => text
  .replace(/(?<![a-zA-Z0-9+.-])([a-zA-Z0-9+.-]*:\/\/)[^/@\s]+@/g, (match, prefix) =>
    /[a-zA-Z]/.test(prefix) ? prefix + TRACE_REDACTED : match)
  .replace(/\b(?:sk|sr)-[A-Za-z0-9_-]{16,}/g, TRACE_REDACTED)
  .replace(/\bgh[opsur]_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b/g, TRACE_REDACTED)
  .replace(/\bAKIA[0-9A-Z]{16}\b/g, TRACE_REDACTED)
  .replace(/\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b/g, TRACE_REDACTED)

// redactEnvDumps: every value in a run of five or more consecutive
// NAME=value lines. Five in a row is the output of env or printenv quoted
// back, and nothing in it has a shape a pattern could pick secrets out by.
const TRACE_ENV_DUMP_RUN = 5
const TRACE_ENV_LINE = /^([\t\n\f\r ]*(?:export[\t\n\f\r ]+)?[A-Za-z_][A-Za-z0-9_]*=)([^\n]*)$/
const redactTraceEnvDumps = (text) => {
  if (text.split('=').length - 1 < TRACE_ENV_DUMP_RUN) return text
  const lines = text.split('\n')
  const bare = (i) => lines[i].endsWith('\r') ? lines[i].slice(0, -1) : lines[i]
  const isEnv = (i) => TRACE_ENV_LINE.test(bare(i))
  let changed = false
  for (let i = 0; i < lines.length;) {
    if (!isEnv(i)) { i++; continue }
    let j = i
    while (j < lines.length && isEnv(j)) j++
    if (j - i >= TRACE_ENV_DUMP_RUN) {
      for (let k = i; k < j; k++) {
        const line = bare(k)
        const cr = line === lines[k] ? '' : '\r'
        const m = TRACE_ENV_LINE.exec(line)
        if (m[2] !== '' && m[2] !== TRACE_REDACTED) { lines[k] = m[1] + TRACE_REDACTED + cr; changed = true }
      }
    }
    i = j
  }
  return changed ? lines.join('\n') : text
}

// redactSecretAssignments: the value of NAME=value when a whole
// `_`-separated segment of NAME says it is a secret, or NAME is our own
// trace bridge, whose value is an envelope no pattern can see into. KEY and
// PASS count only in a name with no lowercase letter: `key=value` is prose.
const TRACE_SECRET_SEGMENTS = new Set(['PASSWORD', 'PASSWD', 'SECRET', 'SECRETS', 'TOKEN', 'CREDENTIAL', 'CREDENTIALS', 'APIKEY'])
const TRACE_SECRET_SEGMENTS_UPPER_ONLY = new Set(['KEY', 'PASS'])
const traceSecretName = (name) => {
  const upper = name.toUpperCase()
  const upperOnly = name === upper
  return upper.split('_').some((seg) => TRACE_SECRET_SEGMENTS.has(seg) || (upperOnly && TRACE_SECRET_SEGMENTS_UPPER_ONLY.has(seg)))
}
const redactTraceSecretAssignments = (text) => text
  .replace(/(^|[^A-Za-z0-9_])([A-Za-z_][A-Za-z0-9_]*)=("[^"\n]*"|'[^'\n]*'|[^\t\n\f\r "']+)/g, (match, lead, name, value) =>
    value === TRACE_REDACTED || !(name === TRACE_BRIDGE_ENV || traceSecretName(name)) ? match : lead + name + '=' + TRACE_REDACTED)

const redactTraceEmails = (text) => text
  .replace(/(?<![A-Za-z0-9._%+-])([.%+-]*)([A-Za-z0-9_][A-Za-z0-9._%+-]*@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b)/g, (match, leading, email, offset, source) =>
    source[offset + match.length] === ':' || /(?:ssh|scp|rsync|sftp)$/i.test(source.slice(0, offset + leading.length).replace(/[ \t]+$/, '')) ? match : leading + TRACE_REDACTED)

const redactTraceHomePaths = (text) => text
  .replace(/([A-Z]:\\Users\\)[^\\\s]+/gi, '$1' + TRACE_REDACTED)
  .replace(/(\/Users\/|\/home\/)[^/\s]+/g, (match, prefix, offset, source) =>
    offset > 0 && /[A-Za-z0-9.]/.test(source[offset - 1]) ? match : prefix + TRACE_REDACTED)

// The local identity: the hostname's first label and the account name, each
// only when specific enough to identify this machine or person. "user" and
// "admin" are ordinary words; replacing them would destroy the text to hide
// nothing. ASCII names only, so both languages fold case the same way.
const TRACE_GENERIC_IDENTITY = new Set(['root', 'user', 'users', 'admin', 'administrator',
  'ubuntu', 'debian', 'runner', 'guest', 'test', 'dev', 'home', 'node', 'app', 'www', 'git', 'deploy',
  'build', 'docker', 'vagrant', 'localhost', 'local', 'server', 'host', 'macbook', 'mac', 'desktop',
  'laptop', 'workstation', 'default', 'system', 'nobody', 'daemon', 'code', 'agent', 'main', 'master'])
const traceIdentityPatterns = (host, account) => {
  host = String(host || '')
  account = String(account || '')
  if (host.includes('.')) host = host.slice(0, host.indexOf('.'))
  if (account.includes('\\')) account = account.slice(account.lastIndexOf('\\') + 1)
  const out = []
  const seen = new Set()
  for (const name of [host, account]) {
    const lower = name.toLowerCase()
    if (!/^[A-Za-z0-9._-]{3,}$/.test(name) || TRACE_GENERIC_IDENTITY.has(lower) || seen.has(lower)) continue
    seen.add(lower)
    out.push(new RegExp('(?<![A-Za-z0-9_])' + name.replace(/\./g, '\\.') + '(?![A-Za-z0-9_])', 'giu'))
  }
  return out
}
const TRACE_LOCAL_IDENTITY = (() => {
  let host = ''
  let account = ''
  try { host = traceHostname() } catch {}
  try { account = traceUserInfo().username } catch {}
  return traceIdentityPatterns(host, account)
})()
// redactLocalIdentity: the hostname and the account name as whole words.
// redactTraceHomePaths takes the name out of /home/<name>; this is the same
// name everywhere else: USER=name, ssh name@host, a prompt, plain prose.
const redactTraceLocalIdentity = (text, patterns) =>
  patterns.reduce((out, re) => out.replace(re, TRACE_REDACTED), text)

const scrubTraceText = (text, identity = TRACE_LOCAL_IDENTITY) =>
  redactTraceLocalIdentity(redactTraceHomePaths(redactTraceEmails(redactTraceSecretAssignments(redactTraceEnvDumps(scrubTraceCommon(text))))), identity)

// prepareTraceHistory takes the COMPLETE text parts of one assistant
// message — `{type: 'text', text}` entries, the shape opencode's message
// parts already have and the shape every other adapter converts to — and
// returns the scrubbed, capped tail that may ride as history.
//
// Returns null when the complete source is over budget: the entry is
// omitted whole, never sliced to fit, because slicing would hand the
// scrubber a severed secret. Returns "" when there is nothing to send.
const prepareTraceHistory = (parts, identity = TRACE_LOCAL_IDENTITY) => {
  const texts = []
  let size = 0
  for (const part of parts) {
    if (part?.type !== 'text' || typeof part.text !== 'string') continue
    size += Buffer.byteLength(part.text) + (texts.length ? 1 : 0)
    if (size > TRACE_SOURCE_CAP) return null
    texts.push(part.text)
  }
  const bytes = Buffer.from(scrubTraceText(texts.join('\n'), identity))
  // The tail, not the head: the words nearest the search are the ones that
  // explain it. Walk forward off any continuation byte so the cut lands on
  // a rune boundary and the result is still valid UTF-8.
  let start = Math.max(0, bytes.length - TRACE_HISTORY_CAP)
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return bytes.subarray(start).toString('utf8')
}

// traceBridge serializes the envelope and enforces the envelope cap:
// history is dropped first, and an envelope still too large yields null —
// no bridge at all, rather than a command the host cannot run. Returns the
// base64url bridge value.
const traceBridge = (env) => {
  if (Buffer.byteLength(JSON.stringify(env)) > TRACE_ENVELOPE_CAP) delete env.history
  if (Buffer.byteLength(JSON.stringify(env)) > TRACE_ENVELOPE_CAP) return null
  return Buffer.from(JSON.stringify(env)).toString("base64url")
}

// ── the bridge this adapter puts on the command ─────────────────────────
//
// H-R4: only a bridge THIS adapter generated for THIS call may carry this
// adapter's harness. So an adapter never stands down because a bridge is
// already there — a model can write one itself, and dropin-miner#68 is what that looks
// like at the router: a trace the model assembled, attributed to us. It
// removes every assignment it RECOGNIZES and prepends its own.
//
// "Recognized" is a syntactically standalone assignment statement in a
// declared shell's syntax, and nothing else: a leading NAME=value word in
// POSIX, a standalone `$env:NAME = …` statement in PowerShell, `set NAME=…`
// as its own command in cmd. Never a substring inside quoted text, inside
// another argument, or inside the JSON request body — a query that mentions
// the variable is still just a query. An assignment somewhere this cannot
// prove is standalone leaves the command untouched and no lineage claimed.
//
// The binary cannot authenticate an environment variable, so the client
// trace stays unauthenticated metadata either way; what this protects is
// the meaning of the harness field, not the trust in it.

// A standalone bridge assignment at the START of the command, in each
// declared shell's syntax, with the rest of the command after it.
const BRIDGE_ASSIGNMENT_RE = new RegExp(
  "^(?:" +
    // POSIX: NAME=value as a leading word.
    TRACE_BRIDGE_ENV + "=[^\\s]*\\s+" +
    "|" +
    // PowerShell: $env:NAME = '…' or "…" or a bare word, as its own
    // statement, ended by a newline or a semicolon.
    "\\$env:" + TRACE_BRIDGE_ENV + "\\s*=\\s*(?:'[^']*'|\"[^\"]*\"|[^\\s;]*)\\s*[;\\n]\\s*" +
    "|" +
    // cmd: set NAME=value as its own command.
    "set\\s+" + TRACE_BRIDGE_ENV + "=[^&\\n]*(?:&+|\\n)\\s*" +
    ")",
  "i",
)

// The try/finally wrapper this adapter writes around a PowerShell command,
// so one it has already rewritten is unwrapped rather than wrapped twice.
const PS_WRAPPER_RE = new RegExp(
  "^try \\{ ([\\s\\S]*)\\n\\} finally \\{ Remove-Item Env:" + TRACE_BRIDGE_ENV + "[^}]*\\}$",
)

// stripBridgeAssignments removes every bridge assignment it can prove is a
// standalone statement at the front of the command, and unwraps this
// adapter's own PowerShell wrapper.
const stripBridgeAssignments = (cmd) => {
  let rest = cmd
  for (;;) {
    const match = BRIDGE_ASSIGNMENT_RE.exec(rest)
    if (match) {
      rest = rest.slice(match[0].length)
      continue
    }
    const wrapped = PS_WRAPPER_RE.exec(rest)
    if (wrapped) {
      rest = wrapped[1]
      continue
    }
    return rest
  }
}

// carriesUnremovableBridge: the command still mentions the variable in a
// position this cannot prove is a standalone assignment. The adapter leaves
// such a command exactly as it found it.
const carriesUnremovableBridge = (cmd) => cmd.includes(TRACE_BRIDGE_ENV + "=") || cmd.includes(TRACE_BRIDGE_ENV + " =")

// needsTraceBridge: our search command. A bridge already on it is not a
// reason to stand down (H-R4); it is a reason to remove it first.
// Recognition only — the host has already decided to run this command.
const needsTraceBridge = (cmd) => typeof cmd === "string" && SEARCH_RE.test(cmd)

// withTraceBridge returns the command carrying THIS adapter's bridge, in the
// syntax of the shell that will run it, or null when the command must be
// left alone.
const withTraceBridge = (cmd, bridge, shell) => {
  const stripped = stripBridgeAssignments(cmd)
  if (carriesUnremovableBridge(stripped)) return null
  if (shell === "powershell") {
    // Assignment, then removal in a finally. A `& { … }` block looks like it
    // would scope this and does not: $env: is the process environment, and
    // both editions were measured leaving the variable set afterwards, which
    // would hand the host's NEXT command this call's envelope.
    return (
      "$env:" + TRACE_BRIDGE_ENV + " = '" + bridge + "'; try { " + stripped +
      "\n} finally { Remove-Item Env:" + TRACE_BRIDGE_ENV + " -ErrorAction SilentlyContinue }"
    )
  }
  // The POSIX prefix binds to the first simple command and is a syntax
  // error before a compound one, so it is written only when that first
  // command is provably the search; anything else is left exactly as the
  // host wrote it, and that search runs on its local fallback identity.
  if (!SEARCH_LEADS_RE.test(stripped)) return null
  return TRACE_BRIDGE_ENV + "=" + bridge + " " + stripped
}
