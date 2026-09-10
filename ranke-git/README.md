# ranke-git

Archives git state into a running `ranke-db`, byte-exact and content-deduplicated —
so a force-push or a squash can never erase what it already captured. A pure REST
client: it never starts a server itself, never touches `ranke-db`'s own module, only
`github.com/rankegraph/ranke-go` to build and sign claims.

For the design decisions behind these shapes — why `path` sits where it does, why
`entity/cve` is unprefixed, the whole claim/edge layout — see [DESIGN.md](./DESIGN.md).

## Install

```sh
go install github.com/rankegraph/ranke-tools/ranke-git@latest
```

Or build from a checkout: `make -C .. build` (repo root), producing `bin/ranke-git`.

## Quickstart

Every command needs a server, an already-registered contributor, and (for
`snapshot`/`backup`) a checkout to read. `--repo` defaults to that checkout's
`origin`, and the project to the repo URL's last segment, so a CI job that
checked the repo out already names both: `--project` is for a monorepo, where
one repo holds several. Mint a contributor once:

```sh
ranke-git identity register --server https://ranke-db.example.com --out contributor.pem
```

Then archive a commit:

```sh
ranke-git snapshot \
  --server https://ranke-db.example.com \
  --signing-key contributor.pem \
  --clone /path/to/local/checkout --ref HEAD
# contributor: the one carrying this key · repo: the clone's origin · project: widgets
```

The key names the identity: `ranke-git` reads the branch's contributors and
signs as the one whose public key matches. `--contributor-id` picks between
them where one key was registered twice, two identities carrying different
provenance.

### Where the signing key comes from

