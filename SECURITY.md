# Security policy

## Reporting a vulnerability

Report a vulnerability privately, through GitHub's private vulnerability reporting on this
repository: the **Security** tab, then **Report a vulnerability**. Never open a public issue
or pull request for one; a public report tells everyone before a fix exists.

Include what you can of:

- the version, from `jevlin version`;
- your operating system and architecture;
- the coding agent and its version, if one is involved;
- the steps that reproduce it;
- the impact: what an attacker gains, and from what position.

## What you can expect

An acknowledgment within five working days. A fix, and a GitHub security advisory published
before any public disclosure. Credit in the advisory if you want it; say so in the report.

## Scope

In scope:

- the `jevlin` binary;
- the npm wrapper (`npm/`);
- the skills, hooks and adapters `jevlin` writes into coding agents;
- the release pipeline: `.github/workflows/release.yml`, `.goreleaser.yaml` and
  `tools/releasecheck`.

Out of scope, and where to go instead: the search router, the platform and the authorization
server are separate services with their own owners; report to them. The coding agents
themselves belong to their vendors.

## Supported versions

The latest release only. A fix ships as a new release, never as a patch to an old one.

## Bounty

There is no bounty program.
