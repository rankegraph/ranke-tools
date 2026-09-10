# ranke-git — design notes

## Use case

Git is not immutable — a force-push or a squash can erase history a project once
depended on. `ranke-git` archives git state into a Ranke-Graph so it survives that,
in two modes that share one mechanism:

- **snapshot** — the exact source an artifact was built from: one commit's tree
  (optionally narrowed to a list of paths, for a monorepo), byte-exact, restorable
  as that one commit but not as a working clone (no `.git`, no history, no refs).
- **backup** — a reconstructible clone: every commit reachable from a list of
  branches and tags, chained to its parents, restorable as a real, walkable git
  repository with the same branches and tags back.

Snapshot is a scope-limited subset of backup, not a different shape: same claim
types, same content strategy, same dedup mechanism. Backup walks every commit
reachable from every given ref and follows parent edges; snapshot walks exactly
one commit, follows none, and may narrow to a path subset besides.

## Claim shape

All of it stays in the `source` class — capture, not interpretation, all the way
down. Nothing here is a `derivation`. Every type is namespaced `git_` —
`commit`, `tree`, `blob`, `tag`, `ref` are common enough words that a bare one
invites collision with another tool's own vocabulary in the same archive-wide,
open `V-TYPE` namespace (`gitPrefix`, convert.go — shared with `attach`'s
own subtype prefix).

- **`source/git_commit`** — one claim per git commit. Content is git's own
  raw commit object bytes, verbatim, stored external (`content_hash`). Fields
  carry the git commit sha for lookup (`git_sha`), a `ranke_git_version` field
  recording which release built it (`main.go`'s `version`, stamped at release
  build time — a later breaking change to how trees/blobs are shaped still has
  an old tool version on hand to restore against; catching and handling that is
  the newer version's own job, not something baked into every file claim — this
  field lives only here, not on trees/blobs/tags/refs, exactly because it's the
  head a restore starts from, not a per-file concern), and, in snapshot mode,
  the parent's git sha as a plain field even when no parent claim exists to
  cite (an honest record of what wasn't captured, not a broken reference). Also
  carries `dated` (`V-DATED`): the commit's own author date (not committer —
  closer to "when the subject stems from," and immune to a rebase moving the
  latter), distinct from `created_at` (when the archive witnessed it, always
  real now). Passed to `WithDatedEDTF` as git's own ISO 8601 output, unconverted
  — `ranke-go` v0.25.1's instant branch accepts any RFC 3339 form, any offset,
  so no reformatting is needed to keep full precision. (Originally reformatted
  to `created_at`'s own exact UTC/nanosecond wire form, worked around a real
  `ranke-go` bug — its EDTF Level 1 parser has no time-of-day at all, `sd-b071b2`
  — since fixed upstream; `WithDatedEDTF` still names the setter, for whenever a
  genuinely EDTF-only form — an interval, an uncertain year — is worth adding.)
- **`source/git_tree`** — one claim per git tree object (one per directory
  level, nested — never flattened). Content is git's raw tree object bytes,
  external. Cites each entry (blob or subtree) via an edge carrying
  `fields: {name, mode, path}` — the structural information a tree object
  encodes, plus the entry's full path from the commit root (`name` alone is
  just the local one), available as edge fields independent of what the raw
  content holds.
