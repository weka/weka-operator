package inventory

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/capacityplanner"
)

const tib = 1024 // GiB per TiB

// ownedDriveContainer builds a drive-sharing WekaContainer owned by ownerUID, pinned to node, requesting
// the given total capacity split by ratio.
func ownedDriveContainer(ownerUID, node string, capGiB, tlc, qlc int) weka.WekaContainer {
	c := weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.ContainerCapacity = capGiB
	c.Spec.DriveTypesRatio = &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc}
	c.Spec.NodeAffinity = weka.NodeName(node)
	if ownerUID != "" {
		c.OwnerReferences = []metav1.OwnerReference{{Kind: "WekaCluster", UID: types.UID(ownerUID)}}
	}
	return c
}

// modeContainer builds a non-drive WekaContainer of the given mode pinned to node, requesting numCores
// CPUs and hugepagesMiB of 2Mi hugepages.
func modeContainer(mode, node string, numCores, hugepagesMiB int) weka.WekaContainer {
	c := weka.WekaContainer{}
	c.Spec.Mode = mode
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Spec.NumCores = numCores
	c.Spec.Hugepages = hugepagesMiB
	return c
}

func strPtr(s string) *string { return &s }

// nodeNamed builds a corev1.Node with a name and labels (compute candidates carry no drive annotations).
func nodeNamed(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func testCons() *capacityplanner.CapacityConstraints {
	return &capacityplanner.CapacityConstraints{
		TlcCapacityPerCoreGiB: 5 * tib,  // 1 core / 5 TiB TLC
		QlcCapacityPerCoreGiB: 50 * tib, // 1 core / 50 TiB QLC
		HugepagesPerCoreMiB:   capacityplanner.HugepagesPerCoreMiB,
		MemoryBaseMiB:         capacityplanner.MemoryBaseMiB,
		MemoryPerCoreMiB:      capacityplanner.MemoryPerCoreMiB,
	}
}

// invByName indexes a planner inventory by node name for assertions.
func invByName(inv []capacityplanner.NodeCapacity) map[string]capacityplanner.NodeCapacity {
	m := make(map[string]capacityplanner.NodeCapacity, len(inv))
	for _, nc := range inv {
		m[nc.NodeName] = nc
	}
	return m
}

// TestAggregateContainerResources_Drives verifies the per-node DRIVE footprint summed across ALL
// drive-sharing containers (this cluster's own AND other clusters') by aggregateContainerResources — the
// basis for the planner's remaining-headroom inventory. HUGEPAGES/MEMORY are derived from each
// container's capacity via the shared sizing model (NOT read from the possibly-stale Spec.Hugepages).
// CPU is the container's real PHYSICAL pod reservation via CPURequestCores (spec.NumCores + 1 per
// container under non-HT dedicated here — topos is nil), matching what the kube-scheduler reserves.
// Unpinned containers contribute nothing.
func TestAggregateContainerResources_Drives(t *testing.T) {
	cons := testCons()
	// Each n1 container: tlc=20TiB (ceil(20/5)=4 cores) + qlc=80TiB (ceil(80/50)=2 cores) = 6 data cores,
	// used for the capacity-derived hugepages/memory footprint.
	const perContainerCores = 6

	// Spec.Hugepages is deliberately stale to prove hugepages come from the capacity model, not the spec.
	// Spec.NumCores matches the capacity-derived count (6), the value the real pod reserves CPU from.
	other := ownedDriveContainer("cluster-other", "n1", 100*tib, 1, 4) // tlc=20TiB, qlc=80TiB
	other.Spec.NumCores = 6
	other.Spec.Hugepages = 1000

	mine := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4) // included too (own + other)
	mine.Spec.NumCores = 6
	mine.Spec.Hugepages = 2000

	labelOwned := ownedDriveContainer("", "n2", 50*tib, 1, 0) // tlc=50TiB → ceil(50/5)=10 cores
	labelOwned.Spec.NumCores = 10

	// Unscheduled / unpinned (no node): contributes nothing.
	noNode := ownedDriveContainer("cluster-other", "", 100*tib, 1, 4)
	noNode.Spec.NumCores = 7

	containers := []weka.WekaContainer{other, mine, labelOwned, noNode}
	res := aggregateContainerResources(containers, cons, nil) // nil topos → non-HT (cpu = numCores+1)

	if res.tlc["n1"] != 40*tib { // 20 (other) + 20 (mine)
		t.Errorf("n1 TLC = %d, want 40TiB (own + other)", res.tlc["n1"])
	}
	if res.qlc["n1"] != 160*tib {
		t.Errorf("n1 QLC = %d, want 160TiB", res.qlc["n1"])
	}
	if want := (6 + 1) + (6 + 1); res.cores["n1"] != want { // physical CPU: two dedicated containers, numCores+1 each
		t.Errorf("n1 cores = %d, want %d (physical CPU, spec.NumCores+1 per container)", res.cores["n1"], want)
	}
	if want := 2 * perContainerCores * capacityplanner.HugepagesPerCoreMiB; res.hugepages["n1"] != want {
		t.Errorf("n1 hugepages = %d, want %d", res.hugepages["n1"], want)
	}
	wantMem := 2 * capacityplanner.ComputeMemoryFootprintMiB(perContainerCores, cons)
	if res.memory["n1"] != wantMem {
		t.Errorf("n1 memory = %d, want %d", res.memory["n1"], wantMem)
	}
	if res.tlc["n2"] != 50*tib || res.qlc["n2"] != 0 {
		t.Errorf("n2 = (tlc %d, qlc %d), want (50TiB, 0)", res.tlc["n2"], res.qlc["n2"])
	}
	if res.cores["n2"] != 10+1 { // physical CPU: spec.NumCores(10) + 1
		t.Errorf("n2 cores = %d, want 11", res.cores["n2"])
	}
}

