# Privacy

What jevlin sends, and to which host. What the service keeps of it, and for how long, is the
platform's to say: see its [privacy policy](https://platform.jevlin.ai/privacy).

## What leaves your machine

| what | goes to | when |
|---|---|---|
| The search request: your query and its options, and a small trace | the router your config names (`[router] url`, `api.jevlin.ai` by default) | every search |
| A page's full text request: the pages and options of a `fetch` | the same router | every fetch |
| The turn end: a record of a turn that searched | the same router | with `[search] turn_end = true`, which setup writes into a config it creates ([Switching it off](#switching-it-off)) |
| Registration and the claim's status | the platform's agents API (`[agents] url`) | `jevlin connect`, and setup, which runs it |
| A request for the latest release's version and, when a release is installed, its files, all with no credential | the project's GitHub releases | the daily release check (which installs a patch release by default), and `jevlin upgrade` |

Your search key goes in each request's `Authorization` header, and only to the router and agents API
recorded for it ([reference, Credentials](reference.md#credentials)). Nothing else is sent anywhere.

**The query on the command line.** Your agent hands jevlin the query on standard input, so it is
not among jevlin's arguments. It is in the shell command the agent runs, as a `<<<` here-string
(`jevlin search … --stdin <<< '{"version":1,"query":"…"}'`), which the agent's shell and its
transcript hold. In Claude Code a request holding a `'` or a `$` is written to a file in the
installation's `requests/` folder instead and read with `--stdin < <file>`; those files stay until
`jevlin uninstall -purge-state` removes the folder. A search you type,
`jevlin search "<query words>"`, puts the query among jevlin's arguments, where `ps` and your shell
history can see it.

## By agent

| agent | the trace's text | the turn end |
|---|---|---|
| Claude Code | An earlier message of the turn: Claude Code records a message after the hook runs, so the sentence beside the search is not in it. | What you asked, the assistant's text along the way, each tool's name, duration and outcome, the final answer, the model and its token counts (Claude Code 2.1.196 or later). |
| Cursor | Its latest reply (`afterAgentResponse`). The model's reasoning, which Cursor's `afterAgentThought` gives, is neither sent nor kept. | What you asked, the turn's searches, the final answer and the model. |
| Codex | None. | What you asked, the assistant's text along the way, each tool's name and outcome (a shell command has no duration), the final answer, the model and its token counts. |
| opencode | The assistant text before the search. | What you asked, the assistant's text along the way, each tool's name, duration and outcome, the final answer, the model and its token counts. |
| Pi | The assistant text before the search. | The same as opencode (Pi 0.80.4 or later). |
| Hermes | None: its hook payload carries no assistant text. | None. |
| any other agent | None: no hooks, a per-shell identity only. | None. |

## The trace

Each search carries a small `trace` so the router can group one task's searches:

| field | carries |
|---|---|
| `v` | `1`. |
| `harness` | Which agent: `claude-code`, `codex`, `cursor`, and so on. |
| `session_id`, `turn_id`, `call_id` | From a hook: the agent's ids, hashed with SHA-256 before they leave the machine; a subagent has a `session_id` of its own. With no hook (`harness` is then `cli`): the per-shell `session_id` below, a random `call_id`, and no `turn_id`. |
| `parent_session_id` | On a subagent's search only: the hashed `session_id` of the agent that started it. Claude Code, Codex and opencode. |
| `window` | Which context window of the session the search was made in, counting compactions. |
| `seq` | A counter of the searches recorded in the lineage file (one per workspace; in Cursor, per conversation). |
| `history` | The assistant text that led to the search ([By agent](#by-agent)), scrubbed, last 32 KiB. |

The text is scrubbed before it is cut, and an entry too large to scrub whole is omitted whole. Over
48 KiB, `history` is dropped; an envelope still too large is not sent. Where a hook writes it, the
envelope travels base64url-encoded in `JEVLIN_TRACE_BRIDGE`, written in the syntax of the shell
that runs the command: in PowerShell in front of any command, in a POSIX shell only when the
command's first simple command is the search, so a loop, a list or a pipeline with the search
anywhere else is left exactly as written. Cursor's hooks write a lineage file instead
(`JEVLIN_LINEAGE`).

A search with no hook carries a per-shell identity: the hostname and the parent shell's pid, keyed
with the random `trace.key` in the state directory, which is never sent; with no usable key and none
it can make, a one-off id. The hashed `session_id` is also sent as the request's top-level
`session_id` and its `X-Session-Id` header while an envelope rides. The trace is unauthenticated
metadata: nothing treats it as proof of origin.

The hooks keep lineage files in `~/.jevlin/sessions`, one per workspace (Cursor: per conversation).
Only the agent that wrote a file reads it, so a second agent in a subdirectory never sends the first
one's text. An agent started by another as a shell command carries the outer session and label: its
searches are work the outer session asked for.

## What the scrub removes

The trace's text and the turn end's text are scrubbed on this machine before they are sent.

| kind | removed |
|---|---|
| A credential's shape | API keys (Google's `AIza…` among them), Stripe secret keys, GitHub, GitLab, AWS, Slack, npm, Hugging Face and PyPI tokens, the secret path of a Slack webhook URL, JWTs, a password inside a URL; a PEM private key block whole, markers included (one cut short from its BEGIN marker through the lines that still look like a key's body); email addresses; a payment card number: 13 to 19 digits, in one run or grouped by single spaces or dashes as cards are printed, starting as a card network's do and passing the Luhn check, with no word, decimal or identifier joined to it. A long number of that shape, such as a decimal id, goes too. |
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
reference that carries a literal default (`${VAR:-…}`, `{{ … | default "…" }}`) is a value and goes.

It is a filter over text a model wrote, not a guarantee. A secret with no telling name and no known
shape passes, and so do, among others: `password: …` in the middle of a sentence; `NAME = value`
unquoted outside PowerShell; JSON escaped inside a quoted string; `env -0` output; a quoted value
broken across lines after a flag; a value Windows PowerShell 5.1 re-quoted as a native argument. It
also takes more than a secret in places: a line that begins `Password: …` loses the rest of the
line, a call at a line's start after a secret's key (`apiKey: getKey(),`) loses its value, and a
one-word line just after a PowerShell table reads as a name with no value, so the table does not end
there and a later line shaped like a row loses its value. Both scrubbers take time linear in their
input, held to a bound by test.

## The turn end

On with `[search] turn_end = true`: setup writes that line into a config it creates where there was
none, and says so. A config without the line, which is every config an earlier version wrote, reads
as off, so an upgrade leaves an existing installation as it was. When a turn in which a jevlin
search was served ends, the
agent's end-of-turn hook sends the router's `POST /v1/turns` one record:

| field | carries |
|---|---|
| `session_id`, `turn_id`, `parent_session_id` | The same hashed ids the turn's searches carried; the parent on a subagent's turn. |
| `harness` | Which agent. |
| `status` | `completed`, `interrupted` or `failed`. |
| `final_text`, `final_chars`, `truncated` | The assistant's last message of the turn, scrubbed, last 32 KiB, with its length before the cut and whether it was cut. Only on a completed turn, and absent when there is no message. |
| `user_text`, `user_chars`, `user_truncated` | What you asked in that turn, scrubbed, first 32 KiB. Absent for a turn Claude Code started rather than you (a continuation, a background task's result, the output of a `!` command, a compaction summary). |
| `steps` | The turn in order: what the assistant wrote along the way (scrubbed, capped), and each tool's **name, duration and whether it succeeded**. Never a tool's input or output. A search of ours is marked by its call id. Under Cursor, the searches only. |
| `model`, `usage` | The model, and the turn's input and output token counts. |

What each agent fills in is in [By agent](#by-agent). A search marks its turn only after the router
answers, so a refused or failed search does not count, and a turn is reported once. Under Claude
Code, a subagent searches under the turn that launched it, and a background one can do so after
that turn ended; that turn is then reported late, as completed, by the next turn end in the session.
Under Codex and opencode a subagent runs a turn of its own, joined to no turn end. The model's
private reasoning is never sent by any agent, in the trace or the turn end, and a subagent's own
steps are never sent in a turn end. The record goes only to the router this
run is configured with. The hook writes it to an owner-only file in `sessions/`, and a detached
`jevlin turn-end` sends it once and deletes the file, sent or not. opencode's plugin and Pi's
extension hand jevlin every turn that searched; jevlin drops it unless `turn_end` is on and the trace
is not switched off.

## Switching it off

Two switches stop the trace, and either one is enough: `trace = false` under `[search]` in your
config, and `export JEVLIN_TRACE=off`. Either sends no trace, no session id and no turn end: the
hooks write no bridge and no lineage file, and searches work the same without them. jevlin decides
this in one place for every agent. opencode's plugin and Pi's extension read only `JEVLIN_TRACE`,
so under `trace = false` they still assemble the trace and the turn in the agent, on this machine;
jevlin drops both, and nothing of either leaves the machine.

`turn_end = false` under `[search]` stops the turn end alone. setup writes `turn_end = true` only
into a config it creates where there was none; a config without the line reads as off.
`jevlin status` shows both, as `trace:` and `turn end:`, each on or off and why.
What jevlin keeps on this machine (the lineage files, the turn marks, the registration and the key)
is listed in [reference, Files and locks](reference.md#files-and-locks).
