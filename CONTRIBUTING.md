# Contributing to jevlin

## Reporting a bug

Use the bug report form under **Issues → New issue**. It asks for what every issue here
carries: where it was seen (platform, `jevlin version`, the agent), what happened and how to
reproduce it, what you expected, the likely cause if you have one, why it matters, and a
severity.

A security problem is not a bug report. Follow [SECURITY.md](SECURITY.md) and report it
privately; never in a public issue or pull request.

## Proposing a change

For a change in behavior, open an issue first, so the change is agreed before anyone writes
it. For documentation and small fixes, a pull request on its own is fine.

## The flow

1. Branch from `main` as it stands, never from another unmerged branch.
2. Run `make verify` and get it green locally. It builds, tests (with and without the race
   detector), vets, lints for the host and for Windows, scans dependencies for known
   vulnerabilities, runs `go mod tidy`, and cross-compiles every release platform.
3. Open a pull request, as a draft while CI runs, with the template filled in.
4. All eight checks must be green on the exact head commit: `test` on Linux, macOS, Windows
   and Windows arm64, plus `race`, `cross`, `golangci-lint` and `govulncheck`.
5. The maintainer merges, with a merge commit.

## Licensing

Contributions are accepted under the Apache License 2.0 that covers this repository. By
submitting one you agree to license it under those terms. There is no separate agreement to
sign.

## The deeper rules

The invariants, the testing discipline and the conventions that every change is held to are in
[AGENTS.md](AGENTS.md). It is written for contributors and for the coding agents they work
with; read it before a change of any size.

## Code of conduct

A code of conduct will be adopted when the project has a contact address to enforce it through.
