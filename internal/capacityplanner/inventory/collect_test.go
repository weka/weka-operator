package inventory

import (
	"context"
	"encoding/json"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
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

// autoFullDrivesDriveContainer builds a non-sharing (auto-full-drives, !UsesDriveSharing) Mode=Drive WekaContainer keyed by containerPodKey(c) = {Namespace, Name}.
func autoFullDrivesDriveContainer(name, namespace, node string, numCores, hugepagesMiB int) weka.WekaContainer {
	c := weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Spec.NumCores = numCores
	c.Spec.Hugepages = hugepagesMiB
	return c
}

// computeContainer builds a Mode=Compute WekaContainer pinned to node, requesting numCores CPUs and hugepagesMiB of 2Mi hugepages.
func computeContainer(name, namespace, node string, numCores, hugepagesMiB int) weka.WekaContainer {
	c := weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Spec.NumCores = numCores
	c.Spec.Hugepages = hugepagesMiB
	return c
}

func strPtr(s string) *string { return &s }

// nodeNamed builds a corev1.Node with a name and labels, Ready by default so it reads as eligible.
func nodeNamed(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Status: readyNodeStatus()}
}

// readyNodeStatus returns a NodeStatus with NodeReady=True.
func readyNodeStatus() corev1.NodeStatus {
	return corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}
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

// TestAggregateContainerResources_Drives verifies drive-sharing containers (own + other clusters') are
// summed by node: hugepages/memory come from the capacity model, not the (possibly stale) Spec.Hugepages;
// CPU is the physical pod reservation (spec.NumCores+1, non-HT); unpinned containers contribute nothing.
func TestAggregateContainerResources_Drives(t *testing.T) {
	cons := testCons()
	// 100TiB @ tlc:qlc=1:4 → tlc=20TiB (4 cores) + qlc=80TiB (2 cores) = 6 data cores.
	const perContainerCores = 6

	// Spec.Hugepages is stale (not the source); Spec.NumCores matches perContainerCores.
	other := ownedDriveContainer("cluster-other", "n1", 100*tib, 1, 4)
	other.Spec.NumCores = 6
	other.Spec.Hugepages = 1000

	mine := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4)
	mine.Spec.NumCores = 6
	mine.Spec.Hugepages = 2000

	labelOwned := ownedDriveContainer("", "n2", 50*tib, 1, 0) // tlc=50TiB → ceil(50/5)=10 cores
	labelOwned.Spec.NumCores = 10

	noNode := ownedDriveContainer("cluster-other", "", 100*tib, 1, 4) // unpinned: contributes nothing
	noNode.Spec.NumCores = 7

	containers := []weka.WekaContainer{other, mine, labelOwned, noNode}
	res, _ := aggregateContainerResources(containers, cons, nil) // nil topos → non-HT

	if res.tlc["n1"] != 40*tib {
		t.Errorf("n1 TLC = %d, want 40TiB (own + other)", res.tlc["n1"])
	}
	if res.qlc["n1"] != 160*tib {
		t.Errorf("n1 QLC = %d, want 160TiB", res.qlc["n1"])
	}
	if want := (6 + 1) + (6 + 1); res.cores["n1"] != want {
		t.Errorf("n1 cores = %d, want %d (physical CPU, spec.NumCores+1 per container)", res.cores["n1"], want)
	}
	if want := 2 * perContainerCores * capacityplanner.HugepagesPerCoreMiB; res.hugepages["n1"] != want {
		t.Errorf("n1 hugepages = %d, want %d", res.hugepages["n1"], want)
	}
	// ComputeMemoryFootprintMiB(6, cons) = 8000 + 6*3000 = 26000, times 2 containers = 52000.
	if want := 52000; res.memory["n1"] != want {
		t.Errorf("n1 memory = %d, want %d", res.memory["n1"], want)
	}
	if res.tlc["n2"] != 50*tib || res.qlc["n2"] != 0 {
		t.Errorf("n2 = (tlc %d, qlc %d), want (50TiB, 0)", res.tlc["n2"], res.qlc["n2"])
	}
	if res.cores["n2"] != 10+1 {
		t.Errorf("n2 cores = %d, want 11", res.cores["n2"])
	}
}

// TestAggregateContainerResources_ComputeAndOther verifies compute (spec cores/hugepages + shared memory
// model, including other clusters') and other modes like ssdproxy (spec cores + 2Mi hugepages) are charged;
// 1Gi hugepages don't count toward the 2Mi pool, and unpinned containers contribute nothing.
func TestAggregateContainerResources_ComputeAndOther(t *testing.T) {
	cons := testCons()
	otherCompute := modeContainer(weka.WekaContainerModeCompute, "h6-9-b", 8, 19572)
	ssdproxy := modeContainer(weka.WekaContainerModeSSDProxy, "h6-9-b", 2, 2962)

	// 1Gi hugepages are a distinct resource pool — its cores still count, its hugepages do not.
	oneGi := modeContainer(weka.WekaContainerModeClient, "h6-9-b", 3, 4096)
	oneGi.Spec.HugepagesSize = "1Gi"

	noNode := modeContainer(weka.WekaContainerModeSSDProxy, "", 5, 2962)

	s3 := modeContainer(weka.WekaContainerModeS3, "h6-9-c", 4, 1500)

	res, _ := aggregateContainerResources(
		[]weka.WekaContainer{otherCompute, ssdproxy, oneGi, noNode, s3}, cons, nil) // nil topos → non-HT

	if want := (8 + 1) + (2 + 1) + (3 + 1); res.cores["h6-9-b"] != want {
		t.Errorf("h6-9-b cores = %d, want %d (physical CPU: compute+ssdproxy+1Gi, numCores+1 each)", res.cores["h6-9-b"], want)
	}
	if want := 19572 + 2962; res.hugepages["h6-9-b"] != want {
		t.Errorf("h6-9-b hugepages = %d, want %d (compute+ssdproxy; 1Gi excluded)", res.hugepages["h6-9-b"], want)
	}
	// ComputeMemoryFootprintMiB(8, cons) = 8000 + 8*3000 = 32000.
	if want := 32000; res.memory["h6-9-b"] != want {
		t.Errorf("h6-9-b memory = %d, want %d (compute footprint)", res.memory["h6-9-b"], want)
	}
	if res.cores["h6-9-c"] != 4+1 || res.hugepages["h6-9-c"] != 1500 {
		t.Errorf("h6-9-c = (cores %d, hugepages %d), want (5, 1500)", res.cores["h6-9-c"], res.hugepages["h6-9-c"])
	}
}

// TestAggregateContainerResources_SkipsMarkedForDeletion verifies deleted containers (DeletionTimestamp +
// finalizer) contribute nothing, for both drive and compute modes.
func TestAggregateContainerResources_SkipsMarkedForDeletion(t *testing.T) {
	cons := testCons()

	live := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4)

	deleting := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4) // same capacity, marked for deletion
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	delCompute := modeContainer(weka.WekaContainerModeCompute, "n1", 8, 19572) // proves all modes are skipped
	delCompute.DeletionTimestamp = &now
	delCompute.Finalizers = []string{"x"}

	res, _ := aggregateContainerResources([]weka.WekaContainer{live, deleting, delCompute}, cons, nil)

	// Reference: live container alone, to avoid hardcoding derived numbers.
	resLiveOnly, _ := aggregateContainerResources([]weka.WekaContainer{live}, cons, nil)

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

// TestResolveInventoryFDValue is the regression guard for the compute-inventory FD bug: in label-based FD
// mode, a node without the FD label belongs to no failure domain and must be skipped, never admitted with
// FDValue=node.Name; AUTO mode (nil config) falls back to node name as FD.
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

// fullDrivesAnnotation JSON-encodes entries for the weka.io/weka-full-drives node annotation.
func fullDrivesAnnotation(t *testing.T, entries []domain.DriveEntry) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal full-drives annotation: %v", err)
	}
	return string(b)
}

// driveNode builds a corev1.Node carrying the weka.io/weka-full-drives annotation (entries) plus generous Allocatable cpu/memory/hugepages.
func driveNode(t *testing.T, name string, entries []domain.DriveEntry) *corev1.Node {
	t.Helper()
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{consts.AnnotationWekaFullDrives: fullDrivesAnnotation(t, entries)},
		},
		Status: corev1.NodeStatus{
			Conditions: readyNodeStatus().Conditions,
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                   resource.MustParse("64"),
				corev1.ResourceMemory:                resource.MustParse("512Gi"),
				corev1.ResourceName("hugepages-2Mi"): resource.MustParse("64Gi"),
			},
		},
	}
}

// allocatedDriveContainer builds a Mode=Drive WekaContainer pinned to node with the given drive serials committed in Status.Allocations.Drives.
func allocatedDriveContainer(name, node string, serials []string) weka.WekaContainer {
	c := weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NodeAffinity = weka.NodeName(node)
	c.Spec.NumDrives = len(serials)
	c.Status.Allocations = &weka.ContainerAllocations{Drives: serials}
	return c
}

