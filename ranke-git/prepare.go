// package: main (ranke-git) / prepare
// type:    logic
// job:     the preparational phase — find or build the repository/project entities and scan
// existing content_hash values, before the build phase mints anything new
// limits:  server-facing only; the reuse it discovers is applied by convert.go's converter
package main

import (
	"context"
	"errors"
	"fmt"

	rankedb "github.com/rankegraph/ranke-db/client"
	"github.com/rankegraph/ranke-go"
)

// prepare finds or builds the repository and project entities and scans every git
// object claim already on branch for content_hash reuse — query first, so
// the build phase mints only what's actually new (-> DESIGN.md).
func prepare(ctx context.Context, c *rankedb.Client, branch, repoURL, project string) (prep, error) {
	var p prep

	repo, err := findOne(ctx, c, branch, nodeRepository, "url", repoURL)
	if err != nil {
		return p, fmt.Errorf("prepare: repository: %w", err)
	}
	p.repository = repo

	proj, err := findOne(ctx, c, branch, nodeProject, "name", project)
	if err != nil {
		return p, fmt.Errorf("prepare: project: %w", err)
	}
	p.project = proj

	hashes, err := scanContentHashes(ctx, c, branch)
	if err != nil {
		return p, fmt.Errorf("prepare: content hashes: %w", err)
	}
	p.knownHashes = hashes
	return p, nil
}

// findOne looks up the single claim of typ on branch whose field equals
// value — find-or-build's lookup half. More than one match is a data problem this tool
// did not cause, so it refuses rather than guessing which one to reuse.
func findOne(ctx context.Context, c *rankedb.Client, branch, typ, field, value string) (*reused, error) {
	claims, err := queryClaims(ctx, c, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where: &ranke.Where{And: []ranke.Where{
			{Field: "type", Test: &ranke.Comparison{Eq: typ}},
			{Field: field, Test: &ranke.Comparison{Eq: value}},
		}},
	})
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, nil
	}
	if len(claims) > 1 {
		return nil, fmt.Errorf("%d %s claims have %s=%q, want at most one", len(claims), typ, field, value)
	}
	return &reused{id: claims[0].ID(), height: claims[0].Node().Height()}, nil
}

// queryClaims reads claims a query reaches, an archive without that branch
// yet answering empty: the first run against a fresh server finds nothing,
// which is an answer rather than a failure.
func queryClaims(ctx context.Context, c *rankedb.Client, q ranke.Query) ([]ranke.Claim, error) {
	claims, err := c.QueryClaims(ctx, q)
	if errors.Is(err, rankedb.ErrNotFound) {
		return nil, nil
	}
	return claims, err
}

// scanContentHashes reads every git-object claim already on branch, keyed by
// content_hash — content-addressed, so it stays stable across separate runs
// even though the claim ids that wrap it aren't (-> DESIGN.md).
func scanContentHashes(ctx context.Context, c *rankedb.Client, branch string) (map[string]reused, error) {
	claims, err := queryClaims(ctx, c, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{
			In: []any{nodeCommit, nodeTree, nodeBlob, nodeTag},
		}},
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]reused, len(claims))
	for _, claim := range claims {
		hash := claim.Node().GetContentHash()
		if hash == nil {
			continue
		}
		out[hash.String()] = reused{id: claim.ID(), height: claim.Node().Height()}
	}
	return out, nil
}
