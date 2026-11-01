# Jevlin reference

## Contents

- Settings: on, off, status and tier
- Tiers and named indexes
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

## Tiers and named indexes

The user picks the tier, and the fast tier's preset, with the tier command above; you never do. A request without
`tier` uses the user's setting. `providers` restricts a search to named indexes: send it only when the user asks for a
specific index, with `tier` set to `balanced` or `max`, billed at those indexes' own prices.

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

`result.merged` is ordered by agreement first, then by each page's best position. Per page:

- `found_by`: the indexes that listed it, and at which position. `votes` counts index groups and wordings that listed
  it, so one index finding a page for two wordings counts twice. Several groups agreeing is evidence; a page one index
  found can still be the find: check it rather than drop it.
- `best_rank`: the best position any index gave the page.
- `matched_terms`: which of the query's identifiers the page's text, title or URL contains, when present; they may not
  be in the trimmed snippet.
- `result_id`: the page's id within this search only.

Per search:

- `result.merged_total` above `result.merged_limit` means more pages exist; raise `merged_limit` if a list question
  needs them.
- `result.indexes`: each index's `status`. `ok`: answered. `pending`: still running when the list was sent; its pages
  are missing, not absent from the web. `skipped`: did not run. `quota` or `auth`: the index is limited or
  misconfigured; rewording will not help. `error`: failed this time; retrying may work.
- `result.coverage`: distinct pages, how many only one group found, how many several groups found, and pages per
  wording. It is not a sum of votes.
- `result.decision`: the indexes selected for this search and any trimmed for budget. Selected is not the same as
  answered: read each index's `status`.
- `result.filters_applied`: the `recency` and `domain_filter` the router held every page to, with `dropped`, the number
  of pages it removed.
- `usage.deadline_hit`: some index ran out of time.
- `usage.cost_micros`: what the search cost, in millionths of a dollar. Mention it only if the user asks.

If a result is too large to show and is saved to a file, open the file with the Read tool, not a shell command, and
send later requests with fewer wordings.