// newFullDrivesTestClient builds a fake controller-runtime client seeded with the given nodes and containers.
func newFullDrivesTestClient(t *testing.T, nodes []*corev1.Node, containers []*weka.WekaContainer) client.Client {
	t.Helper()
	objs := make([]client.Object, 0, len(nodes)+len(containers))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	for _, c := range containers {
		objs = append(objs, c)
	}
	return newInventoryTestClient(t, objs...)
}

// TestFullDrivesInventory_OwnVsFreeDrives verifies FullDrivesInventory splits signed full drives into
// OwnDriveCapacitiesGiB (this cluster's own container) vs. DriveCapacitiesGiB (still free) rather than one
// total bucket, and that another cluster's allocated drives count as neither (they vanish from this view).
func TestFullDrivesInventory_OwnVsFreeDrives(t *testing.T) {
	n1Owned := []domain.DriveEntry{{Serial: "n1-o1", CapacityGiB: 1000}, {Serial: "n1-o2", CapacityGiB: 1000}, {Serial: "n1-o3", CapacityGiB: 1000}}
	n1Free := []domain.DriveEntry{{Serial: "n1-f1", CapacityGiB: 500}, {Serial: "n1-f2", CapacityGiB: 500}}
	n1Entries := append(append([]domain.DriveEntry(nil), n1Owned...), n1Free...)
	n1 := driveNode(t, "n1", n1Entries)
	meContainer := allocatedDriveContainer("me-n1", "n1", domain.DriveEntrySerials(n1Owned))

	n2Other := []domain.DriveEntry{{Serial: "n2-o1", CapacityGiB: 800}, {Serial: "n2-o2", CapacityGiB: 800}}
	n2Free := []domain.DriveEntry{{Serial: "n2-f1", CapacityGiB: 300}}
	n2Entries := append(append([]domain.DriveEntry(nil), n2Other...), n2Free...)
	n2 := driveNode(t, "n2", n2Entries)
	otherContainer := allocatedDriveContainer("other-n2", "n2", domain.DriveEntrySerials(n2Other))
	otherContainer.OwnerReferences = []metav1.OwnerReference{{Kind: "WekaCluster", UID: types.UID("cluster-other")}}

	fakeClient := newFullDrivesTestClient(t,
		[]*corev1.Node{n1, n2},
		[]*weka.WekaContainer{&meContainer, &otherContainer},
	)

	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me", UID: types.UID("cluster-me")}}
	ownContainers := []*weka.WekaContainer{&meContainer} // caller-filtered: this cluster's own only

	collector := NewCollector(fakeClient)
	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, ownContainers, testCons())
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	byName := invByName(inv)

	n1cap, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory (has free drives, should be present)")
	}
	if want := []int{500, 500}; !equalInts(n1cap.DriveCapacitiesGiB, want) {
		t.Errorf("n1 DriveCapacitiesGiB (free) = %v, want %v", n1cap.DriveCapacitiesGiB, want)
	}
	if want := []int{1000, 1000, 1000}; !equalInts(n1cap.OwnDriveCapacitiesGiB, want) {
		t.Errorf("n1 OwnDriveCapacitiesGiB = %v, want %v", n1cap.OwnDriveCapacitiesGiB, want)
	}
	if n1cap.TlcGiB != 1000 { // sum(DriveCapacitiesGiB) only — free, NOT own+free (see NodeCapacity doc)
		t.Errorf("n1 TlcGiB = %d, want 1000 (free-only)", n1cap.TlcGiB)
	}

	n2cap, ok := byName["n2"]
	if !ok {
		t.Fatalf("n2 missing from inventory (has 1 free drive, should be present)")
	}
	if want := []int{300}; !equalInts(n2cap.DriveCapacitiesGiB, want) {
		t.Errorf("n2 DriveCapacitiesGiB (free) = %v, want %v (other cluster's 2 drives must be excluded)", n2cap.DriveCapacitiesGiB, want)
	}
	if len(n2cap.OwnDriveCapacitiesGiB) != 0 {
		t.Errorf("n2 OwnDriveCapacitiesGiB = %v, want empty (this cluster owns nothing on n2; other cluster's drives must not count as own)", n2cap.OwnDriveCapacitiesGiB)
	}
	if n2cap.TlcGiB != 300 {
		t.Errorf("n2 TlcGiB = %d, want 300 (free-only, other cluster's 800+800 excluded)", n2cap.TlcGiB)
	}
}

// TestFullDrivesInventory_FullyOwnedNode_StillEmitted: a node fully owned by this cluster (no free drives) must
// still appear in the inventory; n2 (no signed drives at all) stays a drive-less compute candidate.
func TestFullDrivesInventory_FullyOwnedNode_StillEmitted(t *testing.T) {
	owned := []domain.DriveEntry{
		{Serial: "n1-o1", CapacityGiB: 1000},
		{Serial: "n1-o2", CapacityGiB: 1000},
		{Serial: "n1-o3", CapacityGiB: 500},
	}
	n1 := driveNode(t, "n1", owned) // every signed drive is owned; none free
	meContainer := allocatedDriveContainer("me-n1", "n1", domain.DriveEntrySerials(owned))

	n2 := driveNode(t, "n2", nil) // no signed full drives at all — must stay skipped

	fakeClient := newFullDrivesTestClient(t,
		[]*corev1.Node{n1, n2},
		[]*weka.WekaContainer{&meContainer},
	)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me", UID: types.UID("cluster-me")}}
	ownContainers := []*weka.WekaContainer{&meContainer}

	collector := NewCollector(fakeClient)
	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, ownContainers, testCons())
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	byName := invByName(inv)

	n1cap, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory — a fully-owned node (no free drives) must still be emitted, "+
			"otherwise a converged cluster reads as having no signed drives; inventory = %+v", inv)
	}
	if len(n1cap.DriveCapacitiesGiB) != 0 {
		t.Errorf("n1 DriveCapacitiesGiB = %v, want empty (nothing is free)", n1cap.DriveCapacitiesGiB)
	}
	if want := []int{1000, 1000, 500}; !equalInts(n1cap.OwnDriveCapacitiesGiB, want) {
		t.Errorf("n1 OwnDriveCapacitiesGiB = %v, want %v", n1cap.OwnDriveCapacitiesGiB, want)
	}
	if n1cap.TlcGiB != 0 {
		t.Errorf("n1 TlcGiB = %d, want 0 (TlcGiB is sum(DriveCapacitiesGiB) — free only — see NodeCapacity's "+
			"doc comment; the owned capacity is carried by OwnDriveCapacitiesGiB)", n1cap.TlcGiB)
	}

	if n2cap, ok := byName["n2"]; ok {
		if len(n2cap.DriveCapacitiesGiB) != 0 || len(n2cap.OwnDriveCapacitiesGiB) != 0 {
			t.Errorf("n2 has drives (free=%v own=%v), want none — it has no signed full drives, so the "+
				"widened drive-loop guard must not have admitted it",
				n2cap.DriveCapacitiesGiB, n2cap.OwnDriveCapacitiesGiB)
		}
	}
}

// TestFullDrivesInventory_DeletingOwnContainer_DrivesNotCountedAsOwn: a deleting container's drives stay
// globally allocated (not free) but must not count as own either — avoiding a bogus own-inflation while
// still blocking a second container from claiming them; HasDeletingDriveContainer must be set too.
func TestFullDrivesInventory_DeletingOwnContainer_DrivesNotCountedAsOwn(t *testing.T) {
	owned := []domain.DriveEntry{{Serial: "n1-o1", CapacityGiB: 1000}, {Serial: "n1-o2", CapacityGiB: 1000}}
	free := []domain.DriveEntry{{Serial: "n1-f1", CapacityGiB: 500}}
	entries := append(append([]domain.DriveEntry(nil), owned...), free...)
	n1 := driveNode(t, "n1", entries)

	deleting := allocatedDriveContainer("me-n1-deleting", "n1", domain.DriveEntrySerials(owned))
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"} // required for the fake client to accept a preset DeletionTimestamp

	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&deleting})

	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me", UID: types.UID("cluster-me")}}
	ownContainers := []*weka.WekaContainer{&deleting} // reconciler doesn't pre-filter by deletion state

	collector := NewCollector(fakeClient)
	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, ownContainers, testCons())
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	byName := invByName(inv)

	n1cap, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory (has 1 free drive, should be present)")
	}
	if len(n1cap.OwnDriveCapacitiesGiB) != 0 {
		t.Errorf("n1 OwnDriveCapacitiesGiB = %v, want empty (deleting container's drives must not count as own)", n1cap.OwnDriveCapacitiesGiB)
	}
	if want := []int{500}; !equalInts(n1cap.DriveCapacitiesGiB, want) {
		t.Errorf("n1 DriveCapacitiesGiB (free) = %v, want %v (deleting container's still-allocated drives must stay excluded from free too)", n1cap.DriveCapacitiesGiB, want)
	}
	if !n1cap.HasDeletingDriveContainer {
		t.Errorf("n1 HasDeletingDriveContainer = false, want true")
	}
}

