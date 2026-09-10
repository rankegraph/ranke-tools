// package: main / ranke-git
// type:    logic
// job:     the preparational phase — find or build the repository/project entities and scan
// existing content_hash values, before the build phase mints anything new
// limits:  server-facing only; the reuse it discovers is applied by convert.go's converter
package main

import (
	"context"
	"fmt"

	"github.com/rankegraph/ranke-go"
)

// prepare finds or builds the repository and project entities and scans every git
// object claim already on branch for content_hash reuse — query first, so
// the build phase mints only what's actually new (-> DESIGN.md).
func prepare(ctx context.Context, c *client, branch, repoURL, project string) (prep, error) {
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
func findOne(ctx context.Context, c *client, branch, typ, field, value string) (*reused, error) {
	recs, err := c.query(ctx, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where: &ranke.Where{And: []ranke.Where{
			{Field: "type", Test: &ranke.Comparison{Eq: typ}},
			{Field: field, Test: &ranke.Comparison{Eq: value}},
		}},
		Output: ranke.Output{Detail: ranke.DetailClaims, Encoding: ranke.ResultJSON},
	})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	if len(recs) > 1 {
		return nil, fmt.Errorf("%d %s claims have %s=%q, want at most one", len(recs), typ, field, value)
	}
	id, err := ranke.ParseId(recs[0].ID)
	if err != nil {
		return nil, fmt.Errorf("parse id %q: %w", recs[0].ID, err)
	}
	return &reused{id: id, height: recs[0].Height}, nil
}

// scanContentHashes reads every git-object claim already on branch, keyed by
// content_hash — content-addressed, so it stays stable across separate runs
// even though the claim ids that wrap it aren't (-> DESIGN.md).
func scanContentHashes(ctx context.Context, c *client, branch string) (map[string]reused, error) {
	recs, err := c.query(ctx, ranke.Query{
		Select: ranke.Select{Branch: branch},
		Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{
			In: []any{nodeCommit, nodeTree, nodeBlob, nodeTag},
		}},
		Output: ranke.Output{Detail: ranke.DetailClaims, Encoding: ranke.ResultJSON},
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]reused, len(recs))
	for _, rec := range recs {
		if rec.ContentHash == "" {
			continue
		}
		id, err := ranke.ParseId(rec.ID)
		if err != nil {
			return nil, fmt.Errorf("parse id %q: %w", rec.ID, err)
		}
		out[rec.ContentHash] = reused{id: id, height: rec.Height}
	}
	return out, nil
}
