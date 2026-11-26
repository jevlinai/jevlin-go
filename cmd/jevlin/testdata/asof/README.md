# The router's dated read

`fetch-dated.json` is a dated read (`POST /v1/fetch` with `as_of`), as the router's own fetch
handler wrote it on its `feat/fetch-as-of` branch at `25263f2` (contract version 2026-11-26),
run in that repository with the setup of its own `TestAsOfFetchMapsEachOutcome`: three pages
from the tenant's recent searches, read with `"as_of":"2026-03-01T13:00:00+01:00"`. One page
comes back `ok` with its saved text, one `failed` with `reason: "blocked"` and one `failed`
with `reason: "not_saved_at_as_of"`, and the answer echoes the cutoff, normalized to
`2026-03-01T12:00:00Z`. The URLs are the test's own `example.com` hosts.

The file is not edited by hand: `routerDatedRead` in `asof_test.go` checks it against its
SHA-256. To take a newer answer, run the router's handler again and replace the file and the
sum together. The router's refusals (`as_of_*`) are a `{"error", "code"}` body like every
other refusal, and the tests serve them with the router's exact codes.
