package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
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

// writeKey writes a fresh PKCS#8 PEM key at mode, the shape identity
// register writes and loadSigningKey reads back.
func writeKey(t *testing.T, dir, name string, mode os.FileMode) (string, []byte, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, body, priv
}

// TestLoadSigningKeySources pins every spelling of --signing-key, the CI one
// (env:VAR) included, against the same key read from a file.
func TestLoadSigningKeySources(t *testing.T) {
	dir := t.TempDir()
	path, body, priv := writeKey(t, dir, "contributor.pem", 0o600)
	t.Setenv("RANKE_TEST_KEY", string(body))

	for _, source := range []string{path, "file:" + path, "env:RANKE_TEST_KEY"} {
		got, err := loadSigningKey(source)
		if err != nil {
			t.Fatalf("loadSigningKey(%q): %v", source, err)
		}
		if !got.Equal(priv) {
			t.Errorf("loadSigningKey(%q) returned a different key", source)
		}
	}
}

// TestLoadSigningKeyRefusals pins that keysource's rules reach the caller —
// the mode check, the missing variable, and above all key material passed
// where a source belongs, which upstream reports as compromised.
func TestLoadSigningKeyRefusals(t *testing.T) {
	dir := t.TempDir()
	loose, material, _ := writeKey(t, dir, "loose.pem", 0o644)

	for _, tc := range []struct{ source, want string }{
		{loose, "readable by others"},
		{string(material), "treat this key as compromised"},
		{"env:RANKE_TEST_KEY_UNSET", "is unset"},
		{"file:", "names no file"},
		{filepath.Join(dir, "absent.pem"), "no such file"},
	} {
		_, err := loadSigningKey(tc.source)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("loadSigningKey(%q) = %v, want one containing %q", tc.source, err, tc.want)
		}
	}
}
