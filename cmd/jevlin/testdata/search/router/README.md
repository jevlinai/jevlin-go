# The router's merged-view responses

What `search --stdin` maps into `result.merged` is the router's merged view, so its tests run
against the router's own output, not responses typed for them.

- `merged-view-success.main.json` is `docs/router-contracts/fixtures/merged-view-success.json`
  from Jevlin-core `main` at `d60a265`, byte for byte. The fixtures and the contract are
  unchanged there since `7335652`, the commit the live router ran on 2026-10-20. This is the
  shape a deployed router sends today: `found_by` entries of `{provider, rank}`, and no `votes`,
  `groups`, `coverage` or window fields.
- `merged-view-success.merge-v2.json` is the same file from `feat/merge-v2` at `26f505a`
  (Jevlin-core #55, not yet deployed), byte for byte.
- `merged-view-partial-failure.json` and `merged-view-no-usable.json` are the router's two
  failure fixtures from Jevlin-core `main` at `301a9b0`, byte for byte (unchanged there since
  `152d371`, and pinned by the router's own `SHA256SUMS`). The first is a search that went out
  degraded: `usage.deadline_hit` true and arms that timed out, hit the deadline, were still
  pending or were skipped, beside three that answered. In the second no arm answered, one with
  `status: "error"`, and the merged view is empty.
- `merged-view-two-forms.derived.json` is derived from the `feat/merge-v2` file, not
  captured. Its `_derived` field says how: one provider lists pages under a second query
  form, and the router's own code at that commit renumbered the citations and re-derived the
  merged view. No router fixture has two forms yet.

The router generates its fixtures with `SR_WRITE_CONTRACT_FIXTURES=1 go test ./internal/fanout
-run TestContractFixtures` and pins them with its own `SHA256SUMS`. `SHA256SUMS` here pins
these copies (`TestTheRouterFixturesAreTheOnesRecorded`), so a fixture changed by hand fails.
When the router's contract moves, copy its files again, update `SHA256SUMS`, and say which
commit they came from.
