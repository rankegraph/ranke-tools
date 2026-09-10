// package: main (ranke-git) / scan
// type:    logic + entrypoint
// job:     `ranke-git scan` — records a vulnerability scan's findings against an already-
// archived commit: find or build entity/cve per finding, one derivation/vulnerability_scan
// claim citing the commit (derivation/input) and each finding (relation/cve)
// limits:  never parses scanner output itself — the caller names which CVEs were found
// (-> DESIGN.md, same principle as attach)
package main

import (
	"context"
	"crypto"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	rankedb "github.com/rankegraph/ranke-db/client"
	"github.com/rankegraph/ranke-go"
)

const (
	nodeCVE    = "entity/cve"
	nodeScan   = "derivation/vulnerability_scan"
	edgeCVE    = "relation/cve"
	cveIDField = "cve_id"
	cveURL     = "url"
)

func scanCmd(o *options) *cobra.Command {
	var commitSha, file string
	var cves []string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Record a vulnerability scan's findings against an already-archived commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			if commitSha == "" {
				return fmt.Errorf("scan: --commit is required")
			}
			if len(cves) == 0 {
				return fmt.Errorf("scan: at least one --cve is required")
			}
			content, err := readScanOutput(file)
			if err != nil {
				return err
			}
			return runScan(cmd, o, commitSha, cves, content)
		},
	}
	c.Flags().StringVar(&commitSha, "commit", "", "the git sha of an already-archived commit (required)")
	c.Flags().StringSliceVar(&cves, "cve", nil, "a finding, as ID or ID=URL, e.g. CVE-2024-1234 or CVE-2024-1234=https://... (repeatable, at least one required)")
	c.Flags().StringVar(&file, "file", "", "the scanner's raw output, archived alongside the findings (optional — a bare link needs none)")
	return c
}

// readScanOutput reads --file if given, nil otherwise — unlike attach, a
// scan with no archived output is a legitimate, contentless claim.
func readScanOutput(file string) ([]byte, error) {
	if file == "" {
		return nil, nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return content, nil
}

// cveFinding is one --cve value, split on its optional "=URL" suffix.
type cveFinding struct{ id, url string }

func parseCVE(raw string) cveFinding {
	id, url, _ := strings.Cut(raw, "=")
	return cveFinding{id: id, url: url}
}

// runScan finds the target commit, finds or builds each finding's entity/cve, then
// contributes one derivation/vulnerability_scan claim citing all of it.
func runScan(cmd *cobra.Command, o *options, commitSha string, cves []string, content []byte) error {
	ctx := cmd.Context()
	s, err := connect(ctx, o)
	if err != nil {
		return err
	}
	target, err := findOne(ctx, s.client, o.branch, nodeCommit, gitShaField, commitSha)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if target == nil {
		return fmt.Errorf("scan: no archived commit with git_sha %q on branch %q", commitSha, o.branch)
	}

	input, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.id, Type: edgeInput})
	if err != nil {
		return fmt.Errorf("scan: input edge: %w", err)
	}
	edges := []ranke.Edge{input}
	height := target.height

	u := ranke.NewMemoryUniverse()
	var claims []ranke.Claim
	for _, raw := range cves {
		f := parseCVE(raw)
		cve, mint, err := findOrBuildCVE(ctx, s.client, o.branch, s.contributor, s.signer, target, f, time.Time{})
		if err != nil {
			return err
		}
		if mint != nil {
			claims = append(claims, mint)
		}
		edge, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: cve.id, Type: edgeCVE, RelationDirection: ranke.RelationTo,
		})
		if err != nil {
			return fmt.Errorf("scan: cve edge for %s: %w", f.id, err)
		}
		edges = append(edges, edge)
		if cve.height > height {
			height = cve.height
		}
	}

	scan, err := buildScan(ctx, u, s.contributor, s.signer, content, height, edges, time.Time{})
	if err != nil {
		return err
	}
	claims = append(claims, scan)
	return contributeAndReport(ctx, s.client, u, o.branch, claims, cmd.OutOrStdout())
}

// findOrBuildCVE finds or builds one entity/cve: reuse the query hit if there is one,
// otherwise mint it fresh, D1-anchored to target — the same shape
// repository()/project() use (convert.go), invoked here instead of the walk.
// A zero at defaults to now.
func findOrBuildCVE(
	ctx context.Context, c *rankedb.Client, branch string, contributor ranke.Contributor, signer crypto.Signer,
	target *reused, f cveFinding, at time.Time,
) (reused, ranke.Claim, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	found, err := findOne(ctx, c, branch, nodeCVE, cveIDField, f.id)
	if err != nil {
		return reused{}, nil, fmt.Errorf("scan: cve %s: %w", f.id, err)
	}
	if found != nil {
		return *found, nil, nil
	}
	input, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.id, Type: edgeInput})
	if err != nil {
		return reused{}, nil, fmt.Errorf("scan: cve %s: input edge: %w", f.id, err)
	}
	b := ranke.NewClaim(nodeCVE, contributor).
		WithInlineContent([]byte(f.id)).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(at).
		WithHeight(target.height+1).
		WithField(cveIDField, f.id).
		WithEdges(input)
	if f.url != "" {
		b = b.WithField(cveURL, f.url)
	}
	claim, err := b.Sign(signer)
	if err != nil {
		return reused{}, nil, fmt.Errorf("scan: cve %s: %w", f.id, err)
	}
	return reused{id: claim.ID(), height: target.height + 1}, claim, nil
}

// buildScan signs the derivation/vulnerability_scan claim: external content
// when the caller gave scanner output, none at all otherwise — V-CONTENT
// allows a claim with no content, and this one stays structural without it.
// A zero at defaults to now.
func buildScan(
	ctx context.Context, u ranke.Universe, contributor ranke.Contributor, signer crypto.Signer,
	content []byte, targetHeight uint64, edges []ranke.Edge, at time.Time,
) (ranke.Claim, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	b := ranke.NewClaim(nodeScan, contributor).
		WithCreatedAt(at).
		WithHeight(targetHeight + 1).
		WithEdges(edges...)
	if len(content) > 0 {
		id, err := ranke.HashContent(content)
		if err != nil {
			return nil, fmt.Errorf("scan: hash content: %w", err)
		}
		if err := u.PutContents(ctx, []ranke.ContentBlob{{Hash: id, Content: content}}); err != nil {
			return nil, fmt.Errorf("scan: store content: %w", err)
		}
		b = b.WithExternalContent(id, uint64(len(content))).WithEncoding(ranke.EncodingOctetStream)
	}
	claim, err := b.Sign(signer)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return claim, nil
}