- **`source/git_blob`** — one claim per git blob. Content is the file's
  bytes exactly — already byte-identical to a git blob's payload — external, so
  identical files anywhere in the walk share one `content_hash` regardless of
  path or commit. Also carries a `path` field, for readability browsing the
  claim itself — but it only ever reflects where THIS claim was first minted; a
  blob shared across paths or commits keeps citing the same claim, so its
  authoritative, per-occurrence path is the tree entry edge's own `path` field,
  not this one. Same story for `dated`: the owning commit's own author date at
  first mint (the file's "captured at," not updated on reuse) — backup mode's
  parent-first walk means this naturally lands as the oldest commit *in that
  run's walk* to introduce the content, not a true git-blame first-introduction
  across all of history, but cheap and honest about what it actually knows.
- **`source/git_tag`** — one claim per *annotated* tag object (git's fourth
  object kind), carried exactly like commit/tree/blob: raw bytes, external,
  byte-exact. A *lightweight* tag has no such object — it's just a name pointing
  at a commit, so it never gets one of these.
- **`source/git_ref`** — one claim per branch or tag name, as it resolved
  at backup time. Small inline content (the name itself), `fields: {name, kind}`,
  and one `derivation/points_at` edge to what it names — the commit directly for
  a branch or a lightweight tag, the `source/git_tag` claim for an
  annotated one. This is what lets a restore recreate the same branches and
  tags, not just the same objects.

Storing git's raw object bytes (rather than decomposing and re-deriving git's
encoding at restore time) is what makes byte-exact restore trivial and testable:
after restore, recompute the commit's git sha and compare it to the original — one
hash, not a file diff, proving structure, modes, content, message, and timestamps
all matched.

### Edges beyond the tree/blob structure

Edge classes are closed to `derivation`, `relation`, and `contribution` (`V-TYPE`)
— there is no `source/*` edge class, but a `source`-class *node* citing another
source via a `derivation`- or `relation`-class edge is normal (see: an ingestion
worker citing the dump an `.eml` was split from). This design uses:

- `source/git_commit` → its root `source/git_tree`: a `derivation`-class edge.
- `source/git_tree` → each entry: a `derivation`-class edge, `fields: {name, mode}`.
- `source/git_commit` → its git parent commit(s): a `derivation`-class edge. **Backup
  only** — this is the edge that reconstructs the repo's real commit graph inside
  the archive. Snapshot mode never follows it; the parent sha still rides as a
  plain field (see above).
- `source/git_ref` → what it names: a `derivation/points_at` edge, to a commit
  directly or, for an annotated tag, to its `source/git_tag` claim.
- `source/git_commit`/`source/git_tag` → `entity/repository`/`entity/project`: intended
  to be a `relation/*`-class edge (e.g. `relation/snapshot_of`), present on
  **every** run once phase 2's find-or-build exists — a claim can't grow new edges after
  signing, so this is how a later run would re-establish "this documents that
  entity" against one only created once. **Not built yet** — see Entities below.

## Entities

`entity/repository` and `entity/project` are the stable, abstract things — "this
repo", "this project" — as opposed to the concrete captured material. Each needs a
path back to a source (`D1`), satisfied once, at creation:

- Find or build (`prepare.go`'s `findOne`): query first, by a stable field (`url`
  for the repo, `name` for the project); reuse the id and height if found, mint
  fresh — `derivation/input`-anchored to that run's primary commit — if not. Two
  independent runs won't converge on the same entity id by content-addressing
  alone (`created_at` differs), so this stays an explicit lookup rather than
  something automatic. Built and verified live: a second run against the same
  repo/project reuses both entities, contributing neither again.
- A project's "lives in this repo" fact is a `relation/*` edge directly on the
  `entity/project` claim, pointing at the repo entity — a plain binary fact needs
  no reified `relation/*` node; reification is for genuine n:n relations between
  entities (foundation paper §Relations).

## Attachments

`ranke-git attach` cites arbitrary content onto an already-archived commit — a
build log, a test report, a scan's output, a platform-specific artifact binary.
The driving case: after release CI, `snapshot` the repo, then `attach` its logs
and its build outputs against the same commit.

Every attachment is a `source` claim, no exceptions, however different its
*content* is from a log. `ranke-git` never parses what it's carrying — the
release-process generator in `ranke-db` classifies a vulnerability *scan* as a
`derivation` (it interprets source code against known CVEs and links the
entities it matched), but that tool knows the semantic content and deliberately
builds that graph; `ranke-git` doesn't, anywhere, and attach is no exception.
A scanner's raw output, attached here, is exactly as uninterpreted as a build
log or a Windows-vs-Linux artifact pair — `source`, all of it, unconditionally.

