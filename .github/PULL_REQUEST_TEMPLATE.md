<!-- Thanks for contributing to jevlin. Keep PRs focused and explain the WHY. -->

## Summary

<!-- What does this change do and why? Link any related issue: Closes #NNN -->

## Type of change

- [ ] Bug fix
- [ ] Feature
- [ ] Refactor / internal
- [ ] Docs
- [ ] CI / tooling

## Affected area

- [ ] `pkg/auth` — the key and registration store, the claim record, the redirect policy (**security-sensitive**)
- [ ] `pkg/platform` — search-platform agent onboarding (register / claim / `/me`)
- [ ] `pkg/redact` — trace redaction
- [ ] `pkg/fsx` — the durable writer (atomic rename, fsync, Windows write-through) and the confined operations
- [ ] `cmd/jevlin` — CLI commands (search, fetch, hook, connect, login, setup, uninstall, upgrade, agents, doctor, status, limits)
- [ ] machine protocol (`search --stdin` / `-json` envelope) — a change to the mandatory header or to what `ok`, `status`, `code`, `retryable`, `action` or `exit_code` mean is a protocol version change (`machineVersion`); a command-specific payload addition that preserves the header is not
- [ ] `pkg/config`
- [ ] lifecycle: `setup` / `uninstall` / `upgrade`, `internal/selfupdate`, the lifecycle locks — a destructive or replacing operation; see invariant 17
- [ ] docs / CHANGELOG

## Checklist

- [ ] `make verify` passes locally (build, vet, lint, lint-windows, tidy, race, vuln, cross-compile)
- [ ] Tests added/updated for the change
- [ ] Commit messages carry the reasoning (what was rejected and why), no `Co-Authored-By`/`Claude-Session` trailers (AGENTS.md convention)
- [ ] Docs updated where the behavior is described, each ticked or marked N/A:
      `README.md` · `npm/README.md` (byte-mirrors the top-level README — `TestNPMReadmeMirrorsRootREADME`) ·
      `docs/guide.md` · `docs/agents.md` · `docs/reference.md` · `cmd/jevlin/skill.md` · `usageText` (`main.go`) ·
      `AGENTS.md` · `CHANGELOG.md`
- [ ] State-directory change, if any (a new or changed file under a participant's state dir or installation directory): named below with its writer and its reader — N/A otherwise

State-directory notes:

<!-- File name, what writes it, what reads it. Delete this block if N/A. -->

## Safety & invariants

Required when the change touches the search path, the hooks, credentials or the platform contract — see AGENTS.md's hard invariants:

- [ ] Fail-open: a hook, trace or turn-end failure never fails or blocks a search
- [ ] No prompt/completion content leaves the machine, except the redacted trace and the opted-in turn end
- [ ] Every credential-bearing `http.Client` has an explicit redirect policy
- [ ] Secrets never in argv (stdin or owner-only 0600 files only)
- [ ] No unadvertised URL assembled or called; new hosts are configured, not discovered
- [ ] A claim/console URL is printed, never opened (invariant 11): `connect.go` imports no `os/exec`
- [ ] `platform.base_url` is validated and compared against, never dialed; only `agents_api_url` is (invariant 12)
- [ ] Tests never contact a real `*.jevlin.ai` or `*.nyks.dev` host — `httptest` stubs only
- [ ] `pkg/` still does not import `cmd/`

## Security considerations

- [ ] No secrets or API keys included
- [ ] This PR does not introduce a public security disclosure
- [ ] Security-sensitive behavior (redirect policy, credential handling, what a sandbox can write) is called out for reviewer attention below

Notes:

<!-- Mention any trust-boundary or credential-handling concerns. -->

## Review

Every non-obvious guarantee this PR adds: reverted, confirmed red at the
file:line below, restored (AGENTS.md's testing discipline). A row is
required for each; this section does not merge empty if the PR adds any
guard.

| Guard | Test that goes red | file:line |
|---|---|---|
| | | |

- [ ] Risky files / assumptions called out below

## Reviewer notes

<!-- Highlight files, assumptions, edge cases, or areas that need careful review. -->
