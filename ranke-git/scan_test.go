package main

import (
	"context"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
)

// TestParseCVE pins the --cve syntax: a bare id, or id=url.
func TestParseCVE(t *testing.T) {
	for _, tc := range []struct {
		in      string
		id, url string
	}{
		{"CVE-2024-1234", "CVE-2024-1234", ""},
		{"CVE-2024-1234=https://nvd.nist.gov/vuln/detail/CVE-2024-1234", "CVE-2024-1234", "https://nvd.nist.gov/vuln/detail/CVE-2024-1234"},
		{"CVE-2024-1234=", "CVE-2024-1234", ""},
	} {
		got := parseCVE(tc.in)
		if got.id != tc.id || got.url != tc.url {
			t.Errorf("parseCVE(%q) = %+v, want {%q, %q}", tc.in, got, tc.id, tc.url)
		}
	}
}

// TestBuildScanShape pins the claim buildScan produces with content: external
// content, its edges preserved verbatim (derivation/input and relation/cve
// alike — buildScan itself is edge-type-agnostic, callers build the set).
func TestBuildScanShape(t *testing.T) {
	contributor, signer := testContributor(t)
	ctx := context.Background()
	u := ranke.NewMemoryUniverse()

	commitID, err := ranke.HashContent([]byte("fake target commit"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	cveID, err := ranke.HashContent([]byte("fake cve"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	input, err := ranke.NewEdge(ranke.EdgeConfig{Reference: commitID, Type: edgeInput})
	if err != nil {
		t.Fatalf("input edge: %v", err)
	}
	cveEdge, err := ranke.NewEdge(ranke.EdgeConfig{Reference: cveID, Type: edgeCVE, RelationDirection: ranke.RelationTo})
	if err != nil {
		t.Fatalf("cve edge: %v", err)
	}

	claim, err := buildScan(ctx, u, contributor, signer, []byte("scanner output"), 3, []ranke.Edge{input, cveEdge}, time.Time{})
	if err != nil {
		t.Fatalf("buildScan: %v", err)
	}
	if got := claim.Node().Type(); got != nodeScan {
		t.Errorf("type = %q, want %q", got, nodeScan)
	}
	if got, want := claim.Node().Height(), uint64(4); got != want {
		t.Errorf("height = %d, want %d", got, want)
	}
	if claim.Node().ContentKind() != ranke.ContentExternal {
		t.Errorf("ContentKind() = %v, want ContentExternal", claim.Node().ContentKind())
	}

	found := claim.Edges(ranke.EdgeFilterType{Type: edgeCVE})
	if len(found) != 1 || !found[0].Reference().Equal(cveID) {
		t.Errorf("relation/cve edges = %v, want one edge to %s", found, cveID)
	}
	if found[0].RelationDirection() != ranke.RelationTo {
		t.Errorf("relation/cve direction = %v, want RelationTo", found[0].RelationDirection())
	}

	got, err := readClaimContent(t, ctx, u, claim)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if string(got) != "scanner output" {
		t.Errorf("content = %q, want %q", got, "scanner output")
	}
}

// TestBuildScanContentless pins that omitting scanner output produces a
// valid, signable claim with no content at all — V-CONTENT permits it.
func TestBuildScanContentless(t *testing.T) {
	contributor, signer := testContributor(t)
	ctx := context.Background()
	u := ranke.NewMemoryUniverse()

	claim, err := buildScan(ctx, u, contributor, signer, nil, 3, nil, time.Time{})
	if err != nil {
		t.Fatalf("buildScan: %v", err)
	}
	if claim.Node().ContentKind() != ranke.ContentNone {
		t.Errorf("ContentKind() = %v, want ContentNone", claim.Node().ContentKind())
	}
}
