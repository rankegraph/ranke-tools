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
`snapshot`/`backup`) a repo and project name. Mint a contributor once:

```sh
ranke-git identity register --server https://ranke-db.example.com --out contributor.pem
# prints a --contributor-id to reuse below
```

Then archive a commit:

```sh
ranke-git snapshot \
  --server https://ranke-db.example.com \
  --contributor-id <id printed above> --signing-key contributor.pem \
  --clone /path/to/local/checkout --repo https://github.com/acme/widgets.git \
  --project widgets --ref HEAD
```

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
ranke-git attach --commit <sha> --type build_log --name "release build log" \
  --content-type text/plain --file build.log
```

`--type` becomes `source/git_<type>` — never a bare string, never parsed by
`ranke-git` itself. `--file` omitted reads stdin, so a CI step can pipe its own
log straight through.

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