// TestAggregateContainerResources_ComputeAndOther verifies the per-node footprint charged for compute and
// other (e.g. ssdproxy) modes by the unified aggregator. Compute charges spec cores/hugepages plus the
// shared memory model — including OTHER clusters' compute, which the planner never saw before (gap B).
// Other modes charge spec cores (gap A) and 2Mi hugepages (chiefly the per-node ssdproxy container).
// 1Gi hugepages draw from a different pool, and unpinned containers contribute nothing.
func TestAggregateContainerResources_ComputeAndOther(t *testing.T) {
	cons := testCons()

	// Another cluster's compute on h6-9-b: cores + hugepages + memory all charged (gap B).
	otherCompute := modeContainer(weka.WekaContainerModeCompute, "h6-9-b", 8, 19572)

	// ssdproxy on the same node: cores (gap A) + 2Mi hugepages.
	ssdproxy := modeContainer(weka.WekaContainerModeSSDProxy, "h6-9-b", 2, 2962)

	// 1Gi hugepages are a distinct resource pool — its cores still count, its hugepages do not.
	oneGi := modeContainer(weka.WekaContainerModeClient, "h6-9-b", 3, 4096)
	oneGi.Spec.HugepagesSize = "1Gi"

	// Excluded: no node.
	noNode := modeContainer(weka.WekaContainerModeSSDProxy, "", 5, 2962)

	// Another hugepage-using mode on a second node is counted.
	s3 := modeContainer(weka.WekaContainerModeS3, "h6-9-c", 4, 1500)

	res := aggregateContainerResources(
		[]weka.WekaContainer{otherCompute, ssdproxy, oneGi, noNode, s3}, cons, nil) // nil topos → non-HT

	// CPU is the physical pod reservation (numCores+1 per container under non-HT dedicated).
	if want := (8 + 1) + (2 + 1) + (3 + 1); res.cores["h6-9-b"] != want { // compute + ssdproxy + 1Gi client
		t.Errorf("h6-9-b cores = %d, want %d (physical CPU: compute+ssdproxy+1Gi, numCores+1 each)", res.cores["h6-9-b"], want)
	}
	if want := 19572 + 2962; res.hugepages["h6-9-b"] != want { // 1Gi excluded from 2Mi pool
		t.Errorf("h6-9-b hugepages = %d, want %d (compute+ssdproxy; 1Gi excluded)", res.hugepages["h6-9-b"], want)
	}
	if want := capacityplanner.ComputeMemoryFootprintMiB(8, cons); res.memory["h6-9-b"] != want { // compute only
		t.Errorf("h6-9-b memory = %d, want %d (compute footprint)", res.memory["h6-9-b"], want)
	}
	if res.cores["h6-9-c"] != 4+1 || res.hugepages["h6-9-c"] != 1500 { // s3: physical CPU numCores(4)+1
		t.Errorf("h6-9-c = (cores %d, hugepages %d), want (5, 1500)", res.cores["h6-9-c"], res.hugepages["h6-9-c"])
	}
}

