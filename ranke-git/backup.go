// package: main (ranke-git) / backup
// type:    entrypoint
// job:     `ranke-git backup` — every commit reachable from the given refs
// limits:  builds the command; the walk it drives is convert.go's (-> DESIGN.md)
package main

import (
	"context"
	"crypto"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// backupCmd archives every commit reachable from the given branches and tags, each
// chained to its parent — a reconstructible clone, immune to a later force-push or
// squash.
func backupCmd(o *options) *cobra.Command {
	var branches, tags []string
	c := &cobra.Command{
		Use:   "backup",
		Short: "Archive every reachable commit, chained to its parents — a reconstructible clone",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(branches) == 0 && len(tags) == 0 {
				return fmt.Errorf("backup: at least one --git-branch or --git-tag is required")
			}
			g, err := localRepo(o)
			if err != nil {
				return err
			}
			var refs []refSpec
			for _, b := range branches {
				refs = append(refs, refSpec{kind: "branch", name: b})
			}
			for _, t := range tags {
				refs = append(refs, refSpec{kind: "tag", name: t})
			}
			for _, r := range refs {
				if _, err := resolveRef(g, r.fullRef()); err != nil {
					return err
				}
			}
			return run(cmd, o, func(ctx context.Context, contributor ranke.Contributor, signer crypto.Signer, p prep, u ranke.Universe) ([]ranke.Claim, error) {
				return backupToClaims(ctx, g, refs, u, contributor, signer, o.repoURL, o.project, p, time.Time{})
			})
		},
	}
	// Named --git-branch/--git-tag, not --branch: the archive branch every action
	// contributes onto (the persistent flag) is a different thing entirely.
	c.Flags().StringSliceVar(&branches, "git-branch", nil, "a git branch to archive, with its full reachable history (repeatable)")
	c.Flags().StringSliceVar(&tags, "git-tag", nil, "a git tag to archive (repeatable)")
	return c
}
