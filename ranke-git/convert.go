// package: main (ranke-git) / convert
// type:    logic
// job:     converts git state to Ranke claims, byte-exact — the way back is restore.go's
// limits:  local only — no ranke-db reads or writes; prepare.go reads the server and
// hands this a prep to reuse from
package main

import (
	"cmp"
	"context"
	"crypto"
	"fmt"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
)

// gitPrefix namespaces every source/derivation subtype — a bare word like
// "commit" invites collision in V-TYPE's shared vocabulary.
const gitPrefix = "git_"

// The claim types this conversion produces.
const (
	nodeCommit     = "source/" + gitPrefix + "commit"
	nodeTree       = "source/" + gitPrefix + "tree"
	nodeBlob       = "source/" + gitPrefix + "blob"
	nodeTag        = "source/" + gitPrefix + "tag" // an annotated tag object; a lightweight tag has none
	nodeRef        = "source/" + gitPrefix + "ref" // a branch or tag name, as it resolved at backup time
	nodeRepository = "entity/repository"
	nodeProject    = "entity/project"
	nodeVersion    = "entity/version" // the tag or commit a run archived, as a thing in the world
)

// The structural and semantic edges beyond the automatic contributor edge.
const (
	edgeTree      = "derivation/tree"      // commit -> its root tree
	edgeEntry     = "derivation/entry"     // tree -> one entry; fields: name, mode
	edgeParent    = "derivation/parent"    // commit -> a git parent (backup only)
	edgePointsAt  = "derivation/points_at" // ref -> a commit or tag object; tag -> its commit
	edgeInput     = "derivation/input"     // entity -> its founding commit (D1)
	edgeHostedIn  = "relation/hosted_in"   // project -> repository
	edgeVersionOf = "relation/version_of"  // version -> project
)

// Claim lookup keys, alongside content_hash (-> DESIGN.md).
const (
	gitShaField      = "git_sha"
	parentShaField   = "parent_git_sha"
	versionNameField = "version"
	projectField     = "name"
)

// versionField records which ranke-git built the commit claim — the head of
// a snapshot/backup, never a file — restoring a later breaking change with.
const versionField = "ranke_git_version"

// heightOver is one above the tallest claim a new one references, its
// contributor among them (V-HEIGHT, -> DESIGN.md).
func heightOver(contributor ranke.Contributor, refs ...uint64) uint64 {
	h := contributor.Node().Height()
	for _, r := range refs {
		if r > h {
			h = r
		}
	}
	return h + 1
}

// made is a claim this walk knows about: built fresh (claim set), or already
// on the server and reused (claim nil — nothing new to contribute for it).
type made struct {
	id     ranke.Id
	claim  ranke.Claim
	height uint64
	gitSha string // a commit's own sha, for the version entity naming it
}

// reused is what the preparational phase found already on the server for one
// lookup key — enough to cite it without rebuilding it.
type reused struct {
	id     ranke.Id
	height uint64
}

// prep is what the preparational phase found — the zero value means nothing,
// mint everything (-> prepare.go).
type prep struct {
	repository  *reused
	project     *reused
	version     *reused
	knownHashes map[string]reused // content_hash string -> existing commit/tree/blob/tag
}

// converter walks git objects bottom-up, memoising by git sha so a repeated
// blob, tree, or commit becomes one claim, cited more than once.
type converter struct {
	ctx           context.Context
	git           gitRepo
	u             ranke.Universe
	contributor   ranke.Contributor
	signer        crypto.Signer
	at            time.Time
	bySha         map[string]made
	claims        []ranke.Claim // only what's new — what the build phase must contribute
	followParents bool          // backup's full history vs. snapshot's one commit
	scope         []string      // snapshot only; nil = the whole tree
	scopeSeen     map[string]bool
	archivedTag   string // the tag a run archived, empty where it named a commit instead
	prep          prep
}

// resolveCommit resolves ref — a tag, a branch, or a raw sha — to the commit
// it names, peeling an annotated tag to what it points at.
func resolveCommit(g gitRepo, ref string) (string, error) {
	sha, err := g.revParse(ref + "^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", ref, err)
	}
	return sha, nil
}

// normalizeScope strips leading/trailing slashes, so "services/api/" and
// "/services/api" match the same paths ls-tree reports.
func normalizeScope(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.Trim(p, "/")
	}
	return out
}