// TestAggregateContainerResources_SkipsMarkedForDeletion validates that aggregateContainerResources
// ignores containers where IsMarkedForDeletion() is true (DeletionTimestamp set + at least one
// finalizer), for both drive and compute modes. Only the live container's footprint must appear.
func TestAggregateContainerResources_SkipsMarkedForDeletion(t *testing.T) {
	cons := testCons()

	// Live drive container on "n1" (100 GiB total, tlc:qlc ratio 1:4 → 20 TiB TLC, 80 TiB QLC).
	live := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4)

	// Deleting drive container on "n1" — same capacity but marked for deletion.
	deleting := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	// Deleting compute container on "n1" — proves ALL modes are skipped, not just drive.
	delCompute := modeContainer(weka.WekaContainerModeCompute, "n1", 8, 19572)
	delCompute.DeletionTimestamp = &now
	delCompute.Finalizers = []string{"x"}

	res := aggregateContainerResources([]weka.WekaContainer{live, deleting, delCompute}, cons, nil)

	// Build a reference result from ONLY the live container and compare map-by-map, to avoid hardcoding
	// derived numbers and directly prove the two deleting containers contribute zero.
	resLiveOnly := aggregateContainerResources([]weka.WekaContainer{live}, cons, nil)

	if res.tlc["n1"] != resLiveOnly.tlc["n1"] {
		t.Errorf("n1 TLC: got %d, want %d (only live container; deleting drive must be skipped)",
			res.tlc["n1"], resLiveOnly.tlc["n1"])
	}
	if res.qlc["n1"] != resLiveOnly.qlc["n1"] {
		t.Errorf("n1 QLC: got %d, want %d (only live container; deleting drive must be skipped)",
			res.qlc["n1"], resLiveOnly.qlc["n1"])
	}
	if res.cores["n1"] != resLiveOnly.cores["n1"] {
		t.Errorf("n1 cores: got %d, want %d (deleting drive's cores + deleting compute's 8 cores must be skipped)",
			res.cores["n1"], resLiveOnly.cores["n1"])
	}
	if res.hugepages["n1"] != resLiveOnly.hugepages["n1"] {
		t.Errorf("n1 hugepages: got %d, want %d (deleting containers must not charge hugepages)",
			res.hugepages["n1"], resLiveOnly.hugepages["n1"])
	}
	if res.memory["n1"] != resLiveOnly.memory["n1"] {
		t.Errorf("n1 memory: got %d, want %d (deleting containers must not charge memory)",
			res.memory["n1"], resLiveOnly.memory["n1"])
	}
}

