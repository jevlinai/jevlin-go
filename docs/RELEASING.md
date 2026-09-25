# Releasing

A release has two halves. Choosing what to release is a human decision, recorded by an
annotated `vX.Y.Z` tag that a person pushes to `origin`. Everything after that tag is
`.github/workflows/release.yml`, which runs on a pushed `v*` tag and on nothing else. Nothing
in CI creates, moves or deletes a tag, and merging to `main` releases nothing.

## The invariant

The tagged commit carries `npm/package.json` at the tagged version and a `## vX.Y.Z` heading
in `CHANGELOG.md`, and the tag is `vX.Y.Z` exactly: always the `v`, three plain numbers, no
leading zeros, no pre-release or build suffix.

The npm package carries no binary. Its postinstall, `npm/install.js`, downloads the archive
for its platform from `releases/download/v<version>`, where the version is the package's own,
and checks it against that release's `checksums.txt`. So the wrapper's version *is* the
release tag it fetches, and a mismatch is a package that can never install. The failure is
silent on the side that gets watched — goreleaser never reads `npm/package.json`, so the
GitHub Release is perfect — and surfaces at a participant's `npm install`, in a version that
cannot be withdrawn. That is why it is checked before anything is built. Each check in
preflight exists because a release of the predecessor project shipped without it.

## The human half

1. **`main` is green.** `ci.yml` passing on `main` is a precondition; do not tag through a
   red `main`.
2. **A dedicated `release/x.y.z` PR** bumps `npm/package.json`'s `"version"` to `X.Y.Z` and
   turns `## Unreleased` in `CHANGELOG.md` into `## vX.Y.Z — YYYY-MM-DD`. Its merge commit is
   what gets tagged.
3. **The checks under "Before you tag" run on that exact commit.**
4. **Tag it and push the tag:**

   ```
   git tag -a vX.Y.Z -m "vX.Y.Z" <merge commit>
   git push origin vX.Y.Z
   ```

   Annotated, because it records who cut the release and when. `origin` is
   `jevlinai/jevlin-go`, where `npm/install.js` and the self-updater look for releases.
5. **Watch the run.** Read preflight's output even when it passes: it prints the tagged
   commit, canonical `main` and the asset names it expects. `publish-npm` waits for one of
   the `release` environment's reviewers to approve it.
6. **Accept the upgrade by hand.** On real native installations of the previous release,
   `jevlin upgrade` lands on the new one (`jevlin version` says so) and
   `jevlin upgrade -rollback` puts the previous one back. One of them is a Windows desktop
   with real-time antivirus protection on, because CI's Windows runners do not carry the
   antivirus and endpoint software a participant's machine runs. This is not possible for
   the first release, which has nothing to upgrade from, and its changelog entry says so.

## Before you tag

- **`CHANGELOG.md` has the heading for this version, with its date**, and an entry that says
  what changed for a participant. goreleaser's release notes are the commit list.
- **`npm/package.json` says `X.Y.Z`** in the commit being tagged.
- **The help text and the skill match the flags shipped.** A flag `usageText` or
  `cmd/jevlin/skill.md` names that the binary does not accept, or one it accepts that
  neither mentions, breaks the contract an agent reads.
- **Any adapter that has never had a live smoke is named in the changelog entry.** "Not
  live-smoked" is an acceptable answer; silence is not.
- **`make verify` is green on that exact commit.**

Preflight can run before anything is pushed. With the annotated tag created locally:

```
git fetch origin main
go run ./tools/releasecheck preflight -tag vX.Y.Z -repo . -canonical-ref refs/remotes/origin/main
npm pack ./npm --dry-run
```

A local tag that fails is removed with `git tag -d` and costs nothing; the same failure after
the push costs a version number.

For a release whose updater has never run under antivirus, the first among them, step 6 has a
substitute: on a Windows desktop with real-time protection **on**, at the commit to be tagged,
`go test ./internal/selfupdate ./cmd/jevlin -run 'Replace|Rollback|Upgrade|Uninstall' -count=1`
performs the replacement from inside the process being replaced, which CI runs only without a
participant's antivirus. Without such a machine, the changelog entry says so in one sentence.

## The automated half

`release.yml` is five jobs, each depending on the last, because the npm package downloads its
binary from the GitHub Release: the release must exist and be complete before the package
pointing at it is published, and a published npm version cannot be taken back. The checks
with a right answer live in `tools/releasecheck`, a Go program with its own tests, so every
rejection has a test that drives it. It is release tooling, not part of the client:
`.goreleaser.yaml` builds `./cmd/jevlin` and nothing else.

