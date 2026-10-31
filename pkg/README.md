# pkg/ — the client's shared packages

These packages are the parts of the jevlin client that another client could import rather than
copy: the configuration, the key and registration store, the search platform's onboarding
client, the trace scrub and the durable file writer. `cmd/jevlin` is one client built on them.
Nothing under `pkg/` imports `cmd/` (AGENTS.md, invariant 8).

| package | what it does | who owns its contract |
|---|---|---|
| `pkg/auth` | the key and registration store: the trace key and the agent registration in the state directory, the registration journal and the claim record beside `credentials.json`, and the same-origin redirect policy every credential-bearing client carries | this repository |
| `pkg/config` | resolves the configuration once, from defaults, file, environment and flags; an older version's tables load and are ignored | this repository; the file's shape came from `tokendrop-proxy` |
| `pkg/fsx` | the atomic, fsync'd file write, the replace-in-place of a participant's file, and the operations confined to a directory a sandbox may write | this repository |
| `pkg/platform` | register, poll, mint a claim code and read `/v1/agents/me` against the search platform's control plane | the search platform's agent-onboarding design, owned by `search-router` |
| `pkg/redact` | the trace scrub, and the one place a log handler is built, so no credential reaches a log or the router | this repository |

## PROVENANCE

`pkg/auth`'s store shape, `pkg/config` and `pkg/redact` were copied from
`twilight-project/tokendrop-proxy`, with their import paths changed, and have since been cut
down to what a search client needs. `PROVENANCE` names the proxy commit they were last
synchronised with, so a later diff against the proxy knows where to start. It covers neither
`pkg/fsx` nor `pkg/platform`, which were written for this client and never lived in the proxy.

## Change shared code in one place

A change to one of these packages is made here, once, and every client that imports them picks
it up. `pkg/platform` conforms to a contract owned elsewhere: a change to the platform's wire
shape is made in `search-router`'s design first, and here second.