Two axes, both settable, kept independent:

- `--type` sets the subtype, always assembled as `source/git_<type>` (the
  same `gitPrefix` every claim type here uses) — never a bare `--type`
  string, and never a class other than `source`. The list of kinds here (logs,
  test results, scan output, per-platform artifacts...) is open-ended by
  design, so there's no fixed enum to validate against instead. `subtypeChars`
  enforces the ADT's own character rule (`checkSubtype`, shared with
  `R-FIELDS`' field-name shape: `[a-z0-9][a-z0-9_]*`) before any network round
  trip, so a bad `--type` fails immediately, not inside the library.
- `--content-type` is the attachment's actual MIME encoding — free-form,
  independent of `--type`, since a `build_log` could be `text/plain` today and
  something else tomorrow.

`--name` is the human title either way — a log's caption or an artifact's
filename, the same field `entity/project` and `source/git_ref` already
use for the same purpose.

The target commit is named by `--commit`, or by `--ref`/`--git-tag`/
`--git-branch` resolved in `--clone`. That is one `rev-parse`, the whole of
attach's business with git: the release process holds a tag, and making it
convert that to a sha by hand was work this tool can do. Everything after
stays server- and content-facing, as before.

A run attaches as many files as it is given: each positional path is a file,
or a directory standing for every file beneath it, and each file becomes its
own claim named by its path within that directory. They contribute as one
batch, so a release's whole asset set advances the head once. What the forge
is — how those files got downloaded — stays outside: `gh release download`
writes a directory, `attach` reads one, and `ranke-git` learns nothing about
GitHub.

`--checksum` adds one optional field, `checksum`, holding `<alg>:<hex>`. The
archive already content-addresses the bytes, so this records a different fact:
the digest the release process published, in the algorithm a downstream
consumer will check against. `ranke-git` computes that digest over the content
first and refuses a mismatch — a checksum nobody verified would be a signed
claim resting on the operator's typing. Within a directory the pairing is
automatic: `site.tar.gz.sha256` names `site.tar.gz`, which is in the same
batch, so the digest becomes that artifact's field. The rule is that narrow on
purpose — a `.sha256` whose file is absent stays an attachment of its own,
rather than disappearing into a claim nothing else names.

The claim cites its target via `relation/attached_to` (`RelationTo`) — a
relation, not a `derivation/input`: the attachment isn't an interpretation of
the commit's content, it's evidence associated with the same point in time,
the same shape as the release generator's `relation/mentions`. Content is
always external, same as every other git-object claim, so two identical
attachments (a log rerun byte-for-byte) share one `content_hash`. No git repo
is touched — `attach` finds its target purely by querying the branch for the
`source/git_commit` claim with a matching `git_sha` (`findOne`, the same
helper find-or-build uses), then contributes one claim. `--file` or stdin
supplies the bytes.

## Vulnerability scans

`ranke-git scan` records a scan's findings against an already-archived commit — one
`derivation/vulnerability_scan` claim per run, unlike `attach`: the raw tool output
(when archived) is received like a source, but the claim itself asserts an
interpretation ("scanning this commit found these CVEs"), which is what `derivation`
is for here — a convention for this tool's own claims, not a general rule (the class
boundary itself isn't formally decidable; the foundation paper leaves it to the
application).

- `entity/cve` — found or built exactly like `entity/repository`/`entity/project`
  (`findOrBuildCVE` reuses `findOne`), fields `{cve_id, url (optional)}`, no content,
  D1-anchored via `derivation/input` to the commit on first mint. Unprefixed, unlike
  `attach`'s `git_` types — "CVE" is an external, standardized identifier
  namespace already, not an open word another tool might mean something else by.
