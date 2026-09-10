# ranke-tools

A home for standalone tools that write to a running `ranke-db` instance as ordinary
clients — each its own contributor, over the documented REST API. Claims are built
and signed with `github.com/rankegraph/ranke-go`, and sent with
`github.com/rankegraph/ranke-db/client`, the official Go client that lives beside
the OpenAPI contract it is generated from.

Every tool here is an external consumer of that contract, at a pinned version, by a
released binary and never a sibling path or a `replace`. What proves it still holds
is the live test: `ranke-git`'s own suite starts the pinned `ranke-db` release and
drives it over HTTP, so a version that no longer answers fails a test here rather
than in production.

One Go module, one subdirectory per tool.

Every tool shares one version, released as one bundle (`make release`) — see each
tool's own manual for what that version actually does.

## Tools

- [`ranke-git`](./ranke-git/README.md) — archives an exact git tree (or a repo's
  full history) into a Ranke-Graph archive, byte-exact and content-deduplicated,
  attaches arbitrary content (build logs, artifacts, reports) onto an archived
  commit, records vulnerability scan findings, and provisions the contributor
  identities all of that signs as.

More tools are coming soon.

## Running a dev server

`server/run.sh` runs the `ranke-db` release binary `server/.rankedb-version` pins,
against `server/config.json` — an ephemeral, in-memory instance with a throwaway
signing key, the same shape as `ranke-db`'s own `make dev`. Storage is memory, so
every launch founds its own archive on `main` under a throwaway founder key too.
Nothing persists between runs, and it refuses to start a second instance while one
is already running.

```sh
server/run.sh          # start (installs the pinned binary first, if needed)
server/stop.sh         # stop it from another shell
make upgrade           # move both pins to their latest release, and install the server
```

Two pins carry the graph contract into this repo: the `ranke-go` module every tool
signs its claims with, and the `ranke-db` release the dev server runs. `make upgrade`
moves both, since a contract change lands in the two together. Either pin only moves
there, never implicitly on a plain `run.sh` — a deliberate step, analogous to
`ranke-db`'s own `make upgrade`, rather than the ground shifting under you between
runs. Run it regularly, then `make check`: if ranke-tools breaks against the new
pins, that's exactly what its tests are for.