Every `uses:` in `release.yml` and `ci.yml` is pinned to a full commit SHA, with the action's
release in a trailing comment. A tag like `@v4` is a pointer its owner can move, and
"whatever `@v4` means today" is no answer for what runs beside a publishing credential;
`ci.yml` holds none and is pinned anyway, because a CI that changes without a diff is the
other thing pins prevent. A bump is a deliberate PR, and the comment names each SHA's release.

### `preflight` — may this tag become a release?

Nothing is built until all of these hold. Every file is read out of the tag with `git show`,
never from the worktree, because "`npm/package.json` at the tag" means the bytes the tag
carries; a later fix on `main` does not make a wrong tag right.

1. **The tag is `vX.Y.Z` exactly.** A pre-release tag would build and publish to npm's
   `latest`, and nothing downstream has been exercised against one.
2. **The tag is annotated.**
3. **The tagged commit is an ancestor of canonical `main`**, fetched fresh into its own ref
   and named on the command line, never inferred from the checkout, which on a tag push *is*
   the tag. This is the security-relevant check: a well-formed tag on some commit in the
   repository is not a release, and the ability to create a tag must not by itself publish a
   package built from a commit nobody reviewed.
4. **`npm/package.json` at the tag says `X.Y.Z`.**
5. **`CHANGELOG.md` at the tag has a `## vX.Y.Z` heading.**
6. **`.goreleaser.yaml` and `npm/install.js` name the same assets.** They are independent
   statements of what a release file is called. The expected names are derived from
   `.goreleaser.yaml` at the tag, not hard-coded, and a shape the tool does not model — a
   second build, `targets:`, `ignore:`, a template field it cannot resolve — stops the release.
7. **`npm pack ./npm --dry-run` succeeds.** Not `npm --prefix npm pack`: `--prefix` sets the
   install prefix, not the pack target, and that form fails on the root's missing
   `package.json`.

Last, it classifies the GitHub Release for the tag `absent`, `complete` or `partial`, which
is what makes a rerun safe.

### `release-binaries` — goreleaser

Six static binaries (linux, darwin and windows, each amd64 and arm64), each archived with
`LICENSE`, `NOTICE` and `README.md` — `tar.gz`, `zip` on Windows — and checksummed into
`checksums.txt`, published as a non-draft GitHub Release whose body is the commit log since
the last tag, opened by a link to `CHANGELOG.md` at this one. It is the only job with
`contents: write`, and goreleaser runs only when the release is `absent`.

### `verify-release` — read it back

The release is read back from the API and must carry **exactly** the six archives and
`checksums.txt`, no more and no fewer. An unexpected asset fails as a missing one does: it
means the configuration grew an artifact the check cannot name, and a verifier that shrugs at
files it does not understand is not verifying. goreleaser exiting zero is not proof it
published.

### `publish-npm` — the package

Publishes `./npm` at the version committed at the tag; nothing bumps, commits or pushes. It is
the only job that names the `release` environment and the only one with `id-token: write`, and
its checkout keeps no repository credential. `--ignore-scripts` keeps publication from running
any of the package's lifecycle scripts while the credential is live; the `postinstall` still
ships and runs for whoever installs it. `--provenance` attests the package to this run. When
the version is already on the registry, that is not taken as proof it is right: the published
tarball is fetched and compared file by file with `npm/` at the tag, and a difference stops
the workflow without republishing.

### `smoke-npm` — install it for real

On Ubuntu, macOS and Windows, without failing fast, so the result says which systems are
broken. A bounded wait comes first — the registry polled every ten seconds for at most ten
minutes — because a stale first answer after a publish is propagation, and one that never
arrives is a failure. Then, in a fresh temporary directory, `npm install` of the package at
`X.Y.Z`, whose postinstall fetches, verifies and unpacks the archive, and
`npx --no-install jevlin version`, which must print exactly:

```
jevlin X.Y.Z
```

No `v`: goreleaser stamps `-X main.version={{.Version}}`, the tag with its prefix stripped.
`--no-install` is load-bearing: without it `npx` would fetch the package itself and report a
version even if the install had produced nothing. This is the only check that runs the
released binary; everything before it compares strings.

## When something fails

Each state has one right answer. A rerun never moves the tag, never builds a different commit
and never invents a version.

**Preflight refused the tag.** Nothing was built or published, and the version number is
burned: leave the tag, fix `main`, and tag the next patch. The `release tag immutability`
ruleset refuses deleting or moving a `v*` tag for everyone and cannot tell a published tag
from an unpublished one; what it buys is that a published tag never moves under
`npm/install.js`, the self-updater or a published `checksums.txt`, which is worth more than a
number. The dead tag is inert: nothing resolves to it.

**The release is complete and a later job failed.** Rerun. Preflight classifies the release
`complete` and goreleaser is skipped, because rebuilding would replace assets a published
package may already be downloading.

