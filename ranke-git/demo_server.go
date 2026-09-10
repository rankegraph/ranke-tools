// package: main / ranke-git
// type:    entrypoint
// job:     `ranke-git demo server` — the same story as `demo local`, but genuinely over the
// network: find or build entities, reuse, attach, against a real ranke-db
// limits:  never starts a server itself — checks for one and says how to start it
// if there isn't one (-> DESIGN.md)
package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// demoServerBranch, demoServerRepoURL and demoServerProject are fixed, so
// running this command twice shows entity and content_hash reuse.
const (
	demoServerBranch   = "ranke_git_demo_server" // branch names are [a-z0-9_]
	demoServerRepoURL  = "https://example.com/ranke-git-demo-server.git"
	demoServerProject  = "ranke-git-demo-server"
	demoServerDefault  = "localhost:8080"
	demoServerLogTitle = "demo build log"
	demoServerTag      = "v1.0.0" // annotated: gets its own source/git_tag claim
	demoServerTagLW    = "v1.2.0" // lightweight: just a ref pointing at the commit
)

// demoServerCVEs are fixed too, so entity/cve also reuses on a second run.
var demoServerCVEs = []string{
	"CVE-2024-30111=https://nvd.nist.gov/vuln/detail/CVE-2024-30111",
	"CVE-2024-30222",
}

func demoServerCmd(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Run the demo against a real ranke-db — archive, find or build entities, attach, scan, all live (start one first: server/run.sh)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDemoServer(cmd, o)
		},
	}
}

// demoServerTimeline spreads the story hours apart from real now, forward
// only — the dev sequencer's clock never moves backward.
type demoServerTimeline struct {
	registerAt, commit1At, commit2At, archiveAt, buildAt, scanAt time.Time
}

func newDemoServerTimeline() demoServerTimeline {
	t0 := time.Now().UTC()
	return demoServerTimeline{
		registerAt: t0,
		commit1At:  t0,
		commit2At:  t0.Add(2 * time.Hour),
		archiveAt:  t0.Add(3 * time.Hour),
		buildAt:    t0.Add(4 * time.Hour),
		scanAt:     t0.Add(5 * time.Hour),
	}
}