// sharedDrivesNode builds a corev1.Node carrying the weka.io/shared-drives annotation plus generous Allocatable cpu/memory/hugepages.
func sharedDrivesNode(t *testing.T, name string, drives []domain.SharedDriveInfo) *corev1.Node {
	t.Helper()
	b, err := json.Marshal(drives)
	if err != nil {
		t.Fatalf("marshal shared-drives annotation: %v", err)
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{consts.AnnotationSharedDrives: string(b)},
		},
		Status: corev1.NodeStatus{
			Conditions: readyNodeStatus().Conditions,
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                   resource.MustParse("64"),
				corev1.ResourceMemory:                resource.MustParse("512Gi"),
				corev1.ResourceName("hugepages-2Mi"): resource.MustParse("64Gi"),
			},
		},
	}
}

// testPod builds a single-container corev1.Pod named/namespaced, scheduled to node (or "" to leave it unscheduled), requesting cpuCores, hugepagesMiB, and memoryMiB.
func testPod(name, namespace, node string, phase corev1.PodPhase, cpuCores, hugepagesMiB, memoryMiB int) *corev1.Pod {
	reqs := corev1.ResourceList{}
	if cpuCores > 0 {
		reqs[corev1.ResourceCPU] = *resource.NewQuantity(int64(cpuCores), resource.DecimalSI)
	}
	if memoryMiB > 0 {
		reqs[corev1.ResourceMemory] = *resource.NewQuantity(int64(memoryMiB)*(1<<20), resource.BinarySI)
	}
	if hugepagesMiB > 0 {
		reqs[corev1.ResourceName("hugepages-2Mi")] = *resource.NewQuantity(int64(hugepagesMiB)*(1<<20), resource.BinarySI)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{
				{Name: "main", Resources: corev1.ResourceRequirements{Requests: reqs}},
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// newInventoryTestClient builds a fake controller-runtime client (corev1 + weka schemes) seeded with any mix of nodes, WekaContainers, and/or Pods.
func newInventoryTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("register weka scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// zeroNodeResources returns a nodeResources with every map initialized.
func zeroNodeResources() nodeResources {
	return nodeResources{
		tlc:       map[string]int{},
		qlc:       map[string]int{},
		cores:     map[string]int{},
		hugepages: map[string]int{},
		memory:    map[string]int{},
	}
}

// TestChargeForeignPods_ChargesForeignPod: a scheduled pod with no WekaContainer must still be charged
// against node headroom — otherwise the planner sees full headroom and a placed container sits Pending.
func TestChargeForeignPods_ChargesForeignPod(t *testing.T) {
	pod := testPod("foreign-pod", "default", "n1", corev1.PodRunning, 2, 4096, 8192)
	fakeClient := newInventoryTestClient(t, pod)
	collector := NewCollector(fakeClient)

	res := zeroNodeResources()
	if err := collector.chargeForeignPods(context.Background(), &res, map[podKey]bool{}); err != nil {
		t.Fatalf("chargeForeignPods: %v", err)
	}
	if res.cores["n1"] != 2 {
		t.Errorf("n1 cores = %d, want 2 (foreign pod's cpu request)", res.cores["n1"])
	}
	if res.hugepages["n1"] != 4096 {
		t.Errorf("n1 hugepages = %d, want 4096 (foreign pod's hugepages-2Mi request)", res.hugepages["n1"])
	}
	if res.memory["n1"] != 8192 {
		t.Errorf("n1 memory = %d, want 8192 (foreign pod's memory request)", res.memory["n1"])
	}
}

// TestChargeForeignPods_ExcludesWekaContainersOwnPod: a pod already charged as its WekaContainer (present
// in the charged set) must not be double-charged here as a plain foreign pod.
func TestChargeForeignPods_ExcludesWekaContainersOwnPod(t *testing.T) {
	ownPod := testPod("compute-1", "default", "n1", corev1.PodRunning, 8, 19572, 40000) // matches charged key below
	fakeClient := newInventoryTestClient(t, ownPod)
	collector := NewCollector(fakeClient)

	charged := map[podKey]bool{{Namespace: "default", Name: "compute-1"}: true}
	res := zeroNodeResources()
	if err := collector.chargeForeignPods(context.Background(), &res, charged); err != nil {
		t.Fatalf("chargeForeignPods: %v", err)
	}
	if res.cores["n1"] != 0 || res.hugepages["n1"] != 0 || res.memory["n1"] != 0 {
		t.Errorf("n1 = (cores %d, hugepages %d, memory %d), want all zero: pod already charged as its WekaContainer must not be double-counted",
			res.cores["n1"], res.hugepages["n1"], res.memory["n1"])
	}
}

// TestChargeForeignPods_OperatorLabeledNonWekaContainerPodIsCharged: exclusion is by exact pod identity
// (namespace+name), never by the "app.kubernetes.io/created-by: weka-operator" label — a CSI/envoy/etc.
// pod carrying that label but no matching WekaContainer must still be charged.
func TestChargeForeignPods_OperatorLabeledNonWekaContainerPodIsCharged(t *testing.T) {
	pod := testPod("csi-node-abc123", "kube-system", "n1", corev1.PodRunning, 1, 0, 512)
	pod.Labels = map[string]string{"app.kubernetes.io/created-by": "weka-operator"}
	fakeClient := newInventoryTestClient(t, pod)
	collector := NewCollector(fakeClient)

	res := zeroNodeResources()
	if err := collector.chargeForeignPods(context.Background(), &res, map[podKey]bool{}); err != nil {
		t.Fatalf("chargeForeignPods: %v", err)
	}
	if res.cores["n1"] != 1 || res.memory["n1"] != 512 {
		t.Errorf("n1 = (cores %d, memory %d), want (1, 512): a weka-operator-labeled pod with no matching WekaContainer must still be charged",
			res.cores["n1"], res.memory["n1"])
	}
}

// TestChargeForeignPods_SkipsUnscheduledAndTerminalPods: an unscheduled pod (no node to bucket under) and
// terminal-phase pods (Succeeded/Failed — resources already released) are never charged.
func TestChargeForeignPods_SkipsUnscheduledAndTerminalPods(t *testing.T) {
	unscheduled := testPod("pending-pod", "default", "", corev1.PodPending, 4, 8192, 16384)
	succeeded := testPod("job-done", "default", "n1", corev1.PodSucceeded, 4, 8192, 16384)
	failed := testPod("job-failed", "default", "n1", corev1.PodFailed, 4, 8192, 16384)
	fakeClient := newInventoryTestClient(t, unscheduled, succeeded, failed)
	collector := NewCollector(fakeClient)

	res := zeroNodeResources()
	if err := collector.chargeForeignPods(context.Background(), &res, map[podKey]bool{}); err != nil {
		t.Fatalf("chargeForeignPods: %v", err)
	}
	if len(res.cores) != 0 || len(res.hugepages) != 0 || len(res.memory) != 0 {
		t.Errorf("no pod may be charged to any node: Succeeded/Failed pods are terminal and the unscheduled pod has no NodeName to bucket under, got cores=%v hugepages=%v memory=%v",
			res.cores, res.hugepages, res.memory)
	}
}

// TestNodeInventory_PinnedAutoFullDrivesDriveContainerNoPodReducesHeadroom: a auto-full-drives drive container pinned via
// Spec.NodeAffinity but with no pod yet must still reduce headroom by its spec-derived footprint.
func TestNodeInventory_PinnedAutoFullDrivesDriveContainerNoPodReducesHeadroom(t *testing.T) {
	cons := testCons()
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600) // no corresponding pod object at all

	fakeClient := newInventoryTestClient(t, n1, &pinned)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	wantCPU := 64 - (6 + 1) // dedicated, non-HT (nil topo): numCores+1
	if n1cap.AllocatableCPU != wantCPU {
		t.Errorf("n1 AllocatableCPU = %d, want %d (pinned auto-full-drives drive container's spec cpu must be charged with no pod)", n1cap.AllocatableCPU, wantCPU)
	}
	wantHP := 65536 - 9600 // spec2MiHugepages: raw Spec.Hugepages
	if n1cap.AvailableHugepagesMiB != wantHP {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (pinned auto-full-drives drive container's spec hugepages must be charged with no pod)", n1cap.AvailableHugepagesMiB, wantHP)
	}
	// ComputeMemoryFootprintMiB(6, cons) = 8000 + 6*3000 = 26000; 524288 - 26000 = 498288.
	if want := 498288; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (pinned auto-full-drives drive container's spec memory must be charged with no pod)", n1cap.AvailableMemoryMiB, want)
	}
}

// TestFullDrivesInventory_PinnedAutoFullDrivesDriveContainerNoPodReducesHeadroom is FullDrivesInventory's
// counterpart to TestNodeInventory_PinnedAutoFullDrivesDriveContainerNoPodReducesHeadroom — both wrappers around
// aggregateContainerResources/chargeForeignPods must apply the same spec-derived charge.
func TestFullDrivesInventory_PinnedAutoFullDrivesDriveContainerNoPodReducesHeadroom(t *testing.T) {
	cons := testCons()
	n1 := driveNode(t, "n1", []domain.DriveEntry{{Serial: "d1", CapacityGiB: 20 * tib}})
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)

	fakeClient := newInventoryTestClient(t, n1, &pinned)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	wantHP := 65536 - 9600
	if n1cap.AvailableHugepagesMiB != wantHP {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (pinned auto-full-drives drive container's spec hugepages must be charged with no pod)", n1cap.AvailableHugepagesMiB, wantHP)
	}
	wantCPU := 64 - (6 + 1)
	if n1cap.AllocatableCPU != wantCPU {
		t.Errorf("n1 AllocatableCPU = %d, want %d", n1cap.AllocatableCPU, wantCPU)
	}
	// 524288 - ComputeMemoryFootprintMiB(6, cons) = 524288 - 26000 = 498288.
	if want := 498288; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d", n1cap.AvailableMemoryMiB, want)
	}
}

// TestNodeInventory_PinnedComputeContainerNoPodReducesHeadroom: a compute container pinned via
// Spec.NodeAffinity but with no pod at all must still reduce headroom by its spec-derived footprint —
// compute is charged from spec (aggregateContainerResources, weka.WekaContainerModeCompute branch), keyed
// on GetNodeAffinity() (which prefers Spec.NodeAffinity over Status.NodeAffinity), never from the pod.
func TestNodeInventory_PinnedComputeContainerNoPodReducesHeadroom(t *testing.T) {
	cons := testCons()
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	pinned := computeContainer("compute-1", "default", "n1", 8, 19572) // no corresponding pod object at all

	fakeClient := newInventoryTestClient(t, n1, &pinned)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	wantCPU := 64 - (8 + 1) // dedicated, non-HT (nil topo): numCores+1
	if n1cap.AllocatableCPU != wantCPU {
		t.Errorf("n1 AllocatableCPU = %d, want %d (pinned compute container's spec cpu must be charged with no pod)", n1cap.AllocatableCPU, wantCPU)
	}
	wantHP := 65536 - 19572 // spec2MiHugepages: raw Spec.Hugepages
	if n1cap.AvailableHugepagesMiB != wantHP {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (pinned compute container's spec hugepages must be charged with no pod)", n1cap.AvailableHugepagesMiB, wantHP)
	}
	// 524288 - ComputeMemoryFootprintMiB(8, cons) = 524288 - 32000 = 492288.
	if want := 492288; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (pinned compute container's spec memory must be charged with no pod)", n1cap.AvailableMemoryMiB, want)
	}
}

// TestFullDrivesInventory_PinnedComputeContainerNoPodReducesHeadroom is FullDrivesInventory's counterpart to
// TestNodeInventory_PinnedComputeContainerNoPodReducesHeadroom — both wrappers around
// aggregateContainerResources/chargeForeignPods must apply the same spec-derived compute charge, regardless
// of whether the container's pod exists or is bound to a node.
func TestFullDrivesInventory_PinnedComputeContainerNoPodReducesHeadroom(t *testing.T) {
	cons := testCons()
	n1 := driveNode(t, "n1", []domain.DriveEntry{{Serial: "d1", CapacityGiB: 20 * tib}})
	pinned := computeContainer("compute-1", "default", "n1", 8, 19572) // no corresponding pod object at all

	fakeClient := newInventoryTestClient(t, n1, &pinned)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	wantHP := 65536 - 19572
	if n1cap.AvailableHugepagesMiB != wantHP {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (pinned compute container's spec hugepages must be charged with no pod)", n1cap.AvailableHugepagesMiB, wantHP)
	}
	wantCPU := 64 - (8 + 1)
	if n1cap.AllocatableCPU != wantCPU {
		t.Errorf("n1 AllocatableCPU = %d, want %d", n1cap.AllocatableCPU, wantCPU)
	}
	// 524288 - ComputeMemoryFootprintMiB(8, cons) = 524288 - 32000 = 492288.
	if want := 492288; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d", n1cap.AvailableMemoryMiB, want)
	}
}

// TestNodeInventory_PinnedAutoFullDrivesDriveContainerUnscheduledPodStillChargedFromSpec: a auto-full-drives drive container
// whose pod exists but is not yet scheduled is still charged from spec — chargeForeignPods skips an
// unscheduled pod entirely, so the spec-based pass must pick it up.
func TestNodeInventory_PinnedAutoFullDrivesDriveContainerUnscheduledPodStillChargedFromSpec(t *testing.T) {
	cons := testCons()
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)
	unscheduledPod := testPod("drive-1", "default", "", corev1.PodPending, 3, 2000, 5000) // own pod, not yet scheduled

	fakeClient := newInventoryTestClient(t, n1, &pinned, unscheduledPod)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	// Must match spec-derived figures, not the unscheduled pod's requests (3 cores/2000MiB/5000MiB).
	wantCPU := 64 - (6 + 1)
	if n1cap.AllocatableCPU != wantCPU {
		t.Errorf("n1 AllocatableCPU = %d, want %d (unscheduled pod must not suppress the spec charge)", n1cap.AllocatableCPU, wantCPU)
	}
	wantHP := 65536 - 9600
	if n1cap.AvailableHugepagesMiB != wantHP {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (unscheduled pod must not suppress the spec charge)", n1cap.AvailableHugepagesMiB, wantHP)
	}
	// 524288 - ComputeMemoryFootprintMiB(6, cons) = 524288 - 26000 = 498288.
	if want := 498288; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (unscheduled pod must not suppress the spec charge)", n1cap.AvailableMemoryMiB, want)
	}
}

// TestNodeInventory_PinnedAutoFullDrivesDriveContainerScheduledIsChargedFromSpecNotPod pins the charge source for a
// auto-full-drives drive container whose pod is smaller than its spec — the state every pending growth passes through,
// since growth raises the spec and the pod only catches up when it is recreated. The charge must be the spec's
// alone, never spec+pod.
func TestNodeInventory_PinnedAutoFullDrivesDriveContainerScheduledIsChargedFromSpecNotPod(t *testing.T) {
	cons := testCons()
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	// Spec 6 cores / 9600MiB hugepages; pod still at the smaller pre-growth 3 cores / 2000MiB / 5000MiB.
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)
	scheduledPod := testPod("drive-1", "default", "n1", corev1.PodRunning, 3, 2000, 5000)

	fakeClient := newInventoryTestClient(t, n1, &pinned, scheduledPod)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	// CPURequestCores' CpuPolicyDedicated branch (spec has no CpuPolicy/HT) charges NumCores+1 = 6+1 = 7.
	if want := 64 - 7; n1cap.AllocatableCPU != want {
		t.Errorf("n1 AllocatableCPU = %d, want %d (spec's 7 cores, not the pod's 3 and not both)",
			n1cap.AllocatableCPU, want)
	}
	if want := 65536 - 9600; n1cap.AvailableHugepagesMiB != want {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (spec's 9600MiB, not the pod's 2000 and not both)",
			n1cap.AvailableHugepagesMiB, want)
	}
	// ComputeMemoryFootprintMiB: MemoryBaseMiB(8000) + NumCores(6)*MemoryPerCoreMiB(3000) = 26000.
	if want := 524288 - 26000; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (spec's 26000MiB, not the pod's 5000 and not both)",
			n1cap.AvailableMemoryMiB, want)
	}
}

