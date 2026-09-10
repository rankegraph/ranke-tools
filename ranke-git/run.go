// package: main / ranke-git
// type:    logic
// job:     wires one action together: connect, prepare (find-or-build + content_hash scan) where
// an action needs it, build claims, contribute only what's new
// limits:  orchestration only; the pieces it calls own their own concerns
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	rankedb "github.com/rankegraph/ranke-db/client"
	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/keysource"
)

// loadSigningKey reads the contributor's ed25519 key through keysource's
// grammar — a path, file:PATH, env:NAME, stdin or prompt — so every app built
// on ranke-go finds a key by one set of rules, the mode check among them.
func loadSigningKey(source string) (ed25519.PrivateKey, error) {
	material, err := keysource.Load(source, os.Stdin, keysource.WithTTY())
	if err != nil {
		return nil, err
	}
	key, err := ranke.ParseEd25519PrivateKeyPEM(material)
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", source, err)
	}
	return key, nil
}

// loadContributor fetches and binds the contributor claim id names, whether
// --contributor-id gave it or the signing key found it.
func loadContributor(ctx context.Context, c *rankedb.Client, branch, id string, key ed25519.PrivateKey) (ranke.Contributor, error) {
	parsed, err := ranke.ParseId(id)
	if err != nil {
		return nil, fmt.Errorf("contributor id %q: %w", id, err)
	}
	claim, err := c.GetClaim(ctx, rankedb.Scope(branch), parsed)
	if err != nil {
		return nil, fmt.Errorf("load contributor %s: %w", id, err)
	}
	self, err := claim.AsContributor(ctx, nil, key)
	if err != nil {
		return nil, fmt.Errorf("bind contributor %s: %w", id, err)
	}
	return self, nil
}

// resolveRef turns the ref a command names into its commit sha, in
// ranke-git's own words where git answers with advice about path separators.
func resolveRef(g gitRepo, ref string) (string, error) {
	sha, err := resolveCommit(g, ref)
	if err == nil {
		return sha, nil
	}
	if _, headErr := g.revParse("HEAD"); headErr != nil {
		return "", fmt.Errorf("%s: %w", g.dir, headErr)
	}
	return "", missingRef(ref)
}

// missingRef names what the clone lacks, and the CI checkout that explains
// it: one branch, no tags, unless the workflow asked for more.
func missingRef(ref string) error {
	switch {
	case strings.HasPrefix(ref, "refs/tags/"):
		return fmt.Errorf("no tag %q in this clone — a CI checkout fetches none by default (actions/checkout: fetch-tags: true, or fetch-depth: 0)",
			strings.TrimPrefix(ref, "refs/tags/"))
	case strings.HasPrefix(ref, "refs/heads/"):
		return fmt.Errorf("no branch %q in this clone — a CI checkout fetches only the branch it was given (actions/checkout: fetch-depth: 0)",
			strings.TrimPrefix(ref, "refs/heads/"))
	}
	return fmt.Errorf("%q names no tag, branch, or commit in this clone", ref)
}

// contributorIDForKey finds the contributor carrying the signing key's
// pubkey. A lookup, never a mint (-> DESIGN.md).
func contributorIDForKey(ctx context.Context, c *rankedb.Client, branch string, key ed25519.PrivateKey) (string, error) {
	pubkey, err := ranke.EncodePublicKey(key.Public())
	if err != nil {
		return "", fmt.Errorf("encode public key: %w", err)
	}
	// Each contributor claim carries its pubkey inline, so the match needs no
	// further fetch. No index runs from a pubkey to the claim holding it.
	all, err := queryClaims(ctx, c, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Eq: ranke.NodeTypeContributor}},
	})
	if err != nil {
		return "", fmt.Errorf("find contributor: %w", err)
	}
	var found []string
	for _, claim := range all {
		self, err := claim.AsContributor(ctx, nil)
		if err != nil {
			continue
		}
		if bytes.Equal(self.Pubkey(), pubkey) {
			found = append(found, claim.ID().String())
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no contributor on branch %q carries this signing key's public key — register one with `ranke-git identity register`, or name an existing one with --contributor-id", branch)
	case 1:
		return found[0], nil
	}
	// Nothing makes a pubkey unique: one key registered twice is two identities
	// carrying different provenance, and only the caller knows which it meant.
	return "", fmt.Errorf("%d contributors on branch %q carry this key (%s) — name the one to sign as with --contributor-id", len(found), branch, strings.Join(found, ", "))
}

