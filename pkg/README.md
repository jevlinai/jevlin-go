# pkg/ — the participant protocol

These packages are the Twilight mining participant as a Go library: installation identity
(DPoP) and enrollment, the durable spool and its delivery to the authorization server (AS), the
observation wire format, the wallet keys, and the draw derivation with its golden vectors.
`cmd/jevlin` is one client built on them; they are public so that other clients can import
them rather than copy them. Nothing under `pkg/` imports `cmd/` (AGENTS.md, invariant 8).

| package | what it does | who owns its contract |
|---|---|---|
| `pkg/auth` | AS discovery, OAuth with DPoP, enrollment, submission, the key and authorization store, the wallet keys | the AS wire contract, in `tokendrop-auth-server-design` |
| `pkg/config` | resolves the configuration once, from defaults, file, environment and flags | shared with `tokendrop-proxy`; the `[platform]` block is this repository's |
| `pkg/fsx` | the atomic, fsync'd file write everything durable goes through | this repository |
| `pkg/mining/collector` | delivers spooled observations to the AS, off the response path | the AS wire contract |
| `pkg/mining/draw` | the participation secret and the frozen draw derivation | the AS wire contract; the golden vectors travel with it |
| `pkg/mining/promote` | turns a finished observation into a `ProviderObservationV1` record | the AS wire contract |
| `pkg/mining/scope` | the participation context a mining pass snapshots | the AS wire contract |
| `pkg/mining/spool` | the durable observation queue and its quarantine | the AS wire contract |
| `pkg/observe` | watches provider responses without retaining them | the AS wire contract |
| `pkg/platform` | register, poll and enroll against the search platform's control plane | the search platform's agent-onboarding design, owned by `search-router` |
| `pkg/redact` | the one place a log handler is built; no credential reaches a log | this repository |
| `pkg/wire` | the AS data models, checksum-verified against `testdata/fixtures/wire` | the AS wire contract |

## PROVENANCE

Most of these packages were copied from `twilight-project/tokendrop-proxy`, with their import
paths changed. `PROVENANCE` names the proxy commit they were last synchronised with, so a
later resync knows where to diff from. It covers neither `pkg/fsx` nor `pkg/platform`, which
were written for this client and never lived in the proxy.

## Change protocol code in one place

A protocol change is made here, once, and every client that imports these packages picks it
up. Until the proxy imports them back, a change here must be mirrored there by hand. The golden
vectors under `testdata/vectors` and the checksummed wire corpus under `testdata/fixtures/wire`
are what catch a drift between the two: they are byte-identical in both repositories, so a
copy that has drifted fails them.