// TestAggregateContainerResources_SkipsDeletingAutoFullDrivesDriveContainer: a deleting auto-full-drives
// drive container is never charged, via the shared IsMarkedForDeletion skip at the top of the loop —
// the same guard that applies to every other mode.
func TestAggregateContainerResources_SkipsDeletingAutoFullDrivesDriveContainer(t *testing.T) {
	cons := testCons()
	deleting := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	res, _ := aggregateContainerResources([]weka.WekaContainer{deleting}, cons, nil)

	if res.cores["n1"] != 0 || res.hugepages["n1"] != 0 || res.memory["n1"] != 0 {
		t.Errorf("n1 = (cores %d, hugepages %d, memory %d), want all zero: a deleting container must not be charged",
			res.cores["n1"], res.hugepages["n1"], res.memory["n1"])
	}
}

// TestAggregateContainerResources_AutoFullDrivesDriveContainerSkipsEmptyNodeAffinity proves an auto-full-drives
// drive container with no NodeAffinity (not yet pinned to any node) is charged nowhere — it must not panic
// (there is no node key to bucket a charge under) and must not create a phantom "" entry in any resource map.
func TestAggregateContainerResources_AutoFullDrivesDriveContainerSkipsEmptyNodeAffinity(t *testing.T) {
	cons := testCons()
	unpinned := autoFullDrivesDriveContainer("drive-1", "default", "", 6, 9600) // no NodeAffinity

	res, _ := aggregateContainerResources([]weka.WekaContainer{unpinned}, cons, nil)

	if _, ok := res.cores[""]; ok {
		t.Errorf("res.cores has a phantom entry for node \"\": %v", res.cores)
	}
	if _, ok := res.hugepages[""]; ok {
		t.Errorf("res.hugepages has a phantom entry for node \"\": %v", res.hugepages)
	}
	if _, ok := res.memory[""]; ok {
		t.Errorf("res.memory has a phantom entry for node \"\": %v", res.memory)
	}
}

