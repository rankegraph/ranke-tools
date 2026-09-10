// package: main (ranke-git) / snapshot
// type:    entrypoint
// job:     `ranke-git snapshot` — one commit's tree, byte-exact, no history
// limits:  builds the command; the walk it drives is convert.go's (-> DESIGN.md)
package main

import (
	"context"
	"crypto"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// snapshotCmd archives one commit's tree: the exact source an artifact was built from.
// No `.git` comes back — no history, no refs, just that one commit's files, byte-exact.
func snapshotCmd(o *options) *cobra.Command {
	var ref, branch, tag string
	c := &cobra.Command{
		Use:   "snapshot",
		Short: "Archive one commit's tree, byte-exact — no history, no refs",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := refTarget(ref, branch, tag)
			if err != nil {
				return err
			}
			g, err := localRepo(o)
			if err != nil {
				return err
			}
			// Resolved here, so a ref that names nothing fails before the
			// first network call rather than after the preparational phase.
			sha, err := resolveRef(g, target)
			if err != nil {
				return err
			}
			return run(cmd, o, func(ctx context.Context, contributor ranke.Contributor, signer crypto.Signer, p prep, u ranke.Universe) ([]ranke.Claim, error) {
				return gitToClaims(ctx, g, sha, o.paths, u, contributor, signer, o.repoURL, o.project, p, time.Time{})
			})
		},
	}
	c.Flags().StringVar(&ref, "ref", "", "the tag, branch, or commit to archive, resolved git's own way")
	c.Flags().StringVar(&branch, "git-branch", "", "the git branch whose tip to archive — backup's spelling, for one commit")
	c.Flags().StringVar(&tag, "git-tag", "", "the git tag whose commit to archive — backup's spelling, for one commit")
	return c
}