// scopeRelation is how one tree path relates to a requested scope.
type scopeRelation int

const (
	scopeOut    scopeRelation = iota // excluded: no claim, no edge
	scopeToward                      // an ancestor of a scoped path; must recurse to find it
	scopeIn                          // the scoped path, or already inside it: capture in full
)

// scopeMatch classifies path against scope (empty = the whole tree); "toward"
// recurses past a tree without capturing every sibling (-> DESIGN.md).
func scopeMatch(scope []string, path string) scopeRelation {
	if len(scope) == 0 {
		return scopeIn
	}
	for _, s := range scope {
		if path == s || strings.HasPrefix(path, s+"/") {
			return scopeIn
		}
		if path == "" || s == path || strings.HasPrefix(s, path+"/") {
			return scopeToward
		}
	}
	return scopeOut
}

// gitToClaims converts one commit: ref resolved, no parent history, scope
// optional — snapshot's shape. tag names the version entity, a zero at now.
func gitToClaims(
	ctx context.Context, g gitRepo, ref, tag string, scope []string, u ranke.Universe,
	contributor ranke.Contributor, signer crypto.Signer, repoURL, project string, p prep, at time.Time,
) ([]ranke.Claim, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	sha, err := resolveCommit(g, ref)
	if err != nil {
		return nil, err
	}
	c := &converter{
		ctx: ctx, git: g, u: u, contributor: contributor, signer: signer,
		at: at, bySha: map[string]made{}, archivedTag: tag,
		scope: normalizeScope(scope), scopeSeen: map[string]bool{}, prep: p,
	}
	commit, err := c.commit(sha)
	if err != nil {
		return nil, err
	}
	for _, s := range c.scope {
		if !c.scopeSeen[s] {
			return nil, fmt.Errorf("scope path %q not found in commit %s", s, sha)
		}
	}
	return c.finish(commit, repoURL, project)
}

// refSpec names one ref to archive: a branch or a tag, by its short name.
type refSpec struct {
	kind string // "branch" or "tag"
	name string
}

// fullRef is the ref's fully-qualified name, what git's own plumbing wants.
func (r refSpec) fullRef() string {
	if r.kind == "tag" {
		return "refs/tags/" + r.name
	}
	return "refs/heads/" + r.name
}

// backupToClaims converts every commit reachable from refs, chained to
// parents, each ref its own nodeRef claim (-> DESIGN.md).
func backupToClaims(
	ctx context.Context, g gitRepo, refs []refSpec, u ranke.Universe,
	contributor ranke.Contributor, signer crypto.Signer, repoURL, project string, p prep, at time.Time,
) ([]ranke.Claim, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("backup: no refs given")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	c := &converter{
		ctx: ctx, git: g, u: u, contributor: contributor, signer: signer,
		at: at, bySha: map[string]made{}, followParents: true, prep: p,
		archivedTag: firstTag(refs),
	}
	var primary made
	for i, r := range refs {
		result, err := c.ref(g, r)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			primary = result.commit
		}
	}
	return c.finish(primary, repoURL, project)
}

// firstTag names the version a backup archives; branches name none.
func firstTag(refs []refSpec) string {
	for _, r := range refs {
		if r.kind == "tag" {
			return r.name
		}
	}
	return ""
}

// finish adds the repository, project and version entities (reused from prep
// if found there), anchored to commit, and returns everything newly built.
func (c *converter) finish(commit made, repoURL, project string) ([]ranke.Claim, error) {
	repo, err := c.repository(repoURL, commit)
	if err != nil {
		return nil, err
	}
	proj, err := c.project(project, commit, repo)
	if err != nil {
		return nil, err
	}
	if _, err := c.version(commit, proj, project); err != nil {
		return nil, err
	}
	return c.claims, nil
}