// dial points a client at addr, presenting at most one credential — the
// endpoint routes on the scheme it is given, and refuses two as ambiguous.
func dial(addr string, o *options) (*rankedb.Client, error) {
	var opts []rankedb.Option
	switch {
	case o.token != "":
		opts = append(opts, rankedb.WithToken(o.token))
	case o.apiKey != "":
		opts = append(opts, rankedb.WithAPIKey(o.apiKey))
	}
	c, err := rankedb.New(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", addr, err)
	}
	return c, nil
}

// session is what every action needs before it does its own work: a live
// client and a bound contributor to sign as.
type session struct {
	client      *rankedb.Client
	contributor ranke.Contributor
	signer      crypto.Signer
}

// connect loads the signing key, waits for the server, and binds the
// contributor claim o names — the setup every action shares.
func connect(ctx context.Context, o *options) (*session, error) {
	if o.server == "" {
		return nil, fmt.Errorf("--server is required")
	}
	if o.signingKey == "" {
		return nil, fmt.Errorf("--signing-key is required")
	}
	key, err := loadSigningKey(o.signingKey)
	if err != nil {
		return nil, err
	}
	c, err := dial(o.server, o)
	if err != nil {
		return nil, err
	}
	if err := c.WaitReady(ctx, 10*time.Second); err != nil {
		return nil, fmt.Errorf("%s: %w", o.server, err)
	}
	id := o.contributorID
	if id == "" {
		if id, err = contributorIDForKey(ctx, c, o.branch, key); err != nil {
			return nil, err
		}
	}
	contributor, err := loadContributor(ctx, c, o.branch, id, key)
	if err != nil {
		return nil, err
	}
	return &session{client: c, contributor: contributor, signer: key}, nil
}

// contributeAndReport advances a dev server's clock past the batch, merges
// the claims, and prints what landed. u is where an externally-content
// claim's bytes come from.
func contributeAndReport(ctx context.Context, c *rankedb.Client, u ranke.Universe, branch string, claims []ranke.Claim, out io.Writer) error {
	if _, err := c.Dev().AdvanceClockPast(ctx, claims); err != nil {
		return err
	}
	res, err := c.Contribute(ctx, u, branch, claims, rankedb.Creating())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, ">> merged %d claim(s) onto %q, head %s\n", len(res.Ids), branch, res.Head)
	return nil
}

// projectFromRepoURL reads the project name off the repo's own URL — its last
// segment, without the .git — so the ordinary one-project repository names
// itself. Every remote form ends the same way, whether scp-like
// (git@host:acme/widgets.git), a URL, or a local path. A monorepo names its
// projects with --project instead, one run each.
func projectFromRepoURL(repoURL string) string {
	name := strings.TrimRight(strings.TrimSpace(repoURL), "/")
	name = strings.TrimSuffix(name, ".git")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// shapeFunc builds one action's claims into u, once the contributor,
// signer, and prep are ready.
type shapeFunc func(ctx context.Context, contributor ranke.Contributor, signer crypto.Signer, p prep, u ranke.Universe) ([]ranke.Claim, error)

// run connects, prepares, builds via shape, contributes only what's new —
// snapshot/backup's shared shape (-> DESIGN.md); attach does its own.
func run(cmd *cobra.Command, o *options, shape shapeFunc) error {
	ctx := cmd.Context()
	if o.repoURL == "" {
		if o.clone == "" {
			return fmt.Errorf("--repo is required")
		}
		originURL, err := gitRepo{dir: o.clone}.originURL()
		if err != nil {
			return fmt.Errorf("--repo is required: the clone has no origin to take it from: %w", err)
		}
		o.repoURL = originURL
	}
	if o.project == "" {
		o.project = projectFromRepoURL(o.repoURL)
		if o.project == "" {
			return fmt.Errorf("--project is required: %q carries no name to derive one from", o.repoURL)
		}
	}
	s, err := connect(ctx, o)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, ">> preparing — find or build %s/%s, scanning existing content on %q\n", o.repoURL, o.project, o.branch)
	p, err := prepare(ctx, s.client, o.branch, o.repoURL, o.project)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, ">> %d known object(s) to reuse\n", len(p.knownHashes))

	u := ranke.NewMemoryUniverse()
	claims, err := shape(ctx, s.contributor, s.signer, p, u)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		fmt.Fprintln(out, ">> nothing new — everything was already archived")
		return nil
	}
	return contributeAndReport(ctx, s.client, u, o.branch, claims, out)
}

// localRepo resolves the git repo this run reads from: --clone if given.
// Cloning --repo directly isn't built yet (-> DESIGN.md).
func localRepo(o *options) (gitRepo, error) {
	if o.clone == "" {
		return gitRepo{}, fmt.Errorf("--clone is required for now — cloning --repo directly isn't built yet")
	}
	return gitRepo{dir: o.clone}, nil
}
