package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// TestSubtypeCharsMatchesChecksubtype pins the ADT's own rule (checkSubtype,
// shared with R-FIELDS field names): a leading [a-z0-9], then [a-z0-9_]*.
func TestSubtypeCharsMatchesChecksubtype(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"build_log", true},
		{"artifact_windows", true},
		{"a", true},
		{"9start", true},
		{"", false},
		{"_leading", false},
		{"Build_Log", false},
		{"test-report", false},
		{"has space", false},
		{"has.dot", false},
	} {
		if got := subtypeChars.MatchString(tc.in); got != tc.want {
			t.Errorf("subtypeChars.MatchString(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestBuildAttachmentShape pins the claim buildAttachment produces, fully
// locally: namespaced type, the name field, the declared encoding, and a
// relation/attached_to edge pointing at the target with RelationTo.
func TestBuildAttachmentShape(t *testing.T) {
	contributor, signer := testContributor(t)
	ctx := context.Background()
	u := ranke.NewMemoryUniverse()

	targetID, err := ranke.HashContent([]byte("fake target commit"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	target := reused{id: targetID, height: 3}

	a := attachment{
		typ:         "source/" + gitPrefix + "build_log",
		name:        "release build log",
		contentType: "text/plain",
		content:     []byte("log body"),
	}
	claim, err := buildAttachment(ctx, u, contributor, signer, target, a, time.Time{})
	if err != nil {
		t.Fatalf("buildAttachment: %v", err)
	}

	if got := claim.Node().Type(); got != "source/git_build_log" {
		t.Errorf("type = %q, want source/git_build_log", got)
	}
	if got, err := claim.Node().GetField("name"); err != nil || got != "release build log" {
		t.Errorf("name field = %q, %v, want %q", got, err, "release build log")
	}
	if got := claim.Node().Encoding(); got != "text/plain" {
		t.Errorf("encoding = %q, want text/plain", got)
	}
	if got, want := claim.Node().Height(), target.height+1; got != want {
		t.Errorf("height = %d, want %d", got, want)
	}

	edges := claim.Edges(ranke.EdgeFilterType{Type: edgeAttachedTo})
	if len(edges) != 1 {
		t.Fatalf("attached_to edges = %d, want 1", len(edges))
	}
	if !edges[0].Reference().Equal(targetID) {
		t.Errorf("attached_to reference = %s, want %s", edges[0].Reference(), targetID)
	}
	if edges[0].RelationDirection() != ranke.RelationTo {
		t.Errorf("attached_to direction = %v, want RelationTo", edges[0].RelationDirection())
	}

	got, err := readClaimContent(t, ctx, u, claim)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if string(got) != "log body" {
		t.Errorf("content = %q, want %q", got, "log body")
	}
}

// TestVerifiedChecksum pins the spellings a release process publishes, and
// the two refusals.
func TestVerifiedChecksum(t *testing.T) {
	content := []byte("log body")
	sum := sha256.Sum256(content)
	sha256Hex := hex.EncodeToString(sum[:])
	sum512 := sha512.Sum512(content)

	for _, tc := range []struct{ name, in, want, wantErr string }{
		{name: "bare hex is sha256", in: sha256Hex, want: "sha256:" + sha256Hex},
		{name: "prefixed", in: "sha256:" + sha256Hex, want: "sha256:" + sha256Hex},
		{name: "uppercase is normalised", in: strings.ToUpper(sha256Hex), want: "sha256:" + sha256Hex},
		{name: "sha512", in: "sha512:" + hex.EncodeToString(sum512[:]), want: "sha512:" + hex.EncodeToString(sum512[:])},
		{name: "unknown algorithm", in: "blake3:abc", wantErr: "algorithm \"blake3\" is unknown"},
		{name: "mismatch", in: "sha256:" + strings.Repeat("0", 64), wantErr: "the content is sha256:" + sha256Hex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifiedChecksum(tc.in, content)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifiedChecksum: %v", err)
			}
			if got != tc.want {
				t.Errorf("checksum = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAttachmentChecksumField pins that a verified checksum reaches the claim
// and that an attachment without one carries no such field.
func TestAttachmentChecksumField(t *testing.T) {
	ctx := context.Background()
	u := ranke.NewMemoryUniverse()
	contributor, signer := testContributor(t)
	targetID, err := ranke.HashContent([]byte("target"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	target := reused{id: targetID, height: 3}
	content := []byte("artifact bytes")
	sum := sha256.Sum256(content)

	a := attachment{
		typ:         "source/" + gitPrefix + "artifact",
		name:        "site.tar.gz",
		contentType: "application/gzip",
		checksum:    "sha256:" + hex.EncodeToString(sum[:]),
		content:     content,
	}
	claim, err := buildAttachment(ctx, u, contributor, signer, target, a, time.Time{})
	if err != nil {
		t.Fatalf("buildAttachment: %v", err)
	}
	if got, err := claim.Node().GetField("checksum"); err != nil || got != a.checksum {
		t.Errorf("checksum field = %q, %v, want %q", got, err, a.checksum)
	}

	a.checksum = ""
	plain, err := buildAttachment(ctx, u, contributor, signer, target, a, time.Time{})
	if err != nil {
		t.Fatalf("buildAttachment: %v", err)
	}
	if _, err := plain.Node().GetField("checksum"); err == nil {
		t.Error("attachment without --checksum carries a checksum field, want none")
	}
}

// writeAsset writes one release asset under dir, creating parents.
func writeAsset(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestAttachmentsFromADirectory pins the release-assets case: every file
// under the directory becomes its own attachment, named by its path within,
// with a sidecar digest folded into the file it covers.
func TestAttachmentsFromADirectory(t *testing.T) {
	dir := t.TempDir()
	tarball := "tarball bytes"
	sum := sha256.Sum256([]byte(tarball))
	writeAsset(t, dir, "site.tar.gz", tarball)
	writeAsset(t, dir, "site.tar.gz.sha256", hex.EncodeToString(sum[:])+"  site.tar.gz\n")
	writeAsset(t, dir, "linux/tool", "binary bytes")

	s := attachSpec{kind: "artifact"}
	as, err := s.attachments(&cobra.Command{Use: "attach"}, []string{dir})
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	byName := map[string]attachment{}
	for _, a := range as {
		byName[a.name] = a
	}
	if len(as) != 2 {
		t.Fatalf("attachments = %d (%v), want 2 — the sidecar is a field, not an attachment", len(as), slices.Sorted(maps.Keys(byName)))
	}
	if got := byName["site.tar.gz"].checksum; got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("site.tar.gz checksum = %q, want the sidecar's digest", got)
	}
	if got := byName["linux/tool"].name; got != "linux/tool" {
		t.Errorf("nested asset name = %q, want its path under the directory", got)
	}
	if got := byName["site.tar.gz"].typ; got != "source/git_artifact" {
		t.Errorf("type = %q, want source/git_artifact", got)
	}
}

// TestAttachmentsRefusesOneNameForManyFiles pins that --name cannot title a
// batch, since it would have to title every file in it.
func TestAttachmentsRefusesOneNameForManyFiles(t *testing.T) {
	dir := t.TempDir()
	writeAsset(t, dir, "a.txt", "a")
	writeAsset(t, dir, "b.txt", "b")

	s := attachSpec{kind: "artifact", name: "everything"}
	if _, err := s.attachments(&cobra.Command{Use: "attach"}, []string{dir}); err == nil {
		t.Error("--name over two files: want an error, got none")
	}
}

// TestAttachmentsRefusesAWrongSidecar pins that a sidecar is verified like
// any other checksum.
func TestAttachmentsRefusesAWrongSidecar(t *testing.T) {
	dir := t.TempDir()
	writeAsset(t, dir, "site.tar.gz", "tarball bytes")
	writeAsset(t, dir, "site.tar.gz.sha256", strings.Repeat("0", 64)+"  site.tar.gz\n")

	s := attachSpec{kind: "artifact"}
	if _, err := s.attachments(&cobra.Command{Use: "attach"}, []string{dir}); err == nil {
		t.Error("sidecar disagreeing with the content: want an error, got none")
	}
}

// TestAttachmentsKeepsAnUnpairedSidecar pins that a .sha256 whose file is
// absent stays an attachment of its own rather than vanishing.
func TestAttachmentsKeepsAnUnpairedSidecar(t *testing.T) {
	dir := t.TempDir()
	writeAsset(t, dir, "orphan.sha256", strings.Repeat("0", 64)+"  orphan\n")

	s := attachSpec{kind: "artifact"}
	as, err := s.attachments(&cobra.Command{Use: "attach"}, []string{dir})
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	if len(as) != 1 || as[0].name != "orphan.sha256" {
		t.Errorf("attachments = %+v, want the unpaired sidecar itself", as)
	}
}

// TestMediaType pins the three sources of an attachment's encoding.
func TestMediaType(t *testing.T) {
	if got := mediaType("application/x-custom", "site.tar.gz", nil); got != "application/x-custom" {
		t.Errorf("given type = %q, want it kept", got)
	}
	if got := mediaType("", "notes.txt", []byte("hello")); got != "text/plain" {
		t.Errorf("notes.txt = %q, want text/plain without parameters", got)
	}
	if got := mediaType("", "mystery", []byte{0x00, 0x01, 0x02, 0x03}); got != "application/octet-stream" {
		t.Errorf("unknown bytes = %q, want application/octet-stream", got)
	}
}