// TestResolveInventoryFDValue is the regression guard for the COMPUTE-inventory FD bug: in label-based FD
// mode a compute-eligible node WITHOUT the FD label belongs to no failure domain and must be skipped
// (skip=true), never admitted with FDValue=node.Name. AUTO mode (nil config) falls back to the node name
// = FD per host. Both the drive and compute loops in NodeInventory route through this helper.
func TestResolveInventoryFDValue(t *testing.T) {
	labelFD := &weka.FailureDomain{Label: strPtr("topology.kubernetes.io/rack")}

	tests := []struct {
		name     string
		fd       *weka.FailureDomain
		node     *corev1.Node
		wantFD   string
		wantSkip bool
	}{
		{
			name:     "label-based: labeled compute node keeps its rack FD",
			fd:       labelFD,
			node:     nodeNamed("h1", map[string]string{"topology.kubernetes.io/rack": "rack-1"}),
			wantFD:   "rack-1",
			wantSkip: false,
		},
		{
			name:     "label-based: UNLABELED compute node is skipped (belongs to no FD)",
			fd:       labelFD,
			node:     nodeNamed("unlabeled-node", nil),
			wantFD:   "",
			wantSkip: true,
		},
		{
			name:     "AUTO mode: unlabeled node falls back to node name as its own FD",
			fd:       nil,
			node:     nodeNamed("h1", nil),
			wantFD:   "h1",
			wantSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFD, gotSkip := resolveInventoryFDValue(tt.node, tt.fd)
			if gotFD != tt.wantFD || gotSkip != tt.wantSkip {
				t.Errorf("resolveInventoryFDValue() = (%q, %v), want (%q, %v)", gotFD, gotSkip, tt.wantFD, tt.wantSkip)
			}
		})
	}
}

// TestMergeRoleNodes covers the union of drive- and compute-candidate node sets and the resulting
// compute-eligibility map, for both equal and differing drive/compute role selectors.
func TestMergeRoleNodes(t *testing.T) {
	driveCap := func(name string, tlc int) capacityplanner.NodeCapacity {
		return capacityplanner.NodeCapacity{NodeName: name, FDValue: name, TlcGiB: tlc, AllocatableCPU: 32}
	}
	computeOnly := func(name string) capacityplanner.NodeCapacity {
		return capacityplanner.NodeCapacity{NodeName: name, FDValue: name, AllocatableCPU: 32}
	}

	t.Run("equal selectors: every node both drive and compute, no diskless appends", func(t *testing.T) {
		drive := []capacityplanner.NodeCapacity{driveCap("n1", 50*tib), driveCap("n2", 50*tib)}
		compute := []capacityplanner.NodeCapacity{computeOnly("n1"), computeOnly("n2")}

		inv, eligible := mergeRoleNodes(drive, compute)
		if len(inv) != 2 {
			t.Fatalf("want 2 inventory entries (no diskless appends), got %d", len(inv))
		}
		byName := invByName(inv)
		for _, n := range []string{"n1", "n2"} {
			if byName[n].TlcGiB != 50*tib {
				t.Errorf("%s should keep its drive capacity, got %d", n, byName[n].TlcGiB)
			}
			if !eligible[n] {
				t.Errorf("%s should be compute-eligible", n)
			}
		}
	})

	t.Run("different selectors: drive-only, shared, and compute-only diskless", func(t *testing.T) {
		drive := []capacityplanner.NodeCapacity{driveCap("driveOnly", 50*tib), driveCap("shared", 50*tib)}
		compute := []capacityplanner.NodeCapacity{computeOnly("shared"), computeOnly("computeOnly")}

		inv, eligible := mergeRoleNodes(drive, compute)
		byName := invByName(inv)
		if len(inv) != 3 {
			t.Fatalf("want 3 inventory entries (driveOnly, shared, computeOnly), got %d", len(inv))
		}
		// drive-only node: kept with capacity, NOT compute-eligible.
		if byName["driveOnly"].TlcGiB != 50*tib || eligible["driveOnly"] {
			t.Errorf("driveOnly should keep capacity and be compute-ineligible, got cap=%d eligible=%v", byName["driveOnly"].TlcGiB, eligible["driveOnly"])
		}
		// shared node: kept with drive capacity AND compute-eligible.
		if byName["shared"].TlcGiB != 50*tib || !eligible["shared"] {
			t.Errorf("shared should keep capacity and be compute-eligible, got cap=%d eligible=%v", byName["shared"].TlcGiB, eligible["shared"])
		}
		// compute-only node: appended diskless (no drive capacity) AND compute-eligible.
		if byName["computeOnly"].TlcGiB != 0 || byName["computeOnly"].QlcGiB != 0 || !eligible["computeOnly"] {
			t.Errorf("computeOnly should be diskless and compute-eligible, got tlc=%d qlc=%d eligible=%v", byName["computeOnly"].TlcGiB, byName["computeOnly"].QlcGiB, eligible["computeOnly"])
		}
	})
}