// TestAggregateContainerResources_SharingDriveContainerUnchangedByAutoFullDrivesFix verifies the auto-full-drives spec-charge
// fix left the drive-sharing path untouched: hugepages/memory still come from RequiredDriveResources
// (capacity-derived), never from the auto-full-drives-only spec2MiHugepages/ComputeMemoryFootprintMiB formula.
func TestAggregateContainerResources_SharingDriveContainerUnchangedByAutoFullDrivesFix(t *testing.T) {
	cons := testCons()
	sharing := ownedDriveContainer("cluster-me", "n1", 100*tib, 1, 4) // tlc=20TiB, qlc=80TiB
	sharing.Spec.NumCores = 6

	res, charged := aggregateContainerResources([]weka.WekaContainer{sharing}, cons, nil)

	// numDrives is 0 here: ownedDriveContainer sets ContainerCapacity, and CEL makes numDrives mutually
	// exclusive with it — so this mode takes the per-core-only branch and is unaffected by the drive term.
	// RequiredDriveCores(20TiB, 80TiB, cons) = ceil(20480/5120) + ceil(81920/51200) = 4 + 2 = 6 cores.
	// hugepages = 6 * HugepagesPerCoreMiB(1600) = 9600. memory = 8000 + 6*3000 = 26000.
	if want := 9600; res.hugepages["n1"] != want {
		t.Errorf("n1 hugepages = %d, want %d (capacity-derived via RequiredDriveResources, unchanged)", res.hugepages["n1"], want)
	}
	if want := 26000; res.memory["n1"] != want {
		t.Errorf("n1 memory = %d, want %d (capacity-derived via RequiredDriveResources, unchanged)", res.memory["n1"], want)
	}
	// A sharing container's pod key is returned as charged, so the auto-full-drives third pass never touches it.
	if !charged[containerPodKey(&sharing)] {
		t.Errorf("sharing container's pod key must be present in charged (unchanged aggregateContainerResources behavior)")
	}
}

// TestAggregateContainerResources_NumDrivesDriveCapacityChargesPerDrive pins the numDrives+driveCapacity
// charge to what that mode's pod actually requests. Its pod gets 1400/core + 200/drive
// (allocator.CalculateDriveHugepages via template.NumDrives), so a per-core-only charge under-reserved the
// node by 200*(numDrives-cores) MiB and the planner grew into headroom that was never free.
func TestAggregateContainerResources_NumDrivesDriveCapacityChargesPerDrive(t *testing.T) {
	cons := testCons()
	cons.DriveDpdkPerCoreMiB = 64

	// 6 drives x 3500 GiB = 21000 GiB TLC -> 5 cores at 5120 GiB/core.
	c := weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.DriveCapacity = 3500
	c.Spec.NumDrives = 6
	c.Spec.NumCores = 5
	c.Spec.NodeAffinity = weka.NodeName("n1")
	c.OwnerReferences = []metav1.OwnerReference{{Kind: "WekaCluster", UID: types.UID("cluster-me")}}

	res, _ := aggregateContainerResources([]weka.WekaContainer{c}, cons, nil)

	if want := 5*(1400+64) + 6*200; res.hugepages["n1"] != want {
		t.Errorf("n1 hugepages = %d, want %d (1400/core + 200/drive + DPDK/core, matching the pod)", res.hugepages["n1"], want)
	}
	// The bug this guards: the per-core-only figure is 200*(6-5) MiB short.
	if perCoreOnly := 5 * (1600 + 64); res.hugepages["n1"] == perCoreOnly {
		t.Errorf("n1 hugepages charged the per-core-only figure %d, omitting the drive term", perCoreOnly)
	}
}

// TestEffectivePodResourceRequests_SidecarAndInitContainerSemantics verifies the k8s-accurate effective
// pod request algorithm: per resource, max(sum(regular containers)+sum(sidecar/RestartPolicy:Always init
// containers), max(any single non-sidecar init container)) — NOT a naive flat sum of every container.
func TestEffectivePodResourceRequests_SidecarAndInitContainerSemantics(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				}}},
			},
			InitContainers: []corev1.Container{
				// Sidecar (RestartPolicy: Always): runs alongside main, so its request stacks.
				{Name: "sidecar", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				}}},
				// Regular init container: runs before main/sidecar, so only its peak counts — cpu(5) exceeds
				// main+sidecar(3) so cpu picks 5, but memory(1Gi) is less than main+sidecar(3Gi) so memory stays 3Gi.
				{Name: "init-heavy", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("5"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				}}},
			},
		},
	}
	cpu, hp, mem := effectivePodResourceRequests(pod)
	if cpu != 5 {
		t.Errorf("cpu = %d, want 5 (max(main+sidecar=3, init-heavy peak=5))", cpu)
	}
	if mem != 3072 {
		t.Errorf("memory = %d MiB, want 3072 (max(main+sidecar=3072MiB, init-heavy peak=1024MiB))", mem)
	}
	if hp != 0 {
		t.Errorf("hugepages = %d, want 0 (none requested)", hp)
	}

	// Sanity guard: a naive flat sum (2+1+5=8) must not equal the correct result, or this test wouldn't discriminate.
	if naiveSum := 2 + 1 + 5; cpu == naiveSum {
		t.Fatalf("test is not discriminating: naive flat sum (%d) coincides with the correct result (%d)", naiveSum, cpu)
	}
}

// TestEffectivePodResourceRequests_ExcludesHugepages1Gi: hugepages-1Gi is a distinct, untracked pool and
// must never leak into the hugepages-2Mi figure charged against a node.
func TestEffectivePodResourceRequests_ExcludesHugepages1Gi(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:                   resource.MustParse("1"),
					corev1.ResourceName("hugepages-2Mi"): resource.MustParse("512Mi"),
					corev1.ResourceName("hugepages-1Gi"): resource.MustParse("4Gi"),
				}}},
			},
		},
	}
	_, hp, _ := effectivePodResourceRequests(pod)
	if hp != 512 {
		t.Errorf("hugepages-2Mi = %d MiB, want 512 (hugepages-1Gi's 4Gi must be excluded — distinct pool)", hp)
	}
}

// TestNodeInventory_ForeignPodReducesHeadroom is the end-to-end test proving a foreign pod's charge reaches
// NodeInventory's reported headroom, not just aggregateContainerResources' internal totals.
func TestNodeInventory_ForeignPodReducesHeadroom(t *testing.T) {
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	foreign := testPod("foreign", "default", "n1", corev1.PodRunning, 4, 40000, 100000)

	fakeClient := newInventoryTestClient(t, n1, foreign)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, testCons())
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	if want := 65536 - 40000; n1cap.AvailableHugepagesMiB != want {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (foreign pod's 40000MiB hugepages must be subtracted from the 65536MiB allocatable)", n1cap.AvailableHugepagesMiB, want)
	}
	if want := 64 - 4; n1cap.AllocatableCPU != want {
		t.Errorf("n1 AllocatableCPU = %d, want %d (foreign pod's 4 cores must be subtracted)", n1cap.AllocatableCPU, want)
	}
	if want := 524288 - 100000; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (foreign pod's 100000MiB memory must be subtracted)", n1cap.AvailableMemoryMiB, want)
	}
}

// TestFullDrivesInventory_ForeignPodReducesHeadroom is FullDrivesInventory's (auto-full-drives path) counterpart to
// TestNodeInventory_ForeignPodReducesHeadroom — the duplicated headroom closure must apply the same foreign-pod charge.
func TestFullDrivesInventory_ForeignPodReducesHeadroom(t *testing.T) {
	n1 := driveNode(t, "n1", []domain.DriveEntry{{Serial: "d1", CapacityGiB: 20 * tib}})
	foreign := testPod("foreign", "default", "n1", corev1.PodRunning, 4, 40000, 100000)

	fakeClient := newInventoryTestClient(t, n1, foreign)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.FullDrivesInventory(context.Background(), cluster, nil, testCons())
	if err != nil {
		t.Fatalf("FullDrivesInventory: %v", err)
	}
	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	if want := 65536 - 40000; n1cap.AvailableHugepagesMiB != want {
		t.Errorf("n1 AvailableHugepagesMiB = %d, want %d (foreign pod's 40000MiB hugepages must be subtracted)", n1cap.AvailableHugepagesMiB, want)
	}
	if want := 64 - 4; n1cap.AllocatableCPU != want {
		t.Errorf("n1 AllocatableCPU = %d, want %d (foreign pod's 4 cores must be subtracted)", n1cap.AllocatableCPU, want)
	}
	if want := 524288 - 100000; n1cap.AvailableMemoryMiB != want {
		t.Errorf("n1 AvailableMemoryMiB = %d, want %d (foreign pod's 100000MiB memory must be subtracted)", n1cap.AvailableMemoryMiB, want)
	}
}

// computeOnlyNode builds a Ready node with CPU/memory/hugepages allocatable but no drive annotation, so it
// can only ever be a compute candidate.
func computeOnlyNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Conditions: readyNodeStatus().Conditions,
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                   resource.MustParse("32"),
				corev1.ResourceMemory:                resource.MustParse("256Gi"),
				corev1.ResourceName("hugepages-2Mi"): resource.MustParse("32Gi"),
			},
		},
	}
}