**The release is partial.** Preflight stops and names what is missing. Stop too: whether it
is safe to replace what did upload depends on whether anything has fetched it. Repair the
release by hand, or delete the release (not the tag), and rerun.

**npm publish failed.** Rerun; the compare decides. Absent, it publishes; present and
identical, it moves on; present and different, it stops.

**The registry never served the version.** The publish reported success, so this is a
registry or package-state problem. Do not publish again; the version is immutable.

**A smoke install failed.** The publish cannot be undone, so the workflow stops and does
nothing else. A released package that does not install is a defect to diagnose, and the next
release is the fix.

## What installed updaters depend on

Every native installation carries its expectations of a release compiled in. A release that
breaks one cannot be upgraded into by any installed updater, and no later release fixes that
for copies already out there, so each is a contract with a test that fails in CI first.

- **The `version` line**: exactly `jevlin X.Y.Z` and a newline, nothing on stderr, under a
  stripped environment. `cmd/jevlin`'s `TestVersionOutputIsAReleaseCompatibilityContract`
  builds the binary as the release does, runs it through the updater's own validator, and
  checks that `.goreleaser.yaml` stamps the bare version.
- **The asset names**: `jevlin_X.Y.Z_<os>_<arch>.tar.gz` (`.zip` on Windows) and
  `checksums.txt`, computed by a small checked-in function rather than goreleaser's templates.
  `tools/releasecheck`'s `TestSelfUpdaterAssetNamesMatchGoReleaser` derives the matrix from
  `.goreleaser.yaml` and requires the two to agree.
- **A stable, published release from the canonical origin, under the frozen bounds**: GitHub's
  latest release, or exactly `vX.Y.Z` when asked, from the compiled-in `jevlinai/jevlin-go`;
  no drafts, pre-releases or non-canonical tags; redirects only to GitHub's own hosts; at most
  1 MiB of release metadata, 64 KiB of checksums, a 64 MiB archive, a 64 MiB executable and
  128 MiB of declared expansion. `internal/selfupdate`'s
  `TestReleaseSelectsLatestOrTheExactTagFromTheFixedOrigin`,
  `TestReleaseRejectsWhatIsNotAStableCanonicalRelease`,
  `TestRedirectPolicyIsHTTPSHostBoundedAndHopBounded` and `TestFrozenBounds` guard it. A
  release approaching half a bound is a question for review, not a reason to raise it.

## Repository settings this relies on

- **`main` ruleset**: a pull request is required, the eight CI checks must pass on a branch up
  to date with `main`, merge commits are the only method, deletion and force-push are
  refused, and nobody can bypass it.
- **`release tags` ruleset** on `refs/tags/v*`: only the maintain and admin roles may create
  a release tag.
- **`release tag immutability` ruleset** on `refs/tags/v*`: deletion, update and force-push
  are refused, and nobody can bypass it.
- **`release` environment**: deployments need a required reviewer's approval and are allowed
  only from `main` and `v*` tags.
- **No secrets**, in the repository or the environment; npm authenticates by OIDC.

Three controls, because each covers a case the others do not. A tag push runs the workflow as
it exists at the tagged commit, so a crafted commit could carry a `release.yml` without the
ancestry check. The `release tags` ruleset constrains **who** may create a release tag; the
ancestry check constrains **what commit** an honest tag may release; the `release`
environment, which gates the only job npm accepts a publish from, constrains **what any
workflow can reach** — the one control a workflow file cannot remove.

## npm trusted publishing

`publish-npm` declares `id-token: write`, so the npm CLI can ask GitHub for an OIDC token whose
claims name the run's repository, workflow file and environment. npm compares them with the
trusted-publisher record configured on the package on npmjs.com and, only on a match, mints a
short-lived token for that one publish. The record has four fields:

- **Owner:** `jevlinai`
- **Repository:** `jevlin-go`
- **Workflow file:** `release.yml`
- **Environment:** `release`

The record lives on npmjs.com, so nothing in this repository can widen it: a fork or a
differently named workflow cannot authenticate even with `id-token: write`, and renaming the
workflow file or the environment breaks publishing until the record is changed to match.
Trusted publishing needs npm 11.5.1 or later, so the job installs a pinned npm.

## Outside the automation, deliberately

- **The version bump.** CI does not commit to this repository; the number belongs in a
  reviewed commit.
- **Which commit gets released.** That is the human half.
- **Live host smokes and the upgrade acceptance**, which need real hosts and a real previous
  installation.
- **The workflow's own changes.** A tag-triggered workflow runs the version of itself at the
  tagged commit, so a change to `release.yml` first runs on the next real release. There is
  no way to rehearse a tag push without pushing a tag.
