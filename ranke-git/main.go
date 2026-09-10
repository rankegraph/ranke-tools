// package: main (ranke-git) / main
// type:    entrypoint
// job:     the ranke-git binary — archives git state into a running ranke-db as a client
// limits:  a client only, over the documented REST contract — ranke-go for the claims,
// ranke-db/client for the transport, nothing of the server itself (-> DESIGN.md)
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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
	macaroon      string   // macaroon credential, the one carrying caveats
	contributorID string   // this worker's contributor claim id, already on the archive
	signingKey    string   // where the contributor's ed25519 private key is: a path, or file:|env:|stdin|prompt
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
	Macaroon      string   `yaml:"macaroon"`
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
	fromFile("macaroon", &o.macaroon, c.Macaroon)
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
	f.StringVar(&o.macaroon, "macaroon", "", "base64 macaroon credential, the one carrying caveats")
	f.StringVar(&o.contributorID, "contributor-id", "", "this worker's contributor claim id (default: the one carrying --signing-key's public key)")
	f.StringVar(&o.signingKey, "signing-key", "", "where the contributor's ed25519 private key is, PEM: a path, or file:path|env:VAR|stdin|prompt (required)")
	f.StringVar(&o.repoURL, "repo", "", "the repo's remote URL — names the repo entity (default: the clone's origin)")
	f.StringVar(&o.clone, "clone", "", "an existing local clone to read, instead of cloning --repo")
	f.StringVar(&o.project, "project", "", "the project name — its own entity, distinct from the repo (default: the repo URL's last segment)")
	f.StringVar(&o.branch, "branch", "main", "the ranke-db branch this run contributes onto")
	f.StringSliceVar(&o.paths, "path", nil, "restrict to this path within the repo (repeatable; monorepo subset)")
	root.AddCommand(snapshotCmd(&o), backupCmd(&o), attachCmd(&o), scanCmd(&o), demoCmd(&o))
	return root
}