// TestInventory_SkipsFDSkippedNodes covers the failure-domain filter: a node matching the role selector
// but carrying no FD label belongs to no failure domain, so it must not reach the inventory at all — while
// a sibling node that does resolve an FD key still has its foreign pod charged.
func TestInventory_SkipsFDSkippedNodes(t *testing.T) {
	const rack = "topology.kubernetes.io/rack"
	sel := map[string]string{"role": "drive"}
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	n1.Labels = map[string]string{"role": "drive", rack: "rack-1"}
	n2 := sharedDrivesNode(t, "n2", []domain.SharedDriveInfo{{Serial: "s2", CapacityGiB: 20 * tib, Type: "TLC"}})
	n2.Labels = map[string]string{"role": "drive"} // no FD label: resolveInventoryFDValue skips it
	onN1 := testPod("on-n1", "default", "n1", corev1.PodRunning, 4, 40000, 100000)
	onN2 := testPod("on-n2", "default", "n2", corev1.PodRunning, 8, 80000, 200000)

	fakeClient := newInventoryTestClient(t, n1, n2, onN1, onN2)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	cluster.Spec.NodeSelector = sel
	cluster.Spec.FailureDomain = &weka.FailureDomain{Label: strPtr(rack)}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, testCons())
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}

	if _, ok := invByName(inv)["n2"]; ok {
		t.Errorf("FD-skipped node n2 must not appear in the inventory")
	}
	if want := 64 - 4; invByName(inv)["n1"].AllocatableCPU != want {
		t.Errorf("n1 AllocatableCPU = %d, want %d", invByName(inv)["n1"].AllocatableCPU, want)
	}
}

// TestInventory_ExcludesNodesOutsideSelectors: a node matching neither role selector never enters the
// inventory, and the foreign pod sitting on it never reduces any candidate node's headroom.
func TestInventory_ExcludesNodesOutsideSelectors(t *testing.T) {
	labels := map[string]string{"role": "drive"}
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	n1.Labels = labels
	outside := computeOnlyNode("outside", map[string]string{"role": "other"})
	onCandidate := testPod("on-candidate", "default", "n1", corev1.PodRunning, 4, 40000, 100000)
	onOutside := testPod("on-outside", "default", "outside", corev1.PodRunning, 8, 80000, 200000)

	fakeClient := newInventoryTestClient(t, n1, outside, onCandidate, onOutside)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	cluster.Spec.NodeSelector = labels
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, testCons())
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}

	n1cap, ok := invByName(inv)["n1"]
	if !ok {
		t.Fatalf("n1 missing from inventory")
	}
	if want := 64 - 4; n1cap.AllocatableCPU != want {
		t.Errorf("n1 AllocatableCPU = %d, want %d (only the on-candidate pod's 4 cores should be charged)", n1cap.AllocatableCPU, want)
	}
	if _, ok := invByName(inv)["outside"]; ok {
		t.Errorf("node outside the drive/compute selectors must not appear in the inventory")
	}
}

// TestChargeForeignPods_ComputeOnlyNodeCharged verifies the union case in listRoleNodesAndTopos: when the
// drive and compute selectors differ, a compute-only node (matched only by the compute selector) still
// gets its foreign pods charged, not just the drive-selector nodes.
func TestChargeForeignPods_ComputeOnlyNodeCharged(t *testing.T) {
	driveSel := map[string]string{"role": "drive"}
	computeSel := map[string]string{"role": "compute"}
	n1 := sharedDrivesNode(t, "n1", []domain.SharedDriveInfo{{Serial: "s1", CapacityGiB: 20 * tib, Type: "TLC"}})
	n1.Labels = driveSel
	n2 := computeOnlyNode("n2", computeSel)
	foreign := testPod("foreign", "default", "n2", corev1.PodRunning, 6, 0, 0)

	fakeClient := newInventoryTestClient(t, n1, n2, foreign)
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	cluster.Spec.RoleNodeSelector = weka.RoleNodeSelector{Drive: &driveSel, Compute: &computeSel}
	collector := NewCollector(fakeClient)

	_, inv, _, err := collector.NodeInventory(context.Background(), cluster, nil, testCons())
	if err != nil {
		t.Fatalf("NodeInventory: %v", err)
	}
	n2cap, ok := invByName(inv)["n2"]
	if !ok {
		t.Fatalf("n2 (compute-only) missing from inventory")
	}
	if want := 32 - 6; n2cap.AllocatableCPU != want {
		t.Errorf("n2 AllocatableCPU = %d, want %d (compute-only node's foreign pod must still be charged)", n2cap.AllocatableCPU, want)
	}
}

// equalInts compares two []int slices for exact, order-sensitive equality.
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		if byName["driveOnly"].TlcGiB != 50*tib || eligible["driveOnly"] {
			t.Errorf("driveOnly should keep capacity and be compute-ineligible, got cap=%d eligible=%v", byName["driveOnly"].TlcGiB, eligible["driveOnly"])
		}
		if byName["shared"].TlcGiB != 50*tib || !eligible["shared"] {
			t.Errorf("shared should keep capacity and be compute-eligible, got cap=%d eligible=%v", byName["shared"].TlcGiB, eligible["shared"])
		}
		// computeOnly is appended diskless (no drive-side entry) but still compute-eligible.
		if byName["computeOnly"].TlcGiB != 0 || byName["computeOnly"].QlcGiB != 0 || !eligible["computeOnly"] {
			t.Errorf("computeOnly should be diskless and compute-eligible, got tlc=%d qlc=%d eligible=%v", byName["computeOnly"].TlcGiB, byName["computeOnly"].QlcGiB, eligible["computeOnly"])
		}
	})
}

// TestSpec2MiHugepages_HonorsNamedResources covers spec.resources naming hugepages-2Mi outright.
// The planner has to charge what the POD actually requests, otherwise it reserves one amount
// while the scheduler is handed another and the node silently ends up over- or under-subscribed.
func TestSpec2MiHugepages_HonorsNamedResources(t *testing.T) {
	withResources := func(hugepages int, size string, r *weka.PodResourcesSpec) *weka.WekaContainer {
		c := &weka.WekaContainer{}
		c.Spec.Mode = weka.WekaContainerModeClient
		c.Spec.Hugepages = hugepages
		c.Spec.HugepagesSize = size
		c.Spec.Resources = r
		return c
	}
	q := func(s string) *weka.PodResourcesSpec {
		return &weka.PodResourcesSpec{
			Requests: weka.PodResources{Hugepages2Mi: resource.MustParse(s)},
		}
	}

	cases := []struct {
		name string
		c    *weka.WekaContainer
		want int
	}{
		{"no resources falls back to spec.hugepages", withResources(3000, "", nil), 3000},
		{"empty resources falls back to spec.hugepages", withResources(3000, "", &weka.PodResourcesSpec{}), 3000},
		{"named resources win over spec.hugepages", withResources(3000, "", q("2Gi")), 2048},
		{"named resources charged on a 1Gi container too", withResources(4096, "1Gi", q("2Gi")), 2048},
		{"1Gi container without an override charges no 2Mi", withResources(4096, "1Gi", nil), 0},
		{"limits side alone is charged", withResources(0, "", &weka.PodResourcesSpec{
			Limits: weka.PodResources{Hugepages2Mi: resource.MustParse("1Gi")},
		}), 1024},
		{"drivers-dist with no sizing of its own charges nothing", withResources(0, "", nil), 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spec2MiHugepages(tc.c); got != tc.want {
				t.Errorf("spec2MiHugepages = %d, want %d", got, tc.want)
			}
		})
	}
}

// nodeDetailByName indexes ExploreNodes' output by node name for assertions.
func nodeDetailByName(nodes []NodeDetail) map[string]NodeDetail {
	m := make(map[string]NodeDetail, len(nodes))
	for _, n := range nodes {
		m[n.Node] = n
	}
	return m
}

