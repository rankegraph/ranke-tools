# Changelog

What each release changed for someone who depends on this repository: the tools
it builds, how they are released, and what the build requires. A change earns an
entry when it alters what the repo requires, provides, or removes; rewording
does not.

## Unreleased

**`ranke-db` moves to v1.27.0**, client module and dev server binary both.
Finding the contributor a signing key holds is now `Client.ContributorsFor`,
a method that reads one scope, where `ranke-git` had queried the branch and
filtered the claims itself. A key no contributor on the branch carries is
refused naming both ways one comes to be admitted: `ranke-client branch
create`, and `ranke-client contributor add`, new in this release.

## v0.8.0 — 2026-09-10

**`ranke-git identity register` is gone.** `ranke-client`, the CLI `ranke-db`
ships, provisions contributors: `branch create` contributes the contributor
claim a branch is written under, `contributor list` reports what an archive
holds. `ranke-git` signs as a contributor and provisions none. (*Identity* was
no term of the graph's either; *contributor* is — "the actor, human, program,
or agent, whose work brings a claim into the graph", per the glossary.)

**`ranke-db` moves to v1.26.0**, client module and dev server binary both, and
`make upgrade` now keeps the two in step: the module follows whatever release
`server/.rankedb-version` pins, a client and its server having no business
drifting apart.

**`ranke-git` takes its instance wiring and key loading from `ranke-client`.**
`instance.Instance` builds the client from `--server` and one credential, and
`contributor.Load` resolves `--signing-key` through the same grammar — both
packages exist so `ranke-client`'s verb packages can share them, and a second
tool is the case they already serve. `--macaroon` comes with them, the third
credential the endpoint routes on.

**A contributor claim is built by `client.NewContributor`**, ranke-db's own
shape for one, replacing the three copies `contributor register`, `demo
server` and `demo local` each carried. `client.ContributorsFor` does the
pubkey match that finds an existing contributor, over the branch being written
rather than `$archive` — a branch holds its own contributor claim for a key as
of ranke-db v1.26.0, so the narrower right suffices.

## v0.7.0 — 2026-09-10

**`ranke-git` sends through `ranke-db/client`, the official Go client.** The
REST transport this repository wrote for itself is gone: `Query`/`QueryClaims`,
`GetClaim`, `Contribute` (which gathers external content from the Universe
itself) and `Dev().AdvanceClockPast` replace it, and a refusal now arrives as
a `*client.Error` matching `errors.Is(err, client.ErrNotFound)` and its
siblings. The build requires `github.com/rankegraph/ranke-db` as a result,
pinned like any other dependency; nothing of the server beyond its published
client is reachable from here.

**The dev server pin moves to `ranke-db` v1.24.0**, the release the client
ships in, so client and server move together on one `make upgrade`.

**ranke-go moves to v0.32.0.**

## v0.6.0 — 2026-09-10

**`--signing-key` reads the key from wherever the platform keeps it.** A bare
path still works, and `file:PATH`, `env:NAME`, `stdin` and `prompt` join it.
`env:` is the CI one: the runner is handed the PEM as environment and never
writes it to disk. The grammar is `keysource` in ranke-go, shared by every app
built on the library rather than spelled out again here.

**A signing key that others can read, or that was passed as material, is
refused.** A key file readable beyond its owner is rejected as `ssh` rejects
one — `identity register` has always written `0600` — and a PEM handed over
where a source belongs is rejected as compromised, having reached the process
table, the shell history and any CI log. Both rules come from `keysource`.

**ranke-go's `keysource` package and `Parse*` key readers** replace the PEM
loading `ranke-git` had written for itself.

## v0.5.0 — 2026-09-10

**`--signing-key` finds its own contributor.** `ranke-git` reads the branch's
contributors and signs as the one carrying that key's public key, so
`--contributor-id` is needed only where one key was registered twice. A key no
contributor carries is refused, naming `identity register`; a key two
contributors share is refused, naming both ids.

**`ranke-git attach` names its commit by ref.** `--ref`, `--git-tag` and
`--git-branch` resolve the target in `--clone`, so a release job attaches
against the tag it already holds; `--commit <sha>` still works, and one of the
two is required.

**A ref that names nothing fails before the first network call**, saying which
tag, branch or commit the clone lacks — and, for a tag or a branch, that a CI
checkout fetches neither by default. `snapshot`, `backup` and `attach`
resolved late and passed git's own "ambiguous argument … use '--' to separate
paths from revisions" through.

**`ranke-git attach` takes files and directories, and `--file` is gone.**
Paths are positional now: `attach --type artifact assets` attaches every file
under `assets/`, each its own claim named by its path within, contributed as
one batch; a run with no path still reads stdin. `--name` titles a single
attachment, and defaults to the file's own name. `--content-type` left out
takes the file extension's type, or what the bytes look like, in place of the
old `text/plain` default. Symlinks are skipped.

**`ranke-git attach --checksum` records a published digest on the
attachment.** It lands as the claim's `checksum` field in the form
`<alg>:<hex>` — `sha256` where the algorithm is left out, `md5`, `sha1`,
`sha256` or `sha512` where it is given — and the content is hashed first, so a
mismatch refuses the attachment rather than signing an unchecked claim.
`--checksum-file` reads the digest from a shasum-style file, and a sidecar
inside an attached directory (`site.tar.gz.sha256` beside `site.tar.gz`) is
folded into that artifact's field rather than attached on its own.

**`ranke-git` takes `--repo` and `--project` from the checkout it reads.**
`--repo` defaults to the clone's `origin`, and `--project` to the repo URL's
last segment, so a CI job that has already checked the repository out passes
neither. A URL carrying credentials, as some runners write into `origin`, is
recorded without them; an ssh remote keeps its user, which a restore needs to
reconfigure `origin`. Naming a monorepo's several projects is what `--project`
is now for.

## v0.4.0 — 2026-09-10

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