// runDemoServer checks for a server, refusing to start one itself, then
// backs up a small tagged repo, attaches a build log, and records a scan —
// DESIGN.md's driving use cases, run for real. Two identities sign it: a CI
// pipeline attests the archive and log, a scanner attests the scan and its
// CVEs — a signature is who attested a claim, so one actor per real role.
func runDemoServer(cmd *cobra.Command, o *options) error {
	ctx := cmd.Context()
	addr := o.server
	if addr == "" {
		addr = demoServerDefault
	}
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, ">> looking for a ranke-db at %s ...\n", addr)
	c := newClient(addr, o.token, o.apiKey)
	if err := c.waitReady(ctx, 2*time.Second); err != nil {
		return fmt.Errorf("no ranke-db found at %s — start one first, from another shell:\n\n  server/run.sh\n\nthen run this again", addr)
	}
	fmt.Fprintln(out, ">> found it")

	timeline := newDemoServerTimeline()
	fmt.Fprintln(out, ">> minting two throwaway contributors — a CI pipeline and a security scanner — and registering them")
	ci, ciSigner, err := bootstrapContributor(ctx, c, demoServerBranch, timeline.registerAt)
	if err != nil {
		return err
	}
	scanner, scannerSigner, err := bootstrapContributor(ctx, c, demoServerBranch, timeline.registerAt)
	if err != nil {
		return err
	}

	repo, err := demoServerRepo(timeline)
	if err != nil {
		return err
	}
	g := gitRepo{dir: repo.dir}
	fmt.Fprintf(out, ">> built a demo repo: %s\n", repo.dir)
	fmt.Fprintf(out, "   commit 1  %s  (%s)\n", repo.firstSha, timeline.commit1At.Format(time.RFC3339))
	fmt.Fprintf(out, "   commit 2  %s  (%s) tagged %s (annotated), %s (lightweight)\n",
		repo.secondSha, timeline.commit2At.Format(time.RFC3339), demoServerTag, demoServerTagLW)

	fmt.Fprintf(out, ">> preparing — find or build %s/%s on %q\n", demoServerRepoURL, demoServerProject, demoServerBranch)
	p, err := prepare(ctx, c, demoServerBranch, demoServerRepoURL, demoServerProject)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, ">> %d known object(s) to reuse\n", len(p.knownHashes))

	u := ranke.NewMemoryUniverse()
	refs := []refSpec{
		{kind: "branch", name: "main"},
		{kind: "tag", name: demoServerTag},
		{kind: "tag", name: demoServerTagLW},
	}
	claims, err := backupToClaims(ctx, g, refs, u, ci, ciSigner, demoServerRepoURL, demoServerProject, p, timeline.archiveAt)
	if err != nil {
		return err
	}
	if len(claims) == 0 {
		fmt.Fprintln(out, ">> nothing new to back up — already archived")
	} else if err := contributeAndReport(ctx, c, u, demoServerBranch, claims, out); err != nil {
		return err
	}

	fmt.Fprintln(out, ">> attaching a build log to the tagged commit, as the CI pipeline")
	target, err := findOne(ctx, c, demoServerBranch, nodeCommit, gitShaField, repo.secondSha)
	if err != nil {
		return fmt.Errorf("demo server: %w", err)
	}
	if target == nil {
		return fmt.Errorf("demo server: commit %s not found right after archiving it", repo.secondSha)
	}
	attachU := ranke.NewMemoryUniverse()
	logAttachment := attachment{
		typ:         "source/" + gitPrefix + "build_log",
		name:        demoServerLogTitle,
		contentType: "text/plain",
		content:     []byte("demo-server build log\ncompiling...\nbuild succeeded\n"),
	}
	logClaim, err := buildAttachment(ctx, attachU, ci, ciSigner, *target, logAttachment, timeline.buildAt)
	if err != nil {
		return err
	}
	if err := contributeAndReport(ctx, c, attachU, demoServerBranch, []ranke.Claim{logClaim}, out); err != nil {
		return err
	}

	fmt.Fprintln(out, ">> recording a vulnerability scan against the tagged commit, as the scanner")
	found, scanContent, err := demoServerScan(ctx, c, scanner, scannerSigner, target, timeline.scanAt)
	if err != nil {
		return err
	}
	scanU := ranke.NewMemoryUniverse()
	scan, err := buildScan(ctx, scanU, scanner, scannerSigner, scanContent, found.height, found.edges, timeline.scanAt)
	if err != nil {
		return err
	}
	found.claims = append(found.claims, scan)
	if err := contributeAndReport(ctx, c, scanU, demoServerBranch, found.claims, out); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n>> done — look around:\n")
	fmt.Fprintf(out, "   demo repo:  %s\n", repo.dir)
	fmt.Fprintf(out, "   query:      curl -s -X POST http://%s/query -H 'Content-Type: application/json' \\\n", addr)
	fmt.Fprintf(out, "                 -d '{\"select\":{\"branch\":%q},\"output\":{\"detail\":\"claims\",\"encoding\":\"json\"}}'\n", demoServerBranch)
	fmt.Fprintln(out, "   run this again — the repo/project/cve entities and any unchanged files/trees should come back reused, only the new commits, tag, and tree freshly minted")
	return nil
}

// demoServerScanResult is what demoServerScan found: fresh entity/cve claims,
// buildScan's edges, and its required height (V-HEIGHT: max of target's and
// every cve's — a fresh mint can exceed target's own).
type demoServerScanResult struct {
	claims []ranke.Claim
	edges  []ranke.Edge
	height uint64
}

// demoServerScan finds or builds each demoServerCVE finding — scan.go's own loop,
// split out here so runDemoServer stays about orchestration.
func demoServerScan(
	ctx context.Context, c *client, contributor ranke.Contributor, signer crypto.Signer, target *reused, at time.Time,
) (demoServerScanResult, []byte, error) {
	var out demoServerScanResult
	out.height = target.height
	input, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.id, Type: edgeInput})
	if err != nil {
		return out, nil, fmt.Errorf("demo server: scan input edge: %w", err)
	}
	out.edges = append(out.edges, input)
	for _, raw := range demoServerCVEs {
		f := parseCVE(raw)
		cve, mint, err := findOrBuildCVE(ctx, c, demoServerBranch, contributor, signer, target, f, at)
		if err != nil {
			return out, nil, err
		}
		if mint != nil {
			out.claims = append(out.claims, mint)
		}
		edge, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: cve.id, Type: edgeCVE, RelationDirection: ranke.RelationTo,
		})
		if err != nil {
			return out, nil, fmt.Errorf("demo server: cve edge for %s: %w", f.id, err)
		}
		out.edges = append(out.edges, edge)
		if cve.height > out.height {
			out.height = cve.height
		}
	}
	content := []byte("demo scanner output\nno critical findings\n")
	return out, content, nil
}

