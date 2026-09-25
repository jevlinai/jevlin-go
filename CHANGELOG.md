# Changelog

One heading per tag, newest first, each beginning `## vX.Y.Z — YYYY-MM-DD`. See
`docs/RELEASING.md` for how a release actually gets cut. `goreleaser`'s auto-generated
changelog (from `git log` between tags) already covers the mechanical commit-by-commit
record — what belongs here is the handful of things a participant or operator should be
told in plain language, that a list of commit subjects wouldn't make obvious on its own.

An entry describes the release it sits under, as that release behaved. A later release
superseding something does not make the older entry wrong, and older entries are not
rewritten to match newer behaviour; the newer entry says what changed.

## Unreleased

This repository starts from `twilight-project/dropin-miner` at
`9c547ab0c809244fbb7c6b2138ab61212583f2b3` — the v0.3.0 release plus its pre-import
hygiene PR — imported as one commit. Everything before that is in
[dropin-miner's changelog](https://github.com/twilight-project/dropin-miner/blob/main/CHANGELOG.md).
The first release replaces this heading with its own.

- **Installed through npm, or from a release archive.** The `install.sh`,
  `install.ps1` and `setup.sh` scripts are gone. Without Node, download the archive
  for your OS from the releases page, verify it against `checksums.txt`, put the
  binary on PATH and run `setup`.
- **Renamed to jevlin.** The binary and the npm package are `jevlin`
  (`npm install -g jevlin`), `jevlin version` prints `jevlin X.Y.Z`, the release
  archives are `jevlin_<version>_<os>_<arch>`, and `jevlin upgrade` fetches only from
  `jevlinai/jevlin-go`'s releases. The npm wrapper's own knobs are `JEVLIN_BINARY`
  and `JEVLIN_SKIP_DOWNLOAD`.
- **A new home, config and environment.** The installation lives in `~/.jevlin`
  with its config in `jevlin.toml`, the lifecycle lock is `~/.jevlin.lifecycle.lock`,
  and every variable the client reads, exports or documents is `JEVLIN_*`
  (`JEVLIN_HOME`, `JEVLIN_CONFIG`, `JEVLIN_API_KEY`, the trace channel's
  `JEVLIN_TRACE_BRIDGE`, `JEVLIN_LINEAGE`, `JEVLIN_SESSION` and `JEVLIN_HARNESS`, the
  wallet's and setup's overrides). The skill is `/jevlin`, in a `jevlin/` directory
  under each host, and opencode's and Pi's adapters are `jevlin.js` and
  `jevlin.ts`. The word *mining* stays wherever it was.
- **A security policy and a NOTICE.** `SECURITY.md` says how to report a vulnerability
  privately and what happens next. `NOTICE` carries the copyright line and credits the
  packages derived from `tokendrop-proxy`, and ships in every release archive and in the
  npm package.
