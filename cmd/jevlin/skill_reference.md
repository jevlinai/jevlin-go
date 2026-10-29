# Jevlin reference

## Contents

- Settings: on, off, status and tier
- Tiers, presets and named indexes
- Failure actions in detail
- Fetch failure codes
- More fields in a search result

## Settings: on, off, status and tier

If the argument is exactly `on`, `off` or `status`, run

{{PREFER}}

show its output, and follow the new setting for the rest of the session. `off` makes the built-in web search the
default and keeps this search for when the user names it; `on` makes this search the default again.

If the argument is `tier`, alone or followed by a tier or preset the user named, run

{{TIER}}

with those words (`status` when none were given) and show its output.

Both settings are the user's. Never change either on your own.

## Tiers, presets and named indexes

A request without `tier` uses the user's tier, or the router's default when the user set none. The fast tier runs a
preset the user picks, for example `economy` (the cheapest) or `standard` (more coverage); `jevlin agents tier status`
lists them with their estimated price. `balanced` asks the core set of indexes; `max` asks for more results each and is
the user's choice only. Never pick a tier or preset yourself.

`providers` restricts a search to named indexes. It needs `"tier":"balanced"` or `"max"`, is billed at those indexes'
own prices and draws the user's credit. Send it only when the user asks for a specific index.

## Failure actions in detail

| action | meaning |
|---|---|
| `none` | nothing to do |
| `retry` | the same call may work later; wait `retry_after_ms` first when it is set |
| `fix_input` | the request was wrong; fix it before sending anything again |
| `connect` | the registration, setup or claim needs attention: run `jevlin connect`, or `jevlin status -json` to see which |
| `login` | a search credential exists and was not accepted: run `jevlin login` |
| `check_access` | the credential does not cover this request; the user has to sort it out |
| `report` | neither retrying nor changing the request will help; show the user what happened |

`connect` and `login` are different. A rejected credential, a router 401, is always `login`: a registered installation
whose key was rotated or expired gets exactly the same status, and sending the user to `connect` over it risks
registering a second agent for one person.

If `jevlin connect` prints a claim URL, show it to the user exactly as printed. Never open, shorten or paraphrase it.
Never put a key in a command line or a file yourself.

## Fetch failure codes

- `fetch_unavailable`: this account cannot fetch; `error.message` says why (not claimed, zero retention, or no
  credit).
- `fetch_not_supported` or `fetch_disabled`: this router cannot fetch.

In each case, use another way to read the page if the host has one. Any other failure is read from `ok`, `retryable`
and `action`, as a search's is. A fetch that failed leaves the search's answer standing.

A page entry with `truncated` true is longer (`full_length`) than the router returns; once `next_offset` is absent,
the text it can return is exhausted. Links in `content` come back as their text alone, without URLs.

## More fields in a search result

Per page:

- `best_rank`: the best position any index gave the page.
- `matched_terms`: which of the query's identifiers the page's text, title or URL contains, when present; they may not
  be in the trimmed snippet.
- `result_id`: the page's id within this search only.

Per search:

- `result.coverage`: distinct pages, how many only one group found, how many several groups found, and pages per
  wording. It is not a sum of votes.
- `result.decision`: the indexes selected for this search and any trimmed for budget. Selected is not the same as
  answered: read each index's `status`.
- `result.filters_applied`: the `recency` and `domain_filter` the router held every page to, with `dropped`, the number
  of pages it removed.
- `usage.deadline_hit`: some index ran out of time.
- `usage.cost_micros`: what the search cost, in millionths of a dollar. Mention it only if the user asks.
