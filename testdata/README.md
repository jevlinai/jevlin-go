# testdata

Fixtures shared by the packages under `pkg/` and `cmd/`. A package's own fixtures live in its
own `testdata/` directory; this one holds the ones that belong to a contract rather than to a
package.

| directory | what it holds | who owns it |
| --- | --- | --- |
| `vectors/` | the draw's golden vectors, read by `pkg/mining/draw` | the AS contract |
| `fixtures/wire/` | the AS wire corpus with its `SHA256SUMS`, checksum-mirrored and never edited here (`fixtures/README-wire-mirror.md`) | the AS contract |
| `fixtures/chain/` | CometBFT JSON-RPC responses captured from a public devnet node, read by `earnings` | this repository |
| `fixtures/json/` | synthetic search-router responses, read by `pkg/observe` | this repository |
| `fixtures/sse/` | synthetic OpenRouter streams, read by `pkg/observe` | this repository |

## No captured traffic

No prompt, completion, API key, provider response, or anything a person typed is ever
committed as a fixture. A fixture shaped like a provider's response is written by hand, and it
says so: its provider and model names say `synthetic`, its text is obviously fictional, and
its URLs are under `example.invalid`. Git history is permanent, so a real response committed
once is published for good, whatever a later commit removes.

The one exception is `fixtures/chain/`, and it is narrow. The rule protects what a person or a
provider says; a block chain's public RPC carries none of that. What it does carry is the
shape of a third-party wire protocol, and a hand-written fixture for one is not trusted: the
invented responses the `earnings` command was first tested against were wrong in ways only the
real node showed. `fixtures/chain/README.md` names each request and why that address was chosen.