// commit converts one commit, memoised by sha, parents first if
// followParents — snapshot still records an uncaptured parent's sha as a field.
func (c *converter) commit(sha string) (made, error) {
	if m, ok := c.bySha[sha]; ok {
		return m, nil
	}
	parents, err := c.git.commitParents(sha)
	if err != nil {
		return made{}, fmt.Errorf("commit %s: parents: %w", sha, err)
	}
	fields := map[string]string{gitShaField: sha, versionField: version}
	if len(parents) > 0 {
		fields[parentShaField] = strings.Join(parents, ",")
	}
	dated, err := c.git.commitAuthorDate(sha)
	if err != nil {
		return made{}, fmt.Errorf("commit %s: author date: %w", sha, err)
	}

	var edges []ranke.Edge
	var height uint64
	if c.followParents {
		for _, p := range parents {
			pm, err := c.commit(p)
			if err != nil {
				return made{}, err
			}
			edge, err := ranke.NewEdge(ranke.EdgeConfig{
				Reference: pm.id, Referenced: pm.claim, Type: edgeParent,
			})
			if err != nil {
				return made{}, fmt.Errorf("commit %s: parent edge: %w", sha, err)
			}
			edges = append(edges, edge)
			if pm.height > height {
				height = pm.height
			}
		}
	}

	treeSha, err := c.git.commitTree(sha)
	if err != nil {
		return made{}, fmt.Errorf("commit %s: tree: %w", sha, err)
	}
	root, err := c.tree(treeSha, "", dated)
	if err != nil {
		return made{}, err
	}
	treeEdge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: root.id, Referenced: root.claim, Type: edgeTree,
	})
	if err != nil {
		return made{}, fmt.Errorf("commit %s: root edge: %w", sha, err)
	}
	edges = append(edges, treeEdge)
	if root.height > height {
		height = root.height
	}

	payload, err := c.git.catFile("commit", sha)
	if err != nil {
		return made{}, fmt.Errorf("commit %s: cat-file: %w", sha, err)
	}
	m, err := c.write(nodeCommit, payload, fields, height, edges, dated)
	if err != nil {
		return made{}, fmt.Errorf("commit %s: %w", sha, err)
	}
	m.gitSha = sha
	c.bySha[sha] = m
	return m, nil
}

// tree converts one git tree: entries first (scope permitting), then itself,
// citing each by name and mode. path is relative to the commit root ("" at
// the top); dated is the owning commit's own date, passed through to blob
// (a file's "first captured at" — trees stay purely structural, no dated).
func (c *converter) tree(sha, path, dated string) (made, error) {
	if m, ok := c.bySha[sha]; ok {
		return m, nil
	}
	entries, err := c.git.lsTree(sha)
	if err != nil {
		return made{}, fmt.Errorf("tree %s: %w", sha, err)
	}

	edges := make([]ranke.Edge, 0, len(entries))
	var height uint64
	for _, e := range entries {
		entryPath := e.name
		if path != "" {
			entryPath = path + "/" + e.name
		}
		relation := scopeMatch(c.scope, entryPath)
		if relation == scopeOut {
			continue
		}
		if relation == scopeIn {
			c.markScopeSeen(entryPath)
		}

		var child made
		switch e.typ {
		case "blob":
			child, err = c.blob(e.sha, entryPath, dated)
		case "tree":
			child, err = c.tree(e.sha, entryPath, dated)
		default:
			err = fmt.Errorf("entry %q: unsupported git object type %q", e.name, e.typ)
		}
		if err != nil {
			return made{}, fmt.Errorf("tree %s: %w", sha, err)
		}
		if child.height > height {
			height = child.height
		}
		edge, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: child.id, Referenced: child.claim, Type: edgeEntry,
			Fields: map[string]string{"name": e.name, "mode": e.mode, "path": entryPath},
		})
		if err != nil {
			return made{}, fmt.Errorf("tree %s: entry %q: %w", sha, e.name, err)
		}
		edges = append(edges, edge)
	}

	payload, err := c.git.catFile("tree", sha)
	if err != nil {
		return made{}, fmt.Errorf("tree %s: cat-file: %w", sha, err)
	}
	m, err := c.write(nodeTree, payload, map[string]string{gitShaField: sha}, height, edges, "")
	if err != nil {
		return made{}, fmt.Errorf("tree %s: %w", sha, err)
	}
	c.bySha[sha] = m
	return m, nil
}

// markScopeSeen records that entryPath satisfied one or more requested scope
// paths, so gitToClaims can refuse a scope naming something that was never
// actually reached rather than silently archiving nothing for it.
func (c *converter) markScopeSeen(entryPath string) {
	for _, s := range c.scope {
		if entryPath == s || strings.HasPrefix(entryPath, s+"/") {
			c.scopeSeen[s] = true
		}
	}
}

