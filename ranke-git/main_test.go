package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ranke-git.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadConfigFillsUnsetFlags pins that a config file supplies whatever the
// command line didn't.
func TestLoadConfigFillsUnsetFlags(t *testing.T) {
	path := writeConfig(t, "server: https://from-file.example.com\nbranch: from-file\n")
	var o options
	o.configPath = path
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().StringVar(&o.server, "server", "", "")
	cmd.Flags().StringVar(&o.branch, "branch", "main", "")
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if err := o.loadConfig(cmd); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if o.server != "https://from-file.example.com" {
		t.Errorf("server = %q, want the file's value", o.server)
	}
	if o.branch != "from-file" {
		t.Errorf("branch = %q, want the file's value", o.branch)
	}
}

// TestLoadConfigFlagsWinOverFile pins that a flag given on the command line
// is never overwritten by the config file, even when the flag's value equals
// its own default (branch's "main") — Changed, not the value, decides.
func TestLoadConfigFlagsWinOverFile(t *testing.T) {
	path := writeConfig(t, "server: https://from-file.example.com\nbranch: from-file\n")
	var o options
	o.configPath = path
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().StringVar(&o.server, "server", "", "")
	cmd.Flags().StringVar(&o.branch, "branch", "main", "")
	if err := cmd.ParseFlags([]string{"--server", "https://from-flag.example.com", "--branch", "main"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if err := o.loadConfig(cmd); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if o.server != "https://from-flag.example.com" {
		t.Errorf("server = %q, want the flag's value", o.server)
	}
	if o.branch != "main" {
		t.Errorf("branch = %q, want the flag's value (even though it equals the default)", o.branch)
	}
}

// TestLoadConfigNoPathIsANoop pins that an unset --config touches nothing —
// no flags registered at all here, so any write would be a nil-pointer panic,
// not just a wrong value.
func TestLoadConfigNoPathIsANoop(t *testing.T) {
	var o options
	o.branch = "untouched"
	if err := o.loadConfig(&cobra.Command{Use: "x"}); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if o.branch != "untouched" {
		t.Errorf("branch = %q, want it left alone", o.branch)
	}
}

// TestSnapshotTargetNamesOneCommit pins the three spellings and the two ways
// of naming no commit at all.
func TestSnapshotTargetNamesOneCommit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		ref, branch, tag string
		want             string
		wantErr          string
	}{
		{name: "ref passes through", ref: "HEAD~2", want: "HEAD~2"},
		{name: "branch is a heads ref", branch: "main", want: "refs/heads/main"},
		{name: "tag is a tags ref", tag: "v1.0.0", want: "refs/tags/v1.0.0"},
		{name: "nothing", wantErr: "one of --ref, --git-branch, or --git-tag is required"},
		{name: "two at once", ref: "HEAD", tag: "v1.0.0", wantErr: "--ref and --git-tag name a commit each"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := snapshotTarget(tc.ref, tc.branch, tc.tag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("snapshotTarget: %v", err)
			}
			if got != tc.want {
				t.Errorf("target = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSnapshotTargetSeparatesATagFromASameNamedBranch is why --git-tag earns
// its place beside --ref: git resolves the bare name to the branch, so a
// snapshot of the tag has no other way to ask for it.
func TestSnapshotTargetSeparatesATagFromASameNamedBranch(t *testing.T) {
	dir := t.TempDir()
	g := initRepo(t, dir)
	writeFile(t, dir, "a.txt", []byte("one\n"), 0o644)
	tagged := commitAll(t, g, "tagged")
	if _, err := g.run("tag", "release"); err != nil {
		t.Fatalf("git tag: %v", err)
	}
	writeFile(t, dir, "a.txt", []byte("two\n"), 0o644)
	tip := commitAll(t, g, "later")
	if _, err := g.run("branch", "release"); err != nil {
		t.Fatalf("git branch: %v", err)
	}

	for _, tc := range []struct {
		spelling string
		target   string
		want     string
	}{
		{"--git-tag", mustSnapshotTarget(t, "", "", "release"), tagged},
		{"--git-branch", mustSnapshotTarget(t, "", "release", ""), tip},
	} {
		sha, err := resolveCommit(g, tc.target)
		if err != nil {
			t.Fatalf("%s: resolve %q: %v", tc.spelling, tc.target, err)
		}
		if sha != tc.want {
			t.Errorf("%s resolved to %s, want %s", tc.spelling, sha, tc.want)
		}
	}
}

func mustSnapshotTarget(t *testing.T, ref, branch, tag string) string {
	t.Helper()
	target, err := snapshotTarget(ref, branch, tag)
	if err != nil {
		t.Fatalf("snapshotTarget: %v", err)
	}
	return target
}
