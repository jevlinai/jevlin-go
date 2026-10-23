# The `full` envelope as v0.4.3 printed it

Each file is the whole stdout of `jevlin search --stdin` with `{"version":1,"query":"q","view":"full"}`,
built at `a0ff6ca` (the v0.4.3 merge) against the fake router of `search_test.go`, which answered with
the router fixture of the same name (`../router/<name>.json`, or `../balanced_response.json`). They
were captured before the default view became `merged`, by the code of that commit, not typed.
`beta-balanced-filters-2026-10-23.envelope.json` was captured later, from the beta recording of the
same name, by a checkout of `a0ff6ca` and the same harness, twice, identical both times: what v0.4.3
prints for a router answer that carries `filters_applied` and `decision.build`, which it drops.

`TestTheFullViewOnlyAddsToWhatV043Sent` holds today's `full` envelope to them: with exactly the keys
added since removed (`fullViewAdditions`), every byte must be the same. Re-capturing one is a
deliberate change to what `"view":"full"` promises an integration, and says so in its commit.