// blob converts one git blob: content is the file's bytes exactly, so an
// unchanged file shares one content_hash across trees; path and dated just
// record where/when THIS mint first saw it — a later citation's own edge
// tracks its path, and dated is never authoritative for anything but the
// commit that happened to introduce this exact content first.
func (c *converter) blob(sha, path, dated string) (made, error) {
	if m, ok := c.bySha[sha]; ok {
		return m, nil
	}
	payload, err := c.git.catFile("blob", sha)
	if err != nil {
		return made{}, fmt.Errorf("blob %s: cat-file: %w", sha, err)
	}
	m, err := c.write(nodeBlob, payload, map[string]string{gitShaField: sha, "path": path}, 0, nil, dated)
	if err != nil {
		return made{}, fmt.Errorf("blob %s: %w", sha, err)
	}
	c.bySha[sha] = m
	return m, nil
}

// ref resolves and converts one branch or tag: an annotated tag gets its own
// nodeTag claim, a lightweight one or a branch points at the commit
// directly. Either way the ref itself is a fresh nodeRef claim, never reused
// from prep — it records what a name pointed at THIS run.
func (c *converter) ref(g gitRepo, r refSpec) (struct{ ref, commit made }, error) {
	full := r.fullRef()
	typ, err := g.catFileType(full)
	if err != nil {
		return struct{ ref, commit made }{}, fmt.Errorf("ref %s: %w", full, err)
	}
	commitSha, err := resolveCommit(g, full)
	if err != nil {
		return struct{ ref, commit made }{}, fmt.Errorf("ref %s: %w", full, err)
	}
	commit, err := c.commit(commitSha)
	if err != nil {
		return struct{ ref, commit made }{}, err
	}

	target := commit
	if typ == "tag" {
		tagSha, err := g.revParse(full)
		if err != nil {
			return struct{ ref, commit made }{}, fmt.Errorf("ref %s: %w", full, err)
		}
		if target, err = c.tag(tagSha, commit); err != nil {
			return struct{ ref, commit made }{}, err
		}
	}

	edge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: target.id, Referenced: target.claim, Type: edgePointsAt,
	})
	if err != nil {
		return struct{ ref, commit made }{}, fmt.Errorf("ref %s: points_at edge: %w", full, err)
	}
	m, err := c.writeFact(nodeRef, []byte(r.name), map[string]string{"name": r.name, "kind": r.kind}, target.height, []ranke.Edge{edge})
	if err != nil {
		return struct{ ref, commit made }{}, fmt.Errorf("ref %s: %w", full, err)
	}
	return struct{ ref, commit made }{ref: m, commit: commit}, nil
}

// tag converts one annotated tag object, memoised by sha — carried the same
// way as commit/tree/blob: raw bytes, external, byte-exact.
func (c *converter) tag(sha string, commit made) (made, error) {
	if m, ok := c.bySha[sha]; ok {
		return m, nil
	}
	edge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: commit.id, Referenced: commit.claim, Type: edgePointsAt,
	})
	if err != nil {
		return made{}, fmt.Errorf("tag %s: points_at edge: %w", sha, err)
	}
	payload, err := c.git.catFile("tag", sha)
	if err != nil {
		return made{}, fmt.Errorf("tag %s: cat-file: %w", sha, err)
	}
	m, err := c.write(nodeTag, payload, map[string]string{gitShaField: sha}, commit.height, []ranke.Edge{edge}, "")
	if err != nil {
		return made{}, fmt.Errorf("tag %s: %w", sha, err)
	}
	c.bySha[sha] = m
	return m, nil
}

// write builds one git-object claim (external content, git_sha field, edges
// resolved) — or, when prep already found this content, just reuses it.
func (c *converter) write(typ string, payload []byte, fields map[string]string, childHeight uint64, edges []ranke.Edge, dated string) (made, error) {
	id, err := ranke.HashContent(payload)
	if err != nil {
		return made{}, fmt.Errorf("hash content: %w", err)
	}
	if r, ok := c.prep.knownHashes[id.String()]; ok {
		return made{id: r.id, height: r.height}, nil
	}
	if err := c.u.PutContents(c.ctx, []ranke.ContentBlob{{Hash: id, Content: payload}}); err != nil {
		return made{}, fmt.Errorf("store content: %w", err)
	}
	height := heightOver(c.contributor, childHeight)
	b := ranke.NewClaim(typ, c.contributor).
		WithExternalContent(id, uint64(len(payload))).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(c.at).
		WithDatedEDTF(dated).
		WithHeight(height).
		WithEdges(edges...)
	for k, v := range fields {
		b = b.WithField(k, v)
	}
	claim, err := b.Sign(c.signer)
	if err != nil {
		return made{}, err
	}
	c.claims = append(c.claims, claim)
	return made{id: claim.ID(), claim: claim, height: height}, nil
}