// TestExploreNodes_SharedDrivesNode_ModeSharedAndUnchangedFields: a node signed only for
// weka-shared-drives must report Mode == "shared", keep its Phys/FreeTlc/Qlc fields as before, and leave
// every full-drives field (FreeFullDriveCount, PhysFullTlcGiB, etc.) zero — the two modes are mutually
// exclusive per allocator.ParseAllocatorNodeInfo.
func TestExploreNodes_SharedDrivesNode_ModeSharedAndUnchangedFields(t *testing.T) {
	drives := []domain.SharedDriveInfo{
		{PhysicalUUID: "u1", Serial: "s1", CapacityGiB: 1000, Type: "TLC"},
		{PhysicalUUID: "u2", Serial: "s2", CapacityGiB: 2000, Type: "QLC"},
	}
	n1 := sharedDrivesNode(t, "n1", drives)
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, nil)

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.Mode != "shared" {
		t.Errorf("n1 Mode = %q, want %q", n1detail.Mode, "shared")
	}
	if n1detail.PhysTlcGiB != 1000 || n1detail.FreeTlcGiB != 1000 {
		t.Errorf("n1 Phys/FreeTlcGiB = %d/%d, want 1000/1000 (no consumers)", n1detail.PhysTlcGiB, n1detail.FreeTlcGiB)
	}
	if n1detail.PhysQlcGiB != 2000 || n1detail.FreeQlcGiB != 2000 {
		t.Errorf("n1 Phys/FreeQlcGiB = %d/%d, want 2000/2000 (no consumers)", n1detail.PhysQlcGiB, n1detail.FreeQlcGiB)
	}
	if !n1detail.IsDriveCandidate {
		t.Errorf("n1 IsDriveCandidate = false, want true (shared-drives node)")
	}
	if n1detail.FreeFullDriveCount != 0 || n1detail.PhysFullDriveCount != 0 || n1detail.FreeFullTlcGiB != 0 || n1detail.PhysFullTlcGiB != 0 {
		t.Errorf("n1 full-drives fields not zero: free=%d phys=%d freeTlc=%d physTlc=%d",
			n1detail.FreeFullDriveCount, n1detail.PhysFullDriveCount, n1detail.FreeFullTlcGiB, n1detail.PhysFullTlcGiB)
	}
	if n1detail.BlockedFullDriveCount != 0 {
		t.Errorf("n1 BlockedFullDriveCount = %d, want 0 (no weka-full-drives annotation at all)", n1detail.BlockedFullDriveCount)
	}
	if n1detail.FreeFullDriveCapacitiesGiB != nil || n1detail.ClaimedFullDriveCapacitiesGiB != nil {
		t.Errorf("n1 per-drive capacity fields = %v / %v, want nil/nil (shared-drives node, full-drives fields must stay empty)",
			n1detail.FreeFullDriveCapacitiesGiB, n1detail.ClaimedFullDriveCapacitiesGiB)
	}
}

// TestExploreNodes_FullDrivesNode_PartialAllocation_ModeFull covers a full-drives (auto-full-drives) node with a mix
// of claimed and free signed drives: Mode == "full", counts/capacities are free-vs-total (total = free +
// allocated), and the shared-drives Phys/FreeTlc/Qlc fields stay unpopulated.
func TestExploreNodes_FullDrivesNode_PartialAllocation_ModeFull(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "d1", CapacityGiB: 1000}, {Serial: "d2", CapacityGiB: 1000}, {Serial: "d3", CapacityGiB: 1000},
		{Serial: "d4", CapacityGiB: 1000}, {Serial: "d5", CapacityGiB: 1000}, {Serial: "d6", CapacityGiB: 1000},
	}
	n1 := driveNode(t, "n1", entries)
	// ExploreNodes is cluster-agnostic reporting (see allocatedNodeDrives), so any cluster's claim counts.
	claimed := allocatedDriveContainer("existing-n1", "n1", []string{"d1", "d2", "d3"})
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&claimed})

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.Mode != "full" {
		t.Errorf("n1 Mode = %q, want %q", n1detail.Mode, "full")
	}
	if n1detail.PhysFullDriveCount != 6 {
		t.Errorf("n1 PhysFullDriveCount = %d, want 6 (total signed = free + allocated)", n1detail.PhysFullDriveCount)
	}
	if n1detail.FreeFullDriveCount != 3 {
		t.Errorf("n1 FreeFullDriveCount = %d, want 3 (6 signed - 3 claimed)", n1detail.FreeFullDriveCount)
	}
	if n1detail.PhysFullTlcGiB != 6000 {
		t.Errorf("n1 PhysFullTlcGiB = %d, want 6000", n1detail.PhysFullTlcGiB)
	}
	if n1detail.FreeFullTlcGiB != 3000 {
		t.Errorf("n1 FreeFullTlcGiB = %d, want 3000", n1detail.FreeFullTlcGiB)
	}
	if n1detail.PhysTlcGiB != 0 || n1detail.PhysQlcGiB != 0 || n1detail.FreeTlcGiB != 0 || n1detail.FreeQlcGiB != 0 {
		t.Errorf("n1 shared-drives fields must stay 0 for a full-drives node, got Phys=%d/%d Free=%d/%d",
			n1detail.PhysTlcGiB, n1detail.PhysQlcGiB, n1detail.FreeTlcGiB, n1detail.FreeQlcGiB)
	}
	if want := []int{1000, 1000, 1000}; !equalInts(n1detail.FreeFullDriveCapacitiesGiB, want) {
		t.Errorf("n1 FreeFullDriveCapacitiesGiB = %v, want %v", n1detail.FreeFullDriveCapacitiesGiB, want)
	}
	if want := []int{1000, 1000, 1000}; !equalInts(n1detail.ClaimedFullDriveCapacitiesGiB, want) {
		t.Errorf("n1 ClaimedFullDriveCapacitiesGiB = %v, want %v", n1detail.ClaimedFullDriveCapacitiesGiB, want)
	}
}

// TestExploreNodes_HeterogeneousFullDrivesNode_PerDriveCapacitiesLargestFirstFreeVsClaimed covers a
// heterogeneous full-drives node (mixed sizes, signed in arbitrary order, mirroring the real fleet's
// h6-8-a/h6-9-b shape) with partial allocation: FreeFullDriveCapacitiesGiB and ClaimedFullDriveCapacitiesGiB
// must each be sorted largest-first and correctly split into disjoint free/claimed subsets.
func TestExploreNodes_HeterogeneousFullDrivesNode_PerDriveCapacitiesLargestFirstFreeVsClaimed(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "d1", CapacityGiB: 7153}, {Serial: "d2", CapacityGiB: 14307}, {Serial: "d3", CapacityGiB: 14307},
		{Serial: "d4", CapacityGiB: 7153}, {Serial: "d5", CapacityGiB: 14307}, {Serial: "d6", CapacityGiB: 14307},
	}
	n1 := driveNode(t, "n1", entries)
	claimed := allocatedDriveContainer("existing-n1", "n1", []string{"d1", "d2"})
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&claimed})

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.Mode != "full" {
		t.Errorf("n1 Mode = %q, want %q", n1detail.Mode, "full")
	}
	wantFree := []int{14307, 14307, 14307, 7153}
	if !equalInts(n1detail.FreeFullDriveCapacitiesGiB, wantFree) {
		t.Errorf("n1 FreeFullDriveCapacitiesGiB = %v, want %v (largest-first: d3,d5,d6,d4)", n1detail.FreeFullDriveCapacitiesGiB, wantFree)
	}
	wantClaimed := []int{14307, 7153}
	if !equalInts(n1detail.ClaimedFullDriveCapacitiesGiB, wantClaimed) {
		t.Errorf("n1 ClaimedFullDriveCapacitiesGiB = %v, want %v (largest-first: d2,d1)", n1detail.ClaimedFullDriveCapacitiesGiB, wantClaimed)
	}
	if n1detail.FreeFullDriveCount != 4 || n1detail.PhysFullDriveCount != 6 {
		t.Errorf("n1 Free/PhysFullDriveCount = %d/%d, want 4/6", n1detail.FreeFullDriveCount, n1detail.PhysFullDriveCount)
	}
}

// TestExploreNodes_FullyClaimedFullDrivesNode_StillAppears: a node whose signed full drives are entirely
// claimed (zero free) must still appear with Mode == "full" and PhysFullDriveCount == 6, never vanishing —
// unlike FullDrivesInventory, which skips such nodes for planning purposes.
func TestExploreNodes_FullyClaimedFullDrivesNode_StillAppears(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "d1", CapacityGiB: 1000}, {Serial: "d2", CapacityGiB: 1000}, {Serial: "d3", CapacityGiB: 1000},
		{Serial: "d4", CapacityGiB: 1000}, {Serial: "d5", CapacityGiB: 1000}, {Serial: "d6", CapacityGiB: 1000},
	}
	n1 := driveNode(t, "n1", entries)
	claimed := allocatedDriveContainer("existing-n1", "n1", domain.DriveEntrySerials(entries))
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&claimed})

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 (fully-claimed full-drives node) must still appear in ExploreNodes output, but is missing")
	}
	if n1detail.Mode != "full" {
		t.Errorf("n1 Mode = %q, want %q (fully-claimed node is still a signed full-drives node)", n1detail.Mode, "full")
	}
	if n1detail.FreeFullDriveCount != 0 {
		t.Errorf("n1 FreeFullDriveCount = %d, want 0 (all 6 claimed)", n1detail.FreeFullDriveCount)
	}
	if n1detail.PhysFullDriveCount != 6 {
		t.Errorf("n1 PhysFullDriveCount = %d, want 6 (total signed, not just free)", n1detail.PhysFullDriveCount)
	}
	if n1detail.FreeFullTlcGiB != 0 {
		t.Errorf("n1 FreeFullTlcGiB = %d, want 0", n1detail.FreeFullTlcGiB)
	}
	if n1detail.PhysFullTlcGiB != 6000 {
		t.Errorf("n1 PhysFullTlcGiB = %d, want 6000", n1detail.PhysFullTlcGiB)
	}
	if len(n1detail.FreeFullDriveCapacitiesGiB) != 0 {
		t.Errorf("n1 FreeFullDriveCapacitiesGiB = %v, want empty (all 6 claimed)", n1detail.FreeFullDriveCapacitiesGiB)
	}
	if want := []int{1000, 1000, 1000, 1000, 1000, 1000}; !equalInts(n1detail.ClaimedFullDriveCapacitiesGiB, want) {
		t.Errorf("n1 ClaimedFullDriveCapacitiesGiB = %v, want %v", n1detail.ClaimedFullDriveCapacitiesGiB, want)
	}
}

