package main

import (
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
)

// TestGroupDriveCapacities covers the compact largest-first grouped form the explore-nodes table uses for
// a full-drives node's free-drive sizes (see groupDriveCapacities's doc comment): consecutive equal sizes
// collapse into one "NxSIZE" term, and — critically — a fully uniform slice must collapse to exactly ONE
// term (e.g. "6x14.0TiB"), never splitting into a spurious second all-zero term ("6x14.0TiB+0x...").
func TestGroupDriveCapacities(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want string
	}{
		{"empty/nil", nil, "-"},
		{
			name: "uniform node collapses to a single term",
			in:   []int{14307, 14307, 14307, 14307, 14307, 14307},
			want: "6x14.0TiB",
		},
		{
			name: "mixed node groups by run, largest-first input",
			in:   []int{14307, 14307, 14307, 14307, 14307, 7153},
			want: "5x14.0TiB+1x7.0TiB",
		},
		{
			name: "single drive",
			in:   []int{7153},
			want: "1x7.0TiB",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := groupDriveCapacities(tc.in)
			if got != tc.want {
				t.Errorf("groupDriveCapacities(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRenderNodesTable_FullDrivesNode_ShowsFreeSizesColumn covers requirement 1: the explore-nodes table
// must surface a compact grouped free-drive-sizes column for full-drives nodes, distinguishing a
// heterogeneous node's shape from a uniform one even when their free counts/totals look similar.
func TestRenderNodesTable_FullDrivesNode_ShowsFreeSizesColumn(t *testing.T) {
	nodes := []inventory.NodeDetail{
		{
			Node: "h6-8-a", Mode: "full",
			FreeFullDriveCount: 4, PhysFullDriveCount: 6,
			FreeFullDriveCapacitiesGiB:    []int{14307, 14307, 14307, 7153},
			ClaimedFullDriveCapacitiesGiB: []int{14307, 7153},
		},
		{
			Node: "h1-uniform", Mode: "full",
			FreeFullDriveCount: 6, PhysFullDriveCount: 6,
			FreeFullDriveCapacitiesGiB: []int{14307, 14307, 14307, 14307, 14307, 14307},
		},
		{
			Node: "n-shared", Mode: "shared",
		},
	}
	out := renderNodesTable(nodes, "")
	for _, want := range []string{
		"FREE SIZES",
		"h6-8-a", "3x14.0TiB+1x7.0TiB",
		"h1-uniform", "6x14.0TiB",
		"n-shared",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderNodesTable() missing %q\n---\n%s", want, out)
		}
	}
	// The uniform node's row must not contain a spurious second/zero term.
	if strings.Contains(out, "6x14.0TiB+0x") {
		t.Errorf("renderNodesTable() uniform node rendered a spurious zero term\n---\n%s", out)
	}
}

// TestRenderNodeDetail_FullDrivesNode_ShowsExactFreeAndClaimedDrives covers requirement 2: the per-node
// detail view must show the exact (ungrouped) per-drive capacities in GiB, largest-first, split free vs
// claimed.
func TestRenderNodeDetail_FullDrivesNode_ShowsExactFreeAndClaimedDrives(t *testing.T) {
	n := inventory.NodeDetail{
		Node: "h6-8-a", Mode: "full",
		FreeFullDriveCount: 4, PhysFullDriveCount: 6,
		FreeFullDriveCapacitiesGiB:    []int{14307, 14307, 14307, 7153},
		ClaimedFullDriveCapacitiesGiB: []int{14307, 7153},
	}
	out := renderNodeDetail(&n)
	for _, want := range []string{
		"Free drives (GiB):    14307, 14307, 14307, 7153",
		"Claimed drives (GiB): 14307, 7153",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderNodeDetail() missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderNodeDetail_SharedDrivesNode_NoFullDrivesLines confirms a shared-drives (or compute-only) node's
// detail view doesn't grow the new Free/Claimed drives lines at all (they are only meaningful when
// Mode == "full") — requirement 5's byte-identical-for-shared-nodes guarantee extended to the detail view.
func TestRenderNodeDetail_SharedDrivesNode_NoFullDrivesLines(t *testing.T) {
	n := inventory.NodeDetail{Node: "n-shared", Mode: "shared", PhysTlcGiB: 1000, FreeTlcGiB: 1000}
	out := renderNodeDetail(&n)
	if strings.Contains(out, "Free drives") || strings.Contains(out, "Claimed drives") {
		t.Errorf("renderNodeDetail() for a shared-drives node must not show Free/Claimed drives lines\n---\n%s", out)
	}
}
