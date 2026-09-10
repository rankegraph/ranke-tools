// package: main / ranke-git
// type:    entrypoint
// job:     the ranke-git binary — archives git state into a running ranke-db as a client
// limits:  a client only, over the documented REST contract; no dependency on ranke-db
// itself, only on ranke-go (-> DESIGN.md)
package main

import (
	"context"
	"crypto"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/rankegraph/ranke-go"
)

// version is stamped at release build time (-ldflags "-X main.version=vX.Y.Z",
// .github/workflows/release.yml) — every claim ranke-git mints records it
// (versionField, convert.go), so a later breaking change still has an old
// tool version on hand to restore an archive with.
var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ranke-git:", err)
		os.Exit(1)
	}
}

// options are the flags every action shares: where to write, as whom, and what repo.
type options struct {
	configPath    string   // --config; a YAML alternative to typing every flag by hand
	server        string   // the ranke-db REST base URL
	token         string   // Authorization: Bearer credential
	apiKey        string   // X-API-Key credential
	contributorID string   // this worker's contributor claim id, already on the archive
	signingKey    string   // path to the contributor's ed25519 private key (PEM)
	repoURL       string   // the repo's remote URL, naming the entity; the clone's origin when unset
	clone         string   // an existing local clone to read instead of cloning repoURL
	project       string   // the project name — its own entity, distinct from the repo; derived from repoURL when unset
	branch        string   // the ranke-db branch this run contributes onto
	paths         []string // optional monorepo subset; empty archives the whole tree
}

// fileConfig is --config's YAML shape — one field per flag this run cares
// about, so a config file is a persistent stand-in for typing them by hand.
type fileConfig struct {
	Server        string   `yaml:"server"`
	Token         string   `yaml:"token"`
	APIKey        string   `yaml:"api_key"`
	ContributorID string   `yaml:"contributor_id"`
	SigningKey    string   `yaml:"signing_key"`
	Repo          string   `yaml:"repo"`
	Clone         string   `yaml:"clone"`
	Project       string   `yaml:"project"`
	Branch        string   `yaml:"branch"`
	Paths         []string `yaml:"paths"`
}

// loadConfig fills whatever o.configPath's file sets and the command line
// didn't — a flag given on the command line always wins, checked through
// cmd.Flags().Changed rather than a zero-value guess, since "main" (branch's
// own default) is a legitimate value either side could have meant.
func (o *options) loadConfig(cmd *cobra.Command) error {
	if o.configPath == "" {
		return nil
	}
	data, err := os.ReadFile(o.configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var c fileConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("config: %s: %w", o.configPath, err)
	}
	fromFile := func(flag string, dst *string, val string) {
		if val != "" && !cmd.Flags().Changed(flag) {
			*dst = val
		}
	}
	fromFile("server", &o.server, c.Server)
	fromFile("token", &o.token, c.Token)
	fromFile("api-key", &o.apiKey, c.APIKey)
	fromFile("contributor-id", &o.contributorID, c.ContributorID)
	fromFile("signing-key", &o.signingKey, c.SigningKey)
	fromFile("repo", &o.repoURL, c.Repo)
	fromFile("clone", &o.clone, c.Clone)
	fromFile("project", &o.project, c.Project)
	fromFile("branch", &o.branch, c.Branch)
	if len(c.Paths) > 0 && !cmd.Flags().Changed("path") {
		o.paths = c.Paths
	}
	return nil
}

// rootCmd builds the ranke-git command tree: one subcommand per action.
func rootCmd() *cobra.Command {
	var o options
	root := &cobra.Command{
		Use:           "ranke-git",
		Short:         "Archive git state into a running ranke-db, byte-exact and content-deduplicated",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return o.loadConfig(cmd)
		},
	}
	f := root.PersistentFlags()
	f.StringVar(&o.configPath, "config", "", "path to a YAML config file — an alternative to the flags below; a flag given on the command line still wins (see config.example.yaml)")
	f.StringVar(&o.server, "server", "", "the ranke-db REST base URL (required)")
	f.StringVar(&o.token, "token", "", "Authorization: Bearer credential")
	f.StringVar(&o.apiKey, "api-key", "", "X-API-Key credential")
	f.StringVar(&o.contributorID, "contributor-id", "", "this worker's contributor claim id (default: the one carrying --signing-key's public key)")
	f.StringVar(&o.signingKey, "signing-key", "", "path to the contributor's ed25519 private key, PEM (required)")
	f.StringVar(&o.repoURL, "repo", "", "the repo's remote URL — names the repo entity (default: the clone's origin)")
	f.StringVar(&o.clone, "clone", "", "an existing local clone to read, instead of cloning --repo")
	f.StringVar(&o.project, "project", "", "the project name — its own entity, distinct from the repo (default: the repo URL's last segment)")
	f.StringVar(&o.branch, "branch", "main", "the ranke-db branch this run contributes onto")
	f.StringSliceVar(&o.paths, "path", nil, "restrict to this path within the repo (repeatable; monorepo subset)")
	root.AddCommand(snapshotCmd(&o), backupCmd(&o), attachCmd(&o), scanCmd(&o), identityCmd(&o), demoCmd(&o))
	return root
}

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

// refTarget names the single commit a command works on. --git-branch and
// --git-tag are backup's flags, spelled the same wherever one commit is
// named; each resolves under its own refs/ namespace, which --ref leaves to
// git's resolution order.
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
