// package: main (ranke-git) / run
// type:    logic
// job:     wires one action together: connect, prepare (find-or-build + content_hash scan) where
// an action needs it, build claims, contribute only what's new
// limits:  orchestration only; the pieces it calls own their own concerns
package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	rankedb "github.com/rankegraph/ranke-db/client"
	"github.com/rankegraph/ranke-db/cmd/ranke-client/contributor"
	"github.com/rankegraph/ranke-db/cmd/ranke-client/instance"
	"github.com/rankegraph/ranke-go"
)

// loadContributor fetches and binds the contributor claim id names, whether
// --contributor-id gave it or the signing key found it.
func loadContributor(ctx context.Context, c *rankedb.Client, branch, id string, key crypto.Signer) (ranke.Contributor, error) {
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

// refTarget names the single commit a command works on, --git-branch and
// --git-tag resolving under their own refs/ namespace where --ref leaves the
// order to git.
func refTarget(ref, branch, tag string) (string, error) {
	var given []string
	for _, f := range []struct {
		flag, value string
	}{{"--ref", ref}, {"--git-branch", branch}, {"--git-tag", tag}} {
		if f.value != "" {
			given = append(given, f.flag)
		}
	}
	switch {
	case len(given) == 0:
		return "", fmt.Errorf("one of --ref, --git-branch, or --git-tag is required")
	case len(given) > 1:
		return "", fmt.Errorf("%s name a commit each — give one", strings.Join(given, " and "))
	case branch != "":
		return refSpec{kind: "branch", name: branch}.fullRef(), nil
	case tag != "":
		return refSpec{kind: "tag", name: tag}.fullRef(), nil
	}
	return ref, nil
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

// mintContributor generates a keypair and the contributor claim it signs
// under — ranke-db's own shape for one, so every tool here registers alike.
func mintContributor() (ranke.Contributor, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pubkey, err := ranke.EncodePublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	self, err := rankedb.NewContributor(ranke.Keypair{Private: priv, Pubkey: pubkey})
	if err != nil {
		return nil, nil, err
	}
	return self, priv, nil
}

// contributorIDForKey finds the contributor carrying the signing key's
// pubkey. A lookup, never a mint (-> DESIGN.md).
func contributorIDForKey(ctx context.Context, c *rankedb.Client, branch string, pubkey []byte) (string, error) {
	// Scoped to the branch being written: it holds its own contributor claim
	// for a key, and reading it needs no R on $archive.
	held, err := c.ContributorsFor(ctx, rankedb.Scope(branch), pubkey)
	if err != nil {
		return "", fmt.Errorf("find contributor: %w", err)
	}
	var found []string
	for _, claim := range held {
		found = append(found, claim.ID().String())
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no contributor on branch %q carries this signing key's public key — create the branch with `ranke-client branch create %s --signing-key ...`, or admit the key with `ranke-client contributor add %s --signing-key ...`", branch, branch, branch)
	case 1:
		return found[0], nil
	}
	// Nothing makes a pubkey unique: one key registered twice is two
	// contributors with different provenance, and only the caller knows which.
	return "", fmt.Errorf("%d contributors on branch %q carry this key (%s) — name the one to sign as with --contributor-id", len(found), branch, strings.Join(found, ", "))
}

// dial points a client at addr with whatever credential the flags carry,
// through ranke-client's own wiring.
func dial(addr string, o *options) (*rankedb.Client, error) {
	inst := instance.Instance{URL: addr, Token: o.token, APIKey: o.apiKey, Macaroon: o.macaroon}
	c, err := inst.Connect()
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
	pair, err := contributor.Load(o.signingKey, os.Stdin)
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
		if id, err = contributorIDForKey(ctx, c, o.branch, pair.Pubkey); err != nil {
			return nil, err
		}
	}
	self, err := loadContributor(ctx, c, o.branch, id, pair.Private)
	if err != nil {
		return nil, err
	}
	return &session{client: c, contributor: self, signer: pair.Private}, nil
}

// contribute merges claims onto branch, a dev server's clock moved past the
// batch first: it starts at the epoch, and R-C2DATE refuses a claim dated
// after the base. u holds an externally-content claim's bytes.
func contribute(ctx context.Context, c *rankedb.Client, u ranke.Universe, branch string, claims []ranke.Claim) (*rankedb.ContributionResult, error) {
	if _, err := c.Dev().AdvanceClockPast(ctx, claims); err != nil {
		return nil, err
	}
	return c.Contribute(ctx, u, branch, claims, rankedb.Creating())
}

// contributeAndReport merges the claims and prints what landed.
func contributeAndReport(ctx context.Context, c *rankedb.Client, u ranke.Universe, branch string, claims []ranke.Claim, out io.Writer) error {
	res, err := contribute(ctx, c, u, branch, claims)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, ">> merged %d claim(s) onto %q, head %s\n", len(res.Ids), branch, res.Head)
	return nil
}

// projectFromRepoURL reads the project name off the repo URL's last segment,
// without the .git — every remote form ends the same way, scp-like or not. A
// monorepo names its projects with --project instead.
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
