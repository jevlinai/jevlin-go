# The router's limits answer

`router-get.json` is what search-router's `GET /v1/limits` handler wrote, byte for byte: its
`LimitsHandler` behind its `Guard`, at `45a6624` (`internal/fanout/limits.go` unchanged since
`e8220f8`), run in that repository with a test of the same setup as its own
`TestLimitsReadCarriesTheWeekAndTheMonth`. The tenant is fictional (`t-fictional`), with a daily
cap of $5.00, no weekly cap, a monthly cap of $20.00, an effective ceiling of $5.00, and
$0.123456 spent. The answer nests the caps under `limits`, as the router's contract
(`docs/router-contracts/limits.md`) says; the client once read them at the top level, against
stubs typed in that shape (issue #192).

The file is not edited by hand: `routerLimitsAnswer` in `limits_test.go` checks it against its
SHA-256. To take a newer answer, run the router's handler again and replace the file and the sum
together.
