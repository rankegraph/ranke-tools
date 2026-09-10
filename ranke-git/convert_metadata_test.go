package main

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
)

// TestBlobAndEntryPathFields pins the path field added for readability
// (-> DESIGN.md): every tree entry edge carries the entry's full repo-root
// path, and a blob claim's own path field records where it was first minted
// — which, for a blob two files share, is only one of the two.
func TestBlobAndEntryPathFields(t *testing.T) {
	src := t.TempDir()
	g := initRepo(t, src)
	writeFile(t, src, "README.md", []byte("readme\n"), 0o644)
	writeFile(t, src, "pkg/sub/a.go", []byte("package sub\n"), 0o644)
	writeFile(t, src, "pkg/sub/b.go", []byte("package sub\n"), 0o644) // shares a.go's content
	sha := commitAll(t, g, "path fields")

	contributor, signer := testContributor(t)
	u := ranke.NewMemoryUniverse()
	claims, err := gitToClaims(context.Background(), g, sha, nil, u, contributor, signer, testRepoURL, testProject, prep{}, time.Time{})
	if err != nil {
		t.Fatalf("gitToClaims: %v", err)
	}

	gotEntryPaths := map[string]bool{}
	for _, c := range claims {
		for _, e := range c.Edges(ranke.EdgeFilterType{Type: edgeEntry}) {
			path, err := e.GetField("path")
			if err != nil {
				t.Fatalf("entry edge missing path field: %v", err)
			}
			gotEntryPaths[path] = true
		}
	}
	for _, want := range []string{"README.md", "pkg", "pkg/sub", "pkg/sub/a.go", "pkg/sub/b.go"} {
		if !gotEntryPaths[want] {
			t.Errorf("no tree entry edge carries path %q; got %v", want, gotEntryPaths)
		}
	}

	readme := mustFindByPath(t, claims, "README.md")
	if path, err := readme.Node().GetField("path"); err != nil || path != "README.md" {
		t.Errorf("README.md blob's own path field = %q, %v, want %q", path, err, "README.md")
	}
	shared := mustFindByPath(t, claims, "pkg/sub/a.go")
	if path, err := shared.Node().GetField("path"); err != nil || (path != "pkg/sub/a.go" && path != "pkg/sub/b.go") {
		t.Errorf("shared blob's own path field = %q, %v, want one of pkg/sub/{a,b}.go", path, err)
	}
}

// TestVersionFieldOnlyOnCommit pins that ranke_git_version lands on the
// commit claim only, never on trees/blobs (-> DESIGN.md).
func TestVersionFieldOnlyOnCommit(t *testing.T) {
	old := version
	version = "vTest"
	defer func() { version = old }()

	src := t.TempDir()
	g := initRepo(t, src)
	writeFile(t, src, "a.txt", []byte("content\n"), 0o644)
	sha := commitAll(t, g, "version field")

	contributor, signer := testContributor(t)
	u := ranke.NewMemoryUniverse()
	claims, err := gitToClaims(context.Background(), g, sha, nil, u, contributor, signer, testRepoURL, testProject, prep{}, time.Time{})
	if err != nil {
		t.Fatalf("gitToClaims: %v", err)
	}

	commit := findByType(t, claims, nodeCommit)
	if got, err := commit.Node().GetField(versionField); err != nil || got != "vTest" {
		t.Errorf("commit's %s = %q, %v, want %q", versionField, got, err, "vTest")
	}
	tree := findByType(t, claims, nodeTree)
	if _, err := tree.Node().GetField(versionField); err == nil {
		t.Errorf("tree claim carries %s, want it commit-only", versionField)
	}
}

// TestDatedOnCommitAndBlob pins that dated lands on the commit (its own
// author date, passed through unconverted — WithDatedEDTF's instant branch
// accepts git's own ISO 8601 output as-is) and, first-mint only, on the blob
// it introduces — never on the tree, which stays purely structural
// (-> DESIGN.md).
func TestDatedOnCommitAndBlob(t *testing.T) {
	src := t.TempDir()
	g := initRepo(t, src)
	writeFile(t, src, "a.txt", []byte("content\n"), 0o644)
	sha := commitAll(t, g, "dated field")
	want, err := g.commitAuthorDate(sha)
	if err != nil {
		t.Fatalf("commitAuthorDate: %v", err)
	}

	contributor, signer := testContributor(t)
	u := ranke.NewMemoryUniverse()
	claims, err := gitToClaims(context.Background(), g, sha, nil, u, contributor, signer, testRepoURL, testProject, prep{}, time.Time{})
	if err != nil {
		t.Fatalf("gitToClaims: %v", err)
	}

	commit := findByType(t, claims, nodeCommit)
	if got := commit.Node().Dated(); got != want {
		t.Errorf("commit's Dated() = %q, want %q", got, want)
	}
	blob := findByType(t, claims, nodeBlob)
	if got := blob.Node().Dated(); got != want {
		t.Errorf("blob's Dated() = %q, want %q", got, want)
	}
	tree := findByType(t, claims, nodeTree)
	if got := tree.Node().Dated(); got != "" {
		t.Errorf("tree carries Dated() = %q, want none", got)
	}
}

// mustFindByPath returns the blob claim a tree entry edge cites under path,
// by walking every claim's entry edges for one whose path field matches.
func mustFindByPath(t *testing.T, claims []ranke.Claim, path string) ranke.Claim {
	t.Helper()
	byID := make(map[string]ranke.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID().String()] = c
	}
	for _, c := range claims {
		for _, e := range c.Edges(ranke.EdgeFilterType{Type: edgeEntry}) {
			got, err := e.GetField("path")
			if err == nil && got == path {
				target, ok := byID[e.Reference().String()]
				if !ok {
					t.Fatalf("entry %q references a claim not in the set", path)
				}
				return target
			}
		}
	}
	t.Fatalf("no tree entry edge with path %q", path)
	return nil
}