- The scan claim cites the commit via `derivation/input` (an interpretation of its
  source, unlike `attach`'s `relation/attached_to`) and each finding via one
  `relation/cve` edge per CVE — a claim can carry several edges of the same type to
  different targets, so N findings is just N edges, no reified node needed (a scan's
  findings belong to that one scan, not an independent fact needing a claim of its own).
- Content is optional: `--file` archives the scanner's raw output (external, same
  shape as everything else here) when given, entirely absent when not — `V-CONTENT`
  permits a claim with neither inline nor external content, confirmed against both
  the spec and `ranke-go`'s `ClaimBuilder.Sign()` before relying on it, and live
  against a running instance (`buildScan`'s contentless path signs and contributes).
  Re-scanning the same commit for the same CVEs mints no new `entity/cve` (found,
  not rebuilt) but always a fresh scan claim — each run is its own event, not expected to
  dedupe the way an unchanged file does (same reasoning as attach's own content-hash
  question below).

Built and verified live: `entity/cve` reused across separate `scan` runs and across
different findings sharing one CVE, and a contentless scan claim round-trips through
a real contribute.

## `demo server`'s timeline and contributors

`ranke-git demo server` (demo_server.go) exists to show what the tool actually does
against a live instance, so it stays honest to how a real release looks, not a
convenience shortcut:

- Every claim across one run used to date within the same instant (the whole run
  completes in well under a second) — a flat, unrealistic-looking timeline. Now each
  phase (archive, attach, scan) gets its own timestamp, hours apart, counted forward
  from real now; the two git commits get their own real `GIT_AUTHOR_DATE`/
  `GIT_COMMITTER_DATE`, hours apart too (`demoServerTimeline`, `demoCommit`'s `at`).
  Forward only, never backdated — the dev sequencer's own clock (`/dev/clock`) never
  moves backward (mirrors `cmd/generator`'s ambient-clock pattern in `ranke-db`), so a
  synthetic story has to count up from wherever real now already is.
- This is also what `contributeAndReport`'s `maxCreatedAt` fix is for: it used to
  advance the dev clock by a fixed "+1 minute from real now," which only worked
  because every claim was, in fact, built moments before contributing. Once a claim
  can carry a timestamp hours away from real now, the clock has to track the batch's
  own latest `created_at` exactly, not a fixed offset — a real fix for every command,
  not just the demo.
- Two contributors sign it, not one: a CI pipeline attests the archive and its
  build log, a separate scanner attests the scan and its CVEs. A claim's
  signature is who attested it, not access control, so one actor signing everything
  would misrepresent the story the graph tells.

## Provisioning a contributor

`ranke-client`, the CLI `ranke-db` ships, mints and registers contributors:
`branch create` contributes the contributor claim the branch is written under,
`contributor list` reports what an archive holds. `ranke-git` had its own
`identity register` for a while, from before that existed — one repository's
guess at a shape the server now defines, and a second place for the rules to
drift. It signs as a contributor and provisions none.

The intended shape: a CI step runs `ranke-git snapshot` on every push, signing
as a contributor provisioned once — the archive grows forward from whenever it
is switched on, one real commit at a time, dated today (no historical
backfill, so no `V-MONO` risk from non-monotonic git history).

## Talking to a server

`ranke-db/client` is the transport, the official Go client generated from the
same `openapi.yaml` the server implements. `ranke-git` wrote its own for a
while, on the reasoning that hand-written HTTP kept this repository an
independent check on the REST contract. It did not: a hand-written client
tests one reading of the contract, frozen on the day it was written, and
nothing here failed when the two drifted. The check that does hold is
`demo_server_test.go`, which starts the pinned `ranke-db` release and drives
it over real HTTP — unaffected by whose client sits underneath, and the reason
the pin and the client version move together (`make upgrade`).

`instance.Instance` addresses that server and `contributor.Load` reads the
key, both from `ranke-client`'s own packages: they exist as packages because
its verb packages need them, and a second tool needing the same two things is
the case they already serve. A third credential comes with them, `--macaroon`.

What stays out is the server itself: its adapters, its config, its storage.
`go list -deps` over the client reaches the generated OpenAPI layer and what
ranke-go already brings, and nothing else.

The client also holds what a contributor claim is: `NewContributor` builds and
signs it, `ContributorsFor` picks a key's own out of the claims a read
returned. Each is a shape this repository had written for itself, three times
over in the case of the claim. The read is scoped to the branch being written,
since a branch holds its own contributor claim for a key (ranke-db v1.26.0) —
`Client.Contributors` would answer archive-wide and want **R** on `$archive`,
a right a least-privileged CI account has no reason to hold.

## Sending content

`WriteClaim` carries only a claim's own record — for external content that's
just `content_hash`/`content_size`, never the bytes. `Client.Contribute`
gathers the content itself, reading each externally-content claim's bytes back
from the Universe the build phase wrote them into and deduping by hash within
the batch, so a blob two claims share goes out once. Missing this was a real,
confirmed bug in this repository's own client for a while: the
claim records reached the server and even satisfied re-run dedup (which only
ever reads the `content_hash` *field*, never fetches the bytes it names), so
everything *looked* correct while every blob's actual content was silently
absent server-side. Caught by fetching a blob's content back after a live
contribute and getting a 404; fixed, and now the same fetch returns the bytes.

## Dedup within one run

`bySha` memoises every commit, tree, blob, and tag by git sha, so
a commit reachable from two refs, an unchanged subtree, or a repeated blob becomes
one claim, cited more than once rather than rebuilt — this part is built and
tested (`TestRoundTripDedupesRepeatedBlobs`, `TestBackupRoundTripIsByteExact`).

## Dedup across separate runs

`content_hash` is the reuse key at every level — `source/git_commit` and
`source/git_tree` get one from their external raw-bytes content the same
way `source/git_blob` always has, and unlike a claim's own id,
`content_hash` is time-independent (no `created_at` in it), so it's stable
across separate runs, unlike `bySha`'s in-memory map above which starts empty
every run.

`prepare.go`'s `scanContentHashes` queries every `source/git_commit`/`tree`/`blob`/`tag`
claim **on the destination branch**, keyed by `content_hash`, before the build
phase mints anything — a flat type filter, not a graph walk from the repo entity
as first sketched: simpler, and equivalent in effect as long as one branch holds
one repo's history, which is this tool's own convention. The trade-off is real
though — a branch shared by more than one repo would pool their content_hashes
together too (harmless dedup, but worth knowing it isn't strictly repo-scoped).
`converter.write` checks this map before minting a git-object claim; `converter`
never even calls `PutContents`/`Sign` for something already found there.

Verified live against a running instance: an unchanged re-run of the same
commit contributes nothing at all ("everything was already archived"); a
one-file change contributes exactly the changed blob, the tree that now cites
it, and the new commit — three claims, not the whole tree again.

## Not yet decided

- Cloning `--repo` directly. `--clone` (an existing local checkout) is the only
  path in today; a bare `--repo` with no `--clone` refuses rather than cloning.
- Enumerating "every branch/tag" for `backup` — today's `--git-branch`/`--git-tag`
  are named explicitly, not discovered from the repo.
- `relation/snapshot_of` (see Edges above): once find-or-build finds an existing entity,
  nothing yet records that *this* run's commit also relates to it — only the
  founding commit's `derivation/input` edge does. Worth adding once something
  needs to walk "every snapshot of this repo," not just "the first one."
- Whether `entity/artifact` (an artifact as its own stable, referenceable thing —
  D1-anchored to a `derivation/build` citing the snapshot) is worth adding now or
  only once something actually needs to query artifacts as things across builds.
  `attach` covers "carry the bytes" today; it doesn't give an artifact its own
  claim of its own to reference from elsewhere.
- Content-hash reuse across separate `attach` runs. Unlike `snapshot`/`backup`,
  `attach` never consults `prepare`'s `knownHashes` — attaching the same log
  twice mints two claims, not one reused. Likely fine (an attachment is tied to
  one run, not expected to repeat the way an unchanged file does), but untested
  either way.
- Encoding a submodule (gitlink, mode 160000) — currently refused outright
  (`TestSubmoduleIsRefused`).
