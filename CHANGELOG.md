# Changelog

What each release changed for someone who depends on this repository: the tools
it builds, how they are released, and what the build requires. A change earns an
entry when it alters what the repo requires, provides, or removes; rewording
does not.

## Unreleased

**`make upgrade` moves the `ranke-go` pin as well as the server's.** The module
every tool signs its claims with and the `ranke-db` release the dev server runs
both carry the graph contract, so one command takes both to their latest release
and `go mod tidy` follows.

**This repository is `github.com/rankegraph/ranke-tools`.** It moved to the
rankegraph organisation, and so `go install
github.com/rankegraph/ranke-tools/ranke-git@latest` is how `ranke-git` is
installed.

**`ranke-go` is `github.com/rankegraph/ranke-go`, at v0.30.0.** The module moved
there too, so every import moves with it. The dev server binary comes from
`rankegraph/ranke-db` for the same reason.

**The build needs Go 1.27.1**, which `ranke-go` v0.30.0 requires.

**The dev server founds its own archive on every launch.** The pin moves to
`ranke-db` v1.21.0, whose sequencer wants a bookmark list to advance and an
archive to serve: `server/config.json` carries the seed and founding branch, and
`server/run.sh` mints the throwaway founder key beside the signing key it already
minted.

**`ranke-git demo server` writes to the branch `ranke_git_demo_server`**, branch
names being `[a-z0-9_]`.

**`ranke-git snapshot` takes `--git-tag` and `--git-branch`**, `backup`'s own
flags, naming the one commit to archive. Both resolve under their `refs/`
namespace, so a tag and a branch sharing a name stay apart, where `--ref` keeps
git's resolution order. Exactly one of the three is required.

## v0.3.0 — 2026-09-02

**The release cycle is ranke-graph's shared script, cached rather than
vendored.** `scripts/release.sh` is gone. `make release` fetches
`release-cycle.sh` from ranke-graph into `bin/`, which is gitignored, and runs it
from there, so the git mechanics of a release are written once and this
repository cannot drift from them. `make upgrade` refreshes the cached copy.

**`make release` refuses a dirty tree or a missing bump word before it builds.**
Both checks are instant where the quality gate is not, so a release that was
going to fail on either no longer costs a build first.

**The build fetches ranke-graph from the rankegraph organisation.**
`RANKE_GRAPH_REPO` pointed at `flocko-motion/ranke-graph`.

**The repository states its licence.** `LICENSE` is Apache 2.0, matching
ranke-go, ranke-ts and ranke-db.
