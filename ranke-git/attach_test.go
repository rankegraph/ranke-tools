package main

import (
	"context"
	"testing"
	"time"

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
	contributor, signer := testIdentity(t)
	ctx := context.Background()
	u := ranke.NewMemoryUniverse()

	targetID, err := ranke.HashContent([]byte("fake target commit"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	target := reused{id: targetID, height: 3}

	claim, err := buildAttachment(ctx, u, contributor, signer, target,
		"source/"+gitPrefix+"build_log", "release build log", "text/plain", []byte("log body"), time.Time{})
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