`--signing-key` takes a path, or one of the spellings `ranke-go`'s
[`keysource`](https://github.com/rankegraph/ranke-go) grammar defines, which
every app on the library shares:

| Source | For |
| --- | --- |
| `contributor.pem`, `file:contributor.pem` | a mounted secret — Kubernetes, `/run/secrets`, systemd's `LoadCredential` |
| `env:RANKE_SIGNING_KEY` | CI, which hands secrets over as environment; the key never reaches the runner's disk |
| `stdin` | a pipeline holding the key already |
| `prompt` | a person, pasting it and ending with a blank line |

Two refusals come with the grammar. A key file anybody but its owner can read
is rejected, the way `ssh` rejects one, and `identity register` writes `0600`.
Key material passed where a source belongs — the PEM itself, rather than a
path or `env:NAME` — is rejected as compromised, because by then it has
reached the process table, the shell history and any CI log; rotate that key.
A pasted `prompt` echoes: a paste nobody can see is a paste nobody can check.

```yaml
env:
  RANKE_SIGNING_KEY: ${{ secrets.RANKE_SIGNING_KEY }}
run: ranke-git snapshot --server "$RANKE_SERVER" --signing-key env:RANKE_SIGNING_KEY \
       --clone . --git-tag "$TAG"
```

A URL carrying credentials — `https://gitlab-ci-token:<token>@…`, as some
runners check out with — is recorded without them, since the repo URL becomes
the repository entity's own name. An ssh remote keeps its `git@`, which a
restore needs to reconfigure `origin`.

A [`--config`](./config.example.yaml) YAML file is a standing alternative to typing
every flag by hand — a flag given on the command line still wins over what's in it.

## Commands

Global flags (`--server`, `--token`/`--api-key`, `--contributor-id`, `--signing-key`,
`--repo`, `--clone`, `--project`, `--branch`, `--path`, `--config`) are shared by every
command below; see `ranke-git --help` for their full descriptions.

### `snapshot` — one commit, byte-exact, no history

The exact source an artifact was built from: one commit's tree, optionally narrowed
to a list of `--path`s (a monorepo subset). Restorable as that one commit, not a
working clone — no `.git`, no history, no refs.

```sh
ranke-git snapshot --ref <tag-or-commit> ...
ranke-git snapshot --git-tag v1.0.0 ...        # backup's spelling, for one commit
ranke-git snapshot --git-branch main ...       # that branch's tip
```

Name the commit once, in whichever of the three spellings suits: `--ref` resolves
the name git's own way, `--git-tag` and `--git-branch` resolve under `refs/tags/`
and `refs/heads/`, which tells a tag from a branch that carries the same name.

### `backup` — a reconstructible clone

Every commit reachable from the given branches/tags, chained to its parents,
restorable as a real, walkable git repository with the same branches and tags back.

```sh
ranke-git backup --git-branch main --git-branch feature --git-tag v1.0.0 ...
```

### `attach` — cite arbitrary content onto an archived commit

A build log, a test report, a platform-specific artifact — anything a release
process produces after `snapshot`/`backup` already ran. The driving case: CI
archives the repo, then attaches its own logs and outputs against the same commit.

```sh
gh run view "$RUN_ID" --log | ranke-git attach --git-tag v1.0.0 \
  --type build_log --name "release build log"

gh release download v1.0.0 --dir assets
ranke-git attach --git-tag v1.0.0 --type artifact assets
```

Name the commit with `--commit <sha>`, or with `--ref`, `--git-tag` or
`--git-branch`, which resolve it in `--clone` the way `snapshot` does — the
release tag a CI job already has in hand, rather than a `git rev-parse` of
its own.

`--type` becomes `source/git_<type>` — never a bare string, never parsed by
`ranke-git` itself. With no path given, `attach` reads stdin, so a CI step
pipes its own log through without writing it down.

Each path is a file or a directory, a directory standing for every file
beneath it, named by its path within (`linux/tool`). Every file becomes its
own claim, and the batch is contributed in one go. Symlinks are skipped, and
the run prints each attachment as it goes:

```
>> linux/tool — application/octet-stream, 4194304 byte(s)
>> site.tar.gz — application/gzip, 1049182 byte(s), sha256:db54a0dc…
```

`--content-type` is the media type; left out, the file extension decides, and
failing that the bytes themselves. `--name` titles a single attachment, which
a whole directory has no room for — there, each file keeps its own name.

`--checksum` records the digest a release publishes alongside its artifact, as
the claim's `checksum` field, in the form `<alg>:<hex>` — `sha256` where the
algorithm is left out, and `md5`, `sha1`, `sha256` or `sha512` where it is
given. `--checksum-file` reads it from a shasum-style file instead. Inside a
directory this happens on its own: `site.tar.gz.sha256` beside `site.tar.gz`
becomes that artifact's `checksum` field rather than an attachment of its own.
Either way `ranke-git` computes the digest over the content it is about to
attach and refuses a mismatch, so the field states a checksum that was checked.

### `scan` — record a vulnerability scan's findings

One `derivation/vulnerability_scan` claim citing the commit and each CVE found —
`ranke-git` never parses scanner output; the caller names the findings.

```sh
ranke-git scan --commit <sha> \
  --cve CVE-2024-1234=https://nvd.nist.gov/vuln/detail/CVE-2024-1234 --cve CVE-2024-5678 \
  --file trivy-output.json
```

`--file` is optional — a bare link with no archived scanner output is legitimate.

### `identity register` — provision a real contributor

Mints an ed25519 keypair, contributes its root claim, and writes the signing key to
disk — the one-time bootstrap a real, persistent identity needs (a CI pipeline's
own, say). Refuses to overwrite an existing `--out`.

```sh
ranke-git identity register --server <server> --out contributor.pem
```

Store the written key safely (a CI secret store, a vault) — this command's job ends
at "the identity now exists and here is its key."

### `demo local` / `demo server` — see it work

`demo local` builds a small multi-branch, tagged repo, backs it up, and restores it
— entirely offline, no server. `demo server` does the same story genuinely over the
network against a real `ranke-db`: checks a server is reachable (never starts one),
archives a two-commit tagged repo, attaches a build log, records a CVE scan — run it
twice to watch entity and content-hash reuse.

```sh
ranke-git demo local
ranke-git demo server --server localhost:8080   # start one first: server/run.sh (repo root)
```
