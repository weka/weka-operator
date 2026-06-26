package operations

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/services/ssdproxy"
)

// scanFixture builds a representative full-scan: two live-claimed VIDs, two dead-cluster orphans,
// and one VID owned by a live cluster GUID but unclaimed by any container.
func scanFixture() []scannedVID {
	return []scannedVID{
		{node: "node-a", vd: ssdproxy.VirtualDrive{VirtualUUID: "vid-live-1", ClusterGUID: "guid-live", PhysicalUUID: "phys-1", SizeGB: 384}},
		{node: "node-a", vd: ssdproxy.VirtualDrive{VirtualUUID: "vid-dead-1", ClusterGUID: "guid-dead", PhysicalUUID: "phys-1", SizeGB: 407}},
		{node: "node-b", vd: ssdproxy.VirtualDrive{VirtualUUID: "vid-live-2", ClusterGUID: "guid-live", PhysicalUUID: "phys-2", SizeGB: 384}},
		{node: "node-b", vd: ssdproxy.VirtualDrive{VirtualUUID: "vid-dead-2", ClusterGUID: "guid-dead", PhysicalUUID: "phys-2", SizeGB: 491}},
		{node: "node-b", vd: ssdproxy.VirtualDrive{VirtualUUID: "vid-unclaimed", ClusterGUID: "guid-live", PhysicalUUID: "phys-2", SizeGB: 393}},
	}
}

func staleByUUID(stale []weka.StaleVirtualDriveInfo) map[string]weka.StaleVirtualDriveInfo {
	m := map[string]weka.StaleVirtualDriveInfo{}
	for _, s := range stale {
		m[s.VirtualUUID] = s
	}
	return m
}

func TestComputeStaleVids_CategoriesAndClaimedExclusion(t *testing.T) {
	claimed := map[string]bool{"vid-live-1": true, "vid-live-2": true}
	liveGUIDs := map[string]bool{"guid-live": true}

	stale := computeStaleVids(scanFixture(), claimed, liveGUIDs, false)

	if len(stale) != 3 {
		t.Fatalf("expected 3 stale VIDs, got %d: %+v", len(stale), stale)
	}
	byUUID := staleByUUID(stale)

	if _, ok := byUUID["vid-live-1"]; ok {
		t.Errorf("claimed VID vid-live-1 must not be reported stale")
	}
	if _, ok := byUUID["vid-live-2"]; ok {
		t.Errorf("claimed VID vid-live-2 must not be reported stale")
	}
	if got := byUUID["vid-dead-1"].Category; got != weka.StaleVidCategoryDeadCluster {
		t.Errorf("vid-dead-1 category = %q, want %q", got, weka.StaleVidCategoryDeadCluster)
	}
	if got := byUUID["vid-dead-2"].Category; got != weka.StaleVidCategoryDeadCluster {
		t.Errorf("vid-dead-2 category = %q, want %q", got, weka.StaleVidCategoryDeadCluster)
	}
	if got := byUUID["vid-unclaimed"].Category; got != weka.StaleVidCategoryLiveClusterUnclaimed {
		t.Errorf("vid-unclaimed category = %q, want %q", got, weka.StaleVidCategoryLiveClusterUnclaimed)
	}
}

func TestComputeStaleVids_OnlyNonExistingClusters(t *testing.T) {
	claimed := map[string]bool{"vid-live-1": true, "vid-live-2": true}
	liveGUIDs := map[string]bool{"guid-live": true}

	stale := computeStaleVids(scanFixture(), claimed, liveGUIDs, true)

	byUUID := staleByUUID(stale)
	if len(stale) != 2 {
		t.Fatalf("onlyNonExisting: expected 2 dead-cluster VIDs, got %d: %+v", len(stale), stale)
	}
	if _, ok := byUUID["vid-unclaimed"]; ok {
		t.Errorf("onlyNonExisting must exclude live_cluster_unclaimed VID vid-unclaimed")
	}
	for _, s := range stale {
		if s.Category != weka.StaleVidCategoryDeadCluster {
			t.Errorf("onlyNonExisting yielded non-dead category %q for %s", s.Category, s.VirtualUUID)
		}
	}
}

func TestComputeStaleVids_ReappearingClaimClearsIt(t *testing.T) {
	liveGUIDs := map[string]bool{"guid-live": true}

	// Cycle 1: vid-unclaimed is stale.
	claimed1 := map[string]bool{"vid-live-1": true, "vid-live-2": true}
	stale1 := computeStaleVids(scanFixture(), claimed1, liveGUIDs, false)
	if _, ok := staleByUUID(stale1)["vid-unclaimed"]; !ok {
		t.Fatalf("vid-unclaimed should be stale in cycle 1")
	}

	// Cycle 2: a container now claims vid-unclaimed -> it must drop out of the stale set.
	claimed2 := map[string]bool{"vid-live-1": true, "vid-live-2": true, "vid-unclaimed": true}
	stale2 := computeStaleVids(scanFixture(), claimed2, liveGUIDs, false)
	if _, ok := staleByUUID(stale2)["vid-unclaimed"]; ok {
		t.Errorf("vid-unclaimed became claimed and must no longer be stale")
	}
	if len(stale2) != 2 {
		t.Errorf("expected 2 stale after reclaim, got %d", len(stale2))
	}
}

func TestFingerprint_StableAndSensitive(t *testing.T) {
	claimed := map[string]bool{"vid-live-1": true, "vid-live-2": true}
	liveGUIDs := map[string]bool{"guid-live": true}

	fpEmpty := fingerprintStaleVids(nil)
	if fpEmpty != "" {
		t.Errorf("empty stale set fingerprint should be empty, got %q", fpEmpty)
	}

	a := computeStaleVids(scanFixture(), claimed, liveGUIDs, false)
	b := computeStaleVids(scanFixture(), claimed, liveGUIDs, false)
	if fingerprintStaleVids(a) != fingerprintStaleVids(b) {
		t.Errorf("identical stale sets must produce identical fingerprints")
	}

	// A VID becoming claimed changes the set -> fingerprint must differ.
	claimedMore := map[string]bool{"vid-live-1": true, "vid-live-2": true, "vid-unclaimed": true}
	c := computeStaleVids(scanFixture(), claimedMore, liveGUIDs, false)
	if fingerprintStaleVids(a) == fingerprintStaleVids(c) {
		t.Errorf("a changed stale set must produce a different fingerprint")
	}
}

func TestDeletionEligible_DoubleGate(t *testing.T) {
	const fp = "abc123"

	cases := []struct {
		name       string
		staleCount int
		current    string
		previous   string
		want       bool
	}{
		{"stable non-empty -> eligible", 3, fp, fp, true},
		{"first cycle, no previous -> not eligible", 3, fp, "", false},
		{"changed set -> not eligible", 3, fp, "different", false},
		{"empty set -> not eligible", 0, "", "", false},
		{"empty current but matching previous -> not eligible", 0, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deletionEligible(tc.staleCount, tc.current, tc.previous); got != tc.want {
				t.Errorf("deletionEligible(%d,%q,%q) = %v, want %v", tc.staleCount, tc.current, tc.previous, got, tc.want)
			}
		})
	}
}
