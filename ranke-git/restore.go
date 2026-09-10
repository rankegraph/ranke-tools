// package: main (ranke-git) / restore
// type:    logic
// job:     replays archived claims back into a git repository — objects by their raw
// bytes, refs from their points_at edge, origin from the repository entity
// limits:  the other direction is convert.go's; this reads a claim set and a Universe,
// and talks to no server (-> DESIGN.md)
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/rankegraph/ranke-go"
)

// claimsToGit restores claims into dest: objects replayed, refs recreated
// from their points_at edge, origin from the repository entity's url
// (-> DESIGN.md). refs distinguishes branches from tags in a backup.
func claimsToGit(ctx context.Context, dest gitRepo, u ranke.Universe, claims []ranke.Claim) (commitSha string, refs map[string]string, err error) {
	byID := make(map[string]ranke.Claim, len(claims))
	for _, claim := range claims {
		byID[claim.ID().String()] = claim
	}

	type pendingRef struct{ kind, name, targetID string }
	var pending []pendingRef
	var repoURL string

	for _, claim := range claims {
		typ := claim.Node().Type()
		switch typ {
		case nodeRepository:
			if url, err := claim.Node().GetField("url"); err == nil {
				repoURL = url
			}
			continue
		case nodeProject:
			continue
		case nodeVersion:
			continue
		case nodeRef:
			name, _ := claim.Node().GetField("name")
			kind, _ := claim.Node().GetField("kind")
			points := claim.Edges(ranke.EdgeFilterType{Type: edgePointsAt})
			if len(points) != 1 {
				return "", nil, fmt.Errorf("restore: ref %q has %d points_at edge(s), want 1", name, len(points))
			}
			pending = append(pending, pendingRef{kind: kind, name: name, targetID: points[0].Reference().String()})
			continue
		}

		var kind string
		switch typ {
		case nodeBlob:
			kind = "blob"
		case nodeTree:
			kind = "tree"
		case nodeCommit:
			kind = "commit"
		case nodeTag:
			kind = "tag"
		default:
			return "", nil, fmt.Errorf("restore: claim %s has unexpected type %q", claim.ID(), typ)
		}
		r, err := claim.GetContent(ctx, u)
		if err != nil {
			return "", nil, fmt.Errorf("restore: claim %s: content: %w", claim.ID(), err)
		}
		payload, err := io.ReadAll(r)
		if err != nil {
			return "", nil, fmt.Errorf("restore: claim %s: read content: %w", claim.ID(), err)
		}
		sha, err := dest.hashObjectWrite(kind, payload)
		if err != nil {
			return "", nil, fmt.Errorf("restore: claim %s: write %s: %w", claim.ID(), kind, err)
		}
		if kind == "commit" {
			commitSha = sha
		}
	}
	if commitSha == "" && len(pending) == 0 {
		return "", nil, fmt.Errorf("restore: no commit claim in the set")
	}

	refs = make(map[string]string, len(pending))
	for _, p := range pending {
		target, ok := byID[p.targetID]
		if !ok {
			return "", nil, fmt.Errorf("restore: ref %q points at a claim not in the set", p.name)
		}
		sha, err := target.Node().GetField(gitShaField)
		if err != nil {
			return "", nil, fmt.Errorf("restore: ref %q: target has no %s: %w", p.name, gitShaField, err)
		}
		full := "refs/heads/" + p.name
		if p.kind == "tag" {
			full = "refs/tags/" + p.name
		}
		if _, err := dest.run("update-ref", full, sha); err != nil {
			return "", nil, fmt.Errorf("restore: create ref %s: %w", full, err)
		}
		refs[p.name] = sha
	}

	if repoURL != "" {
		if _, err := dest.run("remote", "add", "origin", repoURL); err != nil {
			return "", nil, fmt.Errorf("restore: configure origin: %w", err)
		}
	}
	return commitSha, refs, nil
}