// demoServerRepoResult is what demoServerRepo built — enough for the archive
// and attach/scan steps that follow.
type demoServerRepoResult struct {
	dir                 string
	firstSha, secondSha string
}

// demoServerRepo builds a tiny mock project across two commits — a change
// and an addition, tagged both ways (annotated and lightweight), each
// commit's own date set from timeline, not real now.
func demoServerRepo(timeline demoServerTimeline) (demoServerRepoResult, error) {
	var result demoServerRepoResult
	dir, err := os.MkdirTemp("", "ranke-git-demo-server-")
	if err != nil {
		return result, err
	}
	result.dir = dir
	g := gitRepo{dir: dir}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Demo"},
		{"config", "user.email", "demo@example.com"},
	} {
		if _, err := g.run(args...); err != nil {
			return result, err
		}
	}

	if err := demoCommit(g, []demoFile{
		{"README.md", "# ranke-git demo\n\nA tiny mock project, backed up by ranke-git.\n", 0o644},
		{"LICENSE.md", "MIT\n", 0o644},
		{"go.mod", "module demo\n\ngo 1.23\n", 0o644},
		{"main.go", "package main\n\nfunc main() {}\n", 0o644},
		{"pkg/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b }\n", 0o644},
		{"internal/config/config.go", "package config\n\ntype Config struct{ Name string }\n", 0o644},
		{"scripts/run.sh", "#!/bin/sh\necho running demo\n", 0o755},
	}, "initial commit", timeline.commit1At); err != nil {
		return result, err
	}
	firstSha, err := g.revParse("HEAD")
	if err != nil {
		return result, err
	}
	result.firstSha = firstSha

	if err := demoCommit(g, []demoFile{
		{"README.md", "# ranke-git demo\n\nA tiny mock project, backed up by ranke-git.\nNow with a Mul helper too.\n", 0o644},
		{"pkg/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a * b }\n", 0o644},
		{"pkg/util/util_test.go", "package util\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n", 0o644},
		{"CHANGELOG.md", "## " + demoServerTag + "\n- add Mul\n", 0o644},
		{"docs/USAGE.md", "# Usage\n\nrun scripts/run.sh\n", 0o644},
	}, "second commit — add Mul, tests, docs", timeline.commit2At); err != nil {
		return result, err
	}
	secondSha, err := g.revParse("HEAD")
	if err != nil {
		return result, err
	}
	result.secondSha = secondSha

	if _, err := g.run("tag", "-a", demoServerTag, "-m", "release "+demoServerTag); err != nil {
		return result, err
	}
	if _, err := g.run("tag", demoServerTagLW); err != nil {
		return result, err
	}
	return result, nil
}

// bootstrapContributor mints an identity and contributes its root claim
// before binding it — demoIdentity (demo.go) never keeps that unbound claim.
// A zero at defaults to now.
func bootstrapContributor(ctx context.Context, c *client, branch string, at time.Time) (ranke.Contributor, crypto.Signer, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := ranke.EncodePublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	claim, err := ranke.NewClaim(ranke.NodeTypeContributor, nil).
		WithInlineContent(encoded).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return nil, nil, err
	}
	if err := c.advanceClock(ctx, at); err != nil {
		return nil, nil, err
	}
	if _, err := c.contribute(ctx, ranke.NewMemoryUniverse(), branch, []ranke.Claim{claim}); err != nil {
		return nil, nil, fmt.Errorf("register contributor: %w", err)
	}
	self, err := claim.AsContributor(ctx, nil, priv)
	if err != nil {
		return nil, nil, err
	}
	return self, priv, nil
}