// TestExploreNodes_BlockedDrivesAnnotation_SurfacesCount: drives listed in weka.io/blocked-drives are
// already excluded from Phys/FreeFullDriveCount by allocator.ParseAllocatorNodeInfo (node_info.go); this
// confirms BlockedFullDriveCount still surfaces their count for visibility, without perturbing the totals.
func TestExploreNodes_BlockedDrivesAnnotation_SurfacesCount(t *testing.T) {
	entries := []domain.DriveEntry{
		{Serial: "d1", CapacityGiB: 1000}, {Serial: "d2", CapacityGiB: 1000}, {Serial: "d3", CapacityGiB: 1000},
		{Serial: "d4", CapacityGiB: 1000}, {Serial: "d5", CapacityGiB: 1000},
	}
	n1 := driveNode(t, "n1", entries)
	blocked, err := json.Marshal([]string{"d4", "d5"})
	if err != nil {
		t.Fatalf("marshal blocked-drives annotation: %v", err)
	}
	n1.Annotations[consts.AnnotationBlockedDrives] = string(blocked)
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, nil)

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.Mode != "full" {
		t.Errorf("n1 Mode = %q, want %q", n1detail.Mode, "full")
	}
	if n1detail.PhysFullDriveCount != 3 {
		t.Errorf("n1 PhysFullDriveCount = %d, want 3 (5 signed - 2 blocked, already excluded by ParseAllocatorNodeInfo)", n1detail.PhysFullDriveCount)
	}
	if n1detail.FreeFullDriveCount != 3 {
		t.Errorf("n1 FreeFullDriveCount = %d, want 3 (none of the 3 non-blocked drives are claimed)", n1detail.FreeFullDriveCount)
	}
	if n1detail.BlockedFullDriveCount != 2 {
		t.Errorf("n1 BlockedFullDriveCount = %d, want 2 (d4, d5)", n1detail.BlockedFullDriveCount)
	}
}

// TestExploreNodes_ComputeOnlyNode_ModeDash: a pure-compute node (no drive annotations) must report
// Mode == "-", never defaulting to one of the drive modes.
func TestExploreNodes_ComputeOnlyNode_ModeDash(t *testing.T) {
	n1 := nodeNamed("n1", nil)
	n1.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU:                   resource.MustParse("64"),
		corev1.ResourceMemory:                resource.MustParse("512Gi"),
		corev1.ResourceName("hugepages-2Mi"): resource.MustParse("64Gi"),
	}
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, nil)

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, testCons())
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	byName := nodeDetailByName(details)
	n1detail, ok := byName["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.Mode != "-" {
		t.Errorf("n1 Mode = %q, want %q (pure compute node)", n1detail.Mode, "-")
	}
	if n1detail.IsDriveCandidate {
		t.Errorf("n1 IsDriveCandidate = true, want false (no drive annotations)")
	}
}

// TestExploreNodes_PinnedAutoFullDrivesDriveContainer_UsedPlusFreeEqualsAllocatable verifies ExploreNodes charges
// a full-drives node's own pinned drive container via aggregateContainerResources, so Used+Free equals
// Allocatable and the container appears in Consumers.
func TestExploreNodes_PinnedAutoFullDrivesDriveContainer_UsedPlusFreeEqualsAllocatable(t *testing.T) {
	cons := testCons()
	n1 := driveNode(t, "n1", []domain.DriveEntry{{Serial: "d1", CapacityGiB: 20 * tib}})
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&pinned})

	collector := NewCollector(fakeClient)
	details, err := collector.ExploreNodes(context.Background(), nil, nil, cons)
	if err != nil {
		t.Fatalf("ExploreNodes: %v", err)
	}
	n1detail, ok := nodeDetailByName(details)["n1"]
	if !ok {
		t.Fatalf("n1 missing from ExploreNodes output")
	}
	if n1detail.UsedCores == 0 {
		t.Errorf("n1 UsedCores = 0, want > 0: the pinned drive container's spec cores must be charged")
	}
	if got, want := n1detail.UsedCores+n1detail.FreeCores, n1detail.AllocatableCores; got != want {
		t.Errorf("n1 UsedCores(%d)+FreeCores(%d) = %d, want AllocatableCores = %d", n1detail.UsedCores, n1detail.FreeCores, got, want)
	}
	if got, want := n1detail.UsedHugepagesMiB+n1detail.FreeHugepagesMiB, n1detail.AllocatableHugepagesMiB; got != want {
		t.Errorf("n1 UsedHugepagesMiB(%d)+FreeHugepagesMiB(%d) = %d, want AllocatableHugepagesMiB = %d", n1detail.UsedHugepagesMiB, n1detail.FreeHugepagesMiB, got, want)
	}
	if got, want := n1detail.UsedMemoryMiB+n1detail.FreeMemoryMiB, n1detail.AllocatableMemoryMiB; got != want {
		t.Errorf("n1 UsedMemoryMiB(%d)+FreeMemoryMiB(%d) = %d, want AllocatableMemoryMiB = %d", n1detail.UsedMemoryMiB, n1detail.FreeMemoryMiB, got, want)
	}
	if len(n1detail.Consumers) != 1 {
		t.Fatalf("n1 Consumers = %v, want exactly 1 (the pinned drive container)", n1detail.Consumers)
	}
	consumer := n1detail.Consumers[0]
	if consumer.Cores == 0 || consumer.HugepagesMiB == 0 || consumer.MemoryMiB == 0 {
		t.Errorf("n1 Consumer = %+v, want non-zero Cores/HugepagesMiB/MemoryMiB (spec-derived footprint, not an empty row)", consumer)
	}
	if consumer.TlcGiB != 0 || consumer.QlcGiB != 0 {
		t.Errorf("n1 Consumer TlcGiB/QlcGiB = %d/%d, want 0/0: capacity comes via the node's own-drive split, not RequiredDriveResources", consumer.TlcGiB, consumer.QlcGiB)
	}
}

// TestCollect_PinnedAutoFullDrivesDriveContainer_NodeDetailsUsedPlusFreeEqualsAllocatable is
// nodeDetailsFromLists' counterpart to the ExploreNodes test above, sharing the same direct
// aggregateContainerResources call.
func TestCollect_PinnedAutoFullDrivesDriveContainer_NodeDetailsUsedPlusFreeEqualsAllocatable(t *testing.T) {
	cons := testCons()
	n1 := driveNode(t, "n1", []domain.DriveEntry{{Serial: "d1", CapacityGiB: 20 * tib}})
	pinned := autoFullDrivesDriveContainer("drive-1", "default", "n1", 6, 9600)
	fakeClient := newFullDrivesTestClient(t, []*corev1.Node{n1}, []*weka.WekaContainer{&pinned})
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-me"}}
	collector := NewCollector(fakeClient)

	result, err := collector.Collect(context.Background(), cluster, nil, cons)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	n1detail, ok := nodeDetailByName(result.Nodes)["n1"]
	if !ok {
		t.Fatalf("n1 missing from Collect's node details")
	}
	if n1detail.UsedCores == 0 {
		t.Errorf("n1 UsedCores = 0, want > 0: the pinned drive container's spec cores must be charged")
	}
	if got, want := n1detail.UsedCores+n1detail.FreeCores, n1detail.AllocatableCores; got != want {
		t.Errorf("n1 UsedCores(%d)+FreeCores(%d) = %d, want AllocatableCores = %d", n1detail.UsedCores, n1detail.FreeCores, got, want)
	}
	if got, want := n1detail.UsedHugepagesMiB+n1detail.FreeHugepagesMiB, n1detail.AllocatableHugepagesMiB; got != want {
		t.Errorf("n1 UsedHugepagesMiB(%d)+FreeHugepagesMiB(%d) = %d, want AllocatableHugepagesMiB = %d", n1detail.UsedHugepagesMiB, n1detail.FreeHugepagesMiB, got, want)
	}
	if got, want := n1detail.UsedMemoryMiB+n1detail.FreeMemoryMiB, n1detail.AllocatableMemoryMiB; got != want {
		t.Errorf("n1 UsedMemoryMiB(%d)+FreeMemoryMiB(%d) = %d, want AllocatableMemoryMiB = %d", n1detail.UsedMemoryMiB, n1detail.FreeMemoryMiB, got, want)
	}
}

// Node eligibility classification (cordoned/not ready/untolerated taint) is exercised in
// internal/controllers/resources/node_test.go: resources.NodeIneligibleReason is the single shared
// predicate this package's NodeInventory/FullDrivesInventory/ExploreNodes all call into.
