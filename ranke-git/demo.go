// package: main (ranke-git) / demo
// type:    entrypoint
// job:     `ranke-git demo local` — a small multi-branch, tagged repo, backed up and
// restored, both kept on disk — illustrative, not one of the tool's real actions
// limits:  no ranke-db (-> demo_server.go's `demo server`, convert_test.go for real coverage)
package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// demoCmd is the shared parent for demo's two shapes: local (no server) and
// server (against a live ranke-db, demo_server.go).
func demoCmd(o *options) *cobra.Command {
	parent := &cobra.Command{
		Use:   "demo",
		Short: "Run a demo — local (no server) or server (against a live ranke-db)",
	}
	parent.AddCommand(demoLocalCmd(), demoServerCmd(o))
	return parent
}

func demoLocalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "local",
		Short: "Back up a small multi-branch, tagged repo, and restore it — keeps both so you can look",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDemo(cmd.OutOrStdout())
		},
	}
}

// runDemo mirrors what the backup tests prove — several commits, a second
// branch, an annotated and a lightweight tag — but writes to fixed, printed,
// never-cleaned-up paths. The tests are the real coverage; this is for a
// human to look at.
func runDemo(out io.Writer) error {
	base, err := os.MkdirTemp("", "ranke-git-demo-")
	if err != nil {
		return err
	}
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")

	g, refs, err := demoBuildRepo(src)
	if err != nil {
		return err
	}

	contributor, signer, err := demoContributor()
	if err != nil {
		return err
	}
	u := ranke.NewMemoryUniverse()
	ctx := context.Background()
	claims, err := backupToClaims(ctx, g, refs, u, contributor, signer,
		"https://example.com/demo/ranke-git.git", "ranke-git-demo", prep{}, time.Time{})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	dstRepo := gitRepo{dir: dst}
	if _, err := dstRepo.run("init", "-q"); err != nil {
		return err
	}
	_, restoredRefs, err := claimsToGit(ctx, dstRepo, u, claims)
	if err != nil {
		return err
	}
	if _, err := dstRepo.run("checkout", "-q", "main"); err != nil {
		return err
	}

	fmt.Fprintf(out, "%d claims built\n\n", len(claims))
	fmt.Fprintf(out, "source repo:   %s\n", src)
	fmt.Fprintf(out, "restored repo: %s\n\n", dst)
	fmt.Fprintln(out, "refs restored:")
	for _, r := range refs {
		orig, err := g.revParse(r.fullRef())
		if err != nil {
			return err
		}
		match := "MISMATCH"
		if restoredRefs[r.name] == orig {
			match = "matches"
		}
		fmt.Fprintf(out, "  %-7s %-10s %s (%s)\n", r.kind, r.name, restoredRefs[r.name], match)
	}
	fmt.Fprintf(out, "\nlook around:\n  cd %s && git log --oneline --graph --all\n  cd %s && git log --oneline --graph --all && git remote -v\n  diff -rq %s %s -x .git\n", src, dst, src, dst)
	return nil
}

// demoBuildRepo creates a small repo with real history to back up: two
// commits on main, a branch diverging from the first, an annotated tag and a
// lightweight one on main's tip — everything backup mode has to carry that
// snapshot mode does not (-> DESIGN.md).
func demoBuildRepo(dir string) (gitRepo, []refSpec, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return gitRepo{}, nil, err
	}
	g := gitRepo{dir: dir}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Demo"},
		{"config", "user.email", "demo@example.com"},
	} {
		if _, err := g.run(args...); err != nil {
			return gitRepo{}, nil, err
		}
	}

	if err := demoCommit(g, []demoFile{
		{"README.md", "ranke-git demo\n", 0o644},
		{"bin/run.sh", "#!/bin/sh\necho hi\n", 0o755},
	}, "first commit", time.Time{}); err != nil {
		return gitRepo{}, nil, err
	}
	if err := demoCommit(g, []demoFile{
		{"pkg/sub/a.go", "package sub\n", 0o644},
	}, "second commit", time.Time{}); err != nil {
		return gitRepo{}, nil, err
	}

	if _, err := g.run("branch", "feature", "main~1"); err != nil {
		return gitRepo{}, nil, err
	}
	if _, err := g.run("checkout", "-q", "feature"); err != nil {
		return gitRepo{}, nil, err
	}
	if err := demoCommit(g, []demoFile{
		{"feature.txt", "work in progress\n", 0o644},
	}, "feature commit", time.Time{}); err != nil {
		return gitRepo{}, nil, err
	}
	if _, err := g.run("checkout", "-q", "main"); err != nil {
		return gitRepo{}, nil, err
	}

	if _, err := g.run("tag", "v1.0-lw"); err != nil {
		return gitRepo{}, nil, err
	}
	if _, err := g.run("tag", "-a", "v1.0", "-m", "release 1.0"); err != nil {
		return gitRepo{}, nil, err
	}

	refs := []refSpec{
		{kind: "branch", name: "main"},
		{kind: "branch", name: "feature"},
		{kind: "tag", name: "v1.0"},
		{kind: "tag", name: "v1.0-lw"},
	}
	return g, refs, nil
}

type demoFile struct {
	rel  string
	body string
	mode os.FileMode
}

// demoCommit writes files (on top of whatever's already there) and commits.
// A zero at commits at real now, like git always does; a real value backdates
// the commit's own author/committer date (GIT_AUTHOR_DATE/GIT_COMMITTER_DATE)
// — demo server's own story spans real calendar days, not one instant.
func demoCommit(g gitRepo, files []demoFile, message string, at time.Time) error {
	for _, f := range files {
		path := filepath.Join(g.dir, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f.body), f.mode); err != nil {
			return err
		}
	}
	if _, err := g.run("add", "-A"); err != nil {
		return err
	}
	if at.IsZero() {
		_, err := g.run("commit", "-q", "-m", message)
		return err
	}
	stamp := at.Format(time.RFC3339)
	_, err := g.runEnv([]string{"GIT_AUTHOR_DATE=" + stamp, "GIT_COMMITTER_DATE=" + stamp}, "commit", "-q", "-m", message)
	return err
}

// demoContributor mints a throwaway root contributor — never an application's
// key, same as the tests' own (-> convert_test.go, testContributor).
func demoContributor() (ranke.Contributor, ed25519.PrivateKey, error) {
	return mintContributor()
}