// repository reuses prep's match if one was found; otherwise builds
// entity/repository fresh, D1-anchored to the commit that first evidenced
// it — its url is what a restore needs back to reconfigure origin.
func (c *converter) repository(repoURL string, commit made) (made, error) {
	if c.prep.repository != nil {
		return made{id: c.prep.repository.id, height: c.prep.repository.height}, nil
	}
	input, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: commit.id, Referenced: commit.claim, Type: edgeInput,
	})
	if err != nil {
		return made{}, fmt.Errorf("repository: input edge: %w", err)
	}
	m, err := c.writeFact(nodeRepository, []byte(repoURL), map[string]string{"url": repoURL}, commit.height, []ranke.Edge{input})
	if err != nil {
		return made{}, fmt.Errorf("repository: %w", err)
	}
	return m, nil
}

// project reuses prep's match if one was found; otherwise builds
// entity/project fresh, D1-anchored like repository, plus a
// relation/hosted_in edge — a binary fact needs no reified node (-> DESIGN.md).
func (c *converter) project(name string, commit, repo made) (made, error) {
	if c.prep.project != nil {
		return made{id: c.prep.project.id, height: c.prep.project.height}, nil
	}
	input, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: commit.id, Referenced: commit.claim, Type: edgeInput,
	})
	if err != nil {
		return made{}, fmt.Errorf("project: input edge: %w", err)
	}
	hostedIn, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: repo.id, Referenced: repo.claim, Type: edgeHostedIn,
		RelationDirection: ranke.RelationTo,
	})
	if err != nil {
		return made{}, fmt.Errorf("project: hosted_in edge: %w", err)
	}
	height := commit.height
	if repo.height > height {
		height = repo.height
	}
	m, err := c.writeFact(nodeProject, []byte(name), map[string]string{"name": name}, height, []ranke.Edge{input, hostedIn})
	if err != nil {
		return made{}, fmt.Errorf("project: %w", err)
	}
	return m, nil
}

// version reuses prep's match, else builds entity/version fresh: the tag a
// run archived, or the commit's own sha where none named it (-> DESIGN.md).
func (c *converter) version(commit, project made, projectName string) (made, error) {
	if c.prep.version != nil {
		return made{id: c.prep.version.id, height: c.prep.version.height}, nil
	}
	input, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: commit.id, Referenced: commit.claim, Type: edgeInput,
	})
	if err != nil {
		return made{}, fmt.Errorf("version: input edge: %w", err)
	}
	versionOf, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: project.id, Referenced: project.claim, Type: edgeVersionOf,
		RelationDirection: ranke.RelationTo,
	})
	if err != nil {
		return made{}, fmt.Errorf("version: version_of edge: %w", err)
	}
	height := commit.height
	if project.height > height {
		height = project.height
	}
	name := cmp.Or(c.archivedTag, commit.gitSha)
	fields := map[string]string{versionNameField: name, projectField: projectName, gitShaField: commit.gitSha}
	m, err := c.writeFact(nodeVersion, []byte(name), fields, height, []ranke.Edge{input, versionOf})
	if err != nil {
		return made{}, fmt.Errorf("version: %w", err)
	}
	return m, nil
}

// writeFact builds one small claim with no git object of its own — an entity
// or a ref: inline content, fields for a later find-or-build lookup.
func (c *converter) writeFact(typ string, content []byte, fields map[string]string, childHeight uint64, edges []ranke.Edge) (made, error) {
	height := heightOver(c.contributor, childHeight)
	b := ranke.NewClaim(typ, c.contributor).
		WithInlineContent(content).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(c.at).
		WithHeight(height).
		WithEdges(edges...)
	for k, v := range fields {
		b = b.WithField(k, v)
	}
	claim, err := b.Sign(c.signer)
	if err != nil {
		return made{}, err
	}
	c.claims = append(c.claims, claim)
	return made{id: claim.ID(), claim: claim, height: height}, nil
}
