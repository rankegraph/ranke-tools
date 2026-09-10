package main

import (
	"strings"
	"testing"
)

// TestProjectFromRepoURL pins the remote forms a project name is read off,
// and the one that carries no name at all.
func TestProjectFromRepoURL(t *testing.T) {
	for _, tc := range []struct{ repoURL, want string }{
		{"https://github.com/acme/widgets.git", "widgets"},
		{"https://github.com/acme/widgets", "widgets"},
		{"https://github.com/acme/widgets/", "widgets"},
		{"git@github.com:acme/widgets.git", "widgets"},
		{"ssh://git@github.com:2222/acme/widgets.git", "widgets"},
		{"/srv/git/widgets.git", "widgets"},
		{"widgets", "widgets"},
		{"", ""},
		{"/", ""},
	} {
		if got := projectFromRepoURL(tc.repoURL); got != tc.want {
			t.Errorf("projectFromRepoURL(%q) = %q, want %q", tc.repoURL, got, tc.want)
		}
	}
}

// TestOriginURLIsTheDefaultRepoURL pins what a clone answers when --repo is
// left out, credentials and all.
func TestOriginURLIsTheDefaultRepoURL(t *testing.T) {
	dir := t.TempDir()
	g := initRepo(t, dir)
	if _, err := g.originURL(); err == nil {
		t.Error("originURL on a repo with no origin: want an error, got none")
	}
	if _, err := g.run("remote", "add", "origin", "https://ci-token:s3cret@github.com/acme/widgets.git"); err != nil {
		t.Fatalf("git remote add: %v", err)
	}
	got, err := g.originURL()
	if err != nil {
		t.Fatalf("originURL: %v", err)
	}
	if want := "https://github.com/acme/widgets.git"; got != want {
		t.Errorf("originURL = %q, want %q", got, want)
	}
}

// TestWithoutCredentialsKeepsAnSSHUserWhole pins both halves: an http(s)
// remote loses its userinfo, an ssh one keeps the account it logs in as.
func TestWithoutCredentialsKeepsAnSSHUserWhole(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://ci-token:s3cret@github.com/acme/widgets.git", "https://github.com/acme/widgets.git"},
		{"https://ghp_s3cret@github.com/acme/widgets.git", "https://github.com/acme/widgets.git"},
		{"git@github.com:acme/widgets.git", "git@github.com:acme/widgets.git"},
		{"ssh://git@github.com/acme/widgets.git", "ssh://git@github.com/acme/widgets.git"},
		{"https://github.com/acme/widgets.git", "https://github.com/acme/widgets.git"},
		{"/srv/git/widgets.git", "/srv/git/widgets.git"},
	} {
		if got := withoutCredentials(tc.remote); got != tc.want {
			t.Errorf("withoutCredentials(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// TestResolveRefSaysWhatIsMissing pins the three refusals, each naming the
// thing that was not there rather than repeating git's own advice.
func TestResolveRefSaysWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	g := initRepo(t, dir)
	writeFile(t, dir, "a.txt", []byte("one\n"), 0o644)
	head := commitAll(t, g, "only commit")

	if sha, err := resolveRef(g, "HEAD"); err != nil || sha != head {
		t.Fatalf("resolveRef(HEAD) = %q, %v, want %q", sha, err, head)
	}
	for _, tc := range []struct{ ref, want string }{
		{"refs/tags/v9.9.9", `no tag "v9.9.9" in this clone`},
		{"refs/heads/nope", `no branch "nope" in this clone`},
		{"nothing-like-this", `"nothing-like-this" names no tag, branch, or commit`},
	} {
		_, err := resolveRef(g, tc.ref)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("resolveRef(%q) = %v, want one containing %q", tc.ref, err, tc.want)
		}
	}
}

// TestResolveRefReportsAnUnusableClone pins that a directory which is no git
// repository says so, rather than blaming the ref.
func TestResolveRefReportsAnUnusableClone(t *testing.T) {
	_, err := resolveRef(gitRepo{dir: t.TempDir()}, "refs/tags/v1.0.0")
	if err == nil || strings.Contains(err.Error(), "no tag") {
		t.Errorf("err = %v, want one about the clone itself", err)
	}
}
