package validation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// Distinct label KEYS, deliberately: a hyperconverged node carries both, and a single shared key
// (e.g. role=drive / role=compute) could only ever hold one of them.
var (
	afdDriveLabels   = map[string]string{"afd-drive": "yes"}
	afdComputeLabels = map[string]string{"afd-compute": "yes"}
)

// setAutoFullDrivesHugepagesConfig pins every knob the validator reads to the operator's shipped
// defaults (LoadCapacityEnv isn't called in unit tests, so they'd otherwise be zero) and restores them
// on cleanup.
func setAutoFullDrivesHugepagesConfig(t *testing.T) {
	t.Helper()
	prevTlcRatio := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	prevMaxHP := globalconfig.Config.ComputeMaxHugepagesMiB
	prevMaxCores := globalconfig.Config.CapacityPlanner.MaxCoresPerContainer
	prevFullDrivesRatio := globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio
	prevMinCompute := globalconfig.Consts.FormClusterMinComputeContainers

	globalconfig.Config.DriveSharing.HugepagesTlcRatio = 1000
	globalconfig.Config.ComputeMaxHugepagesMiB = 360000
	globalconfig.Config.CapacityPlanner.MaxCoresPerContainer = 19
	globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = 2.0
	globalconfig.Consts.FormClusterMinComputeContainers = 5

	t.Cleanup(func() {
		globalconfig.Config.DriveSharing.HugepagesTlcRatio = prevTlcRatio
		globalconfig.Config.ComputeMaxHugepagesMiB = prevMaxHP
		globalconfig.Config.CapacityPlanner.MaxCoresPerContainer = prevMaxCores
		globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = prevFullDrivesRatio
		globalconfig.Consts.FormClusterMinComputeContainers = prevMinCompute
	})
}

// withHugepages stamps allocatable hugepages-2Mi onto a node.
func withHugepages(n *corev1.Node, allocatableMiB int) *corev1.Node {
	n.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi"): *resource.NewQuantity(
			int64(allocatableMiB)*mib, resource.BinarySI),
	}
	return n
}

// computeRoleNode builds a diskless compute-eligible node with the given allocatable hugepages-2Mi.
func computeRoleNode(name string, allocatableMiB int) *corev1.Node {
	return withHugepages(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: afdComputeLabels},
	}, allocatableMiB)
}

// afdCluster builds an auto-full-drives cluster with explicit drive and compute role selectors.
func afdCluster(dynamic *weka.WekaClusterTemplate) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	drive, compute := afdDriveLabels, afdComputeLabels
	c.Spec.RoleNodeSelector.Drive = &drive
	c.Spec.RoleNodeSelector.Compute = &compute
	return c
}

// afdHyperconvergedFleet is the fleet from doc/operator/deployment/act-as-daemonset.md's worked
// example: 8 HYPERCONVERGED nodes, each carrying 6 signed drives of 14307 GiB and `allocatableMiB` of
// hugepages, and each matched by BOTH role selectors. Every node therefore pays for its own drive
// container out of the same hugepages pool its compute container draws from — the shape the doc's
// "60,000 − 6 × 1664 = 50,016 MiB" step describes.
func afdHyperconvergedFleet(t *testing.T, nodeCount, allocatableMiB int) []*corev1.Node {
	t.Helper()
	labels := map[string]string{}
	for k, v := range afdDriveLabels {
		labels[k] = v
	}
	for k, v := range afdComputeLabels {
		labels[k] = v
	}
	var nodes []*corev1.Node
	for i := 0; i < nodeCount; i++ {
		caps := make([]int, 6)
		for j := range caps {
			caps[j] = 14307
		}
		nodes = append(nodes, withHugepages(
			driveRoleNode(t, fmt.Sprintf("node-%d", i), labels, caps), allocatableMiB))
	}
	return nodes
}

// afdLabFleet is the same 8 drive nodes but with a SEPARATE, diskless pool of compute-eligible nodes,
// so the two populations never overlap and no node is charged for a drive container.
func afdLabFleet(t *testing.T, computeFreeMiB int, computeNodes int) []*corev1.Node {
	t.Helper()
	var nodes []*corev1.Node
	for i := 0; i < 8; i++ {
		caps := make([]int, 6)
		for j := range caps {
			caps[j] = 14307
		}
		nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("drive-%d", i), afdDriveLabels, caps))
	}
	for i := 0; i < computeNodes; i++ {
		nodes = append(nodes, computeRoleNode(fmt.Sprintf("compute-%d", i), computeFreeMiB))
	}
	return nodes
}

// TestAutoFullDrivesComputeHugepages_LabFleetGroundTruth reproduces the worked example in
// doc/operator/deployment/act-as-daemonset.md step by step. The doc is the specification for this
// policy, so every figure it prints is asserted here: if the two ever disagree, one of them is a bug.
//
//	claimed      = 8 × 6 × 14307                    = 686,736 GiB
//	drive cores  = 8 × min(6, 19)                   = 48   ⇒ 2.0 × 48 = 96 compute cores
//	per node     = 60,000 − 6 × 1664 (own drive ctr) = 50,016 MiB for compute
//	at 8 ctrs    = 703,217/8 + 1700×12, +64×12      = 109,070 MiB  ✗
//	at 18 ctrs   = 703,217/18 + 1700×6, evened, +384 = 49,652 MiB  ✓  (17 misses at 51,950)
func TestAutoFullDrivesComputeHugepages_LabFleetGroundTruth(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdHyperconvergedFleet(t, 8, 60000)...)

	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	detail := errs[0].Detail
	for _, want := range []string{
		"686736 GiB",      // step 1
		"48 drive core",   // step 2 — the FULL derived count, never reduced to fit compute
		"96 compute core", // step 3
		"109070 MiB of hugepages per compute container", // step 4
		"50016 MiB", // step 2, net of this node's own drive container
		"18 compute-eligible node(s) of that size would be needed", // step 6
		"AutoFullDrivesInfeasible",
		"Drive cores are never reduced to make compute fit",
		// Step 5: even at one compute core the capacity share alone is 89,666 MiB, above the node's
		// entire 60,000 MiB — so this fleet is capacity-bound and no core reduction can rescue it.
		"89666 MiB even at one compute core",
		"the binding term is capacity, not cores",
		// The four remedies, in the doc's order.
		"add compute-eligible nodes",
		// Helm value names, asserted verbatim: both are top-level keys in values.yaml
		"hugepagesTlcRatio Helm value",
		"computeMaxHugepagesMiB Helm value",
		"spec.dynamicTemplate.numDrives lower",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected message to contain %q, got: %s", want, detail)
		}
	}
	// Offering "lower driveCores" on a capacity-bound fleet would send the operator down a dead end.
	if strings.Contains(detail, "lower spec.dynamicTemplate.driveCores") {
		t.Errorf("must not offer the driveCores remedy when capacity binds, got: %s", detail)
	}
}

// TestAutoFullDrivesComputeHugepages_DriveCoresRemedyWhenCoresBind is the other side of that branch.
// The planner no longer reduces drive cores on its own, so on a fleet where the PER-CORE term is what
// binds, lowering driveCores is the operator's most direct lever and costs zero drives — the message
// must say so. Small drives (2000 GiB) keep the capacity share at 12,288 MiB, well inside the node's
// 20,000 MiB, so the shortfall is genuinely about cores.
func TestAutoFullDrivesComputeHugepages_DriveCoresRemedyWhenCoresBind(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	bothRoles := map[string]string{}
	for k, val := range afdDriveLabels {
		bothRoles[k] = val
	}
	for k, val := range afdComputeLabels {
		bothRoles[k] = val
	}
	var nodes []*corev1.Node
	for i := 0; i < 8; i++ {
		caps := make([]int, 6)
		for j := range caps {
			caps[j] = 2000
		}
		nodes = append(nodes, withHugepages(
			driveRoleNode(t, fmt.Sprintf("node-%d", i), bothRoles, caps), 20000))
	}

	errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	detail := errs[0].Detail
	if !strings.Contains(detail, "pin spec.dynamicTemplate.driveCores — it is currently derived per node "+
		"from each node's drive count (48 core(s) across 8 node(s))") {
		t.Errorf("expected the driveCores remedy reporting the derived totals, got: %s", detail)
	}
	if strings.Contains(detail, "the binding term is capacity, not cores") {
		t.Errorf("this fleet is core-bound, not capacity-bound, got: %s", detail)
	}
}

// TestAutoFullDrivesComputeHugepages_HyperconvergedNodePaysForItsDriveContainer isolates the headroom
// rule. Both fleets have the SAME claim (the same 8 signed drive nodes ⇒ 686,736 GiB, 48 drive cores,
// 96 compute cores) and the SAME 18 compute-eligible nodes at 55,000 MiB allocatable — the only
// difference is whether the 8 drive nodes are among those 18.
//
// Disjoint: 18 diskless nodes at 55,000 ≥ the 49,652 MiB an 18-container layout needs ⇒ admitted.
// Overlapping: the 8 drive nodes are charged 6 × 1664 = 9,984 MiB for their own drive container,
// leaving 45,016 — below 49,652 ⇒ rejected. Without the charge this fleet would be waved through.
func TestAutoFullDrivesComputeHugepages_HyperconvergedNodePaysForItsDriveContainer(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	const allocatableMiB = 55000

	signedDrives := func() []int {
		caps := make([]int, 6)
		for j := range caps {
			caps[j] = 14307
		}
		return caps
	}
	bothRoles := map[string]string{}
	for k, v := range afdDriveLabels {
		bothRoles[k] = v
	}
	for k, v := range afdComputeLabels {
		bothRoles[k] = v
	}

	t.Run("disjoint diskless compute pool is admitted", func(t *testing.T) {
		var nodes []*corev1.Node
		for i := 0; i < 8; i++ {
			nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("drive-%d", i), afdDriveLabels, signedDrives()))
		}
		for i := 0; i < 18; i++ {
			nodes = append(nodes, computeRoleNode(fmt.Sprintf("compute-%d", i), allocatableMiB))
		}
		if errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
			t.Fatalf("18 diskless nodes at %d MiB should fit the 49652 MiB layout, got %v", allocatableMiB, errs)
		}
	})

	t.Run("overlapping drive nodes are charged and rejected", func(t *testing.T) {
		var nodes []*corev1.Node
		for i := 0; i < 8; i++ {
			nodes = append(nodes, withHugepages(
				driveRoleNode(t, fmt.Sprintf("both-%d", i), bothRoles, signedDrives()), allocatableMiB))
		}
		for i := 0; i < 10; i++ {
			nodes = append(nodes, computeRoleNode(fmt.Sprintf("compute-%d", i), allocatableMiB))
		}
		errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), afdCluster(&weka.WekaClusterTemplate{}))
		if len(errs) != 1 {
			t.Fatalf("expected rejection once the 8 drive nodes pay for their own drive container, got %v", errs)
		}
		// The claim is unchanged — only the headroom moved.
		if !strings.Contains(errs[0].Detail, "686736 GiB") || !strings.Contains(errs[0].Detail, "48 drive core") {
			t.Errorf("expected the same claim as the disjoint case, got: %s", errs[0].Detail)
		}
	})
}

// TestAutoFullDrivesComputeHugepages_Fits: the same fleet on nodes with room to spare is admitted.
func TestAutoFullDrivesComputeHugepages_Fits(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 200000, 8)...)

	if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("expected no violation when the requirement fits, got %v", errs)
	}
}

// TestAutoFullDrivesComputeHugepages_CoreCapBindsBeforeHugepages guards the per-container core cap.
// The 8-node drive fleet needs 96 compute cores (48 drive cores × 2.0), and compute spreads one
// container per node at no more than MaxCoresPerContainer=19 — so 5 compute nodes top out at 95 cores
// and no layout can carry the requirement, however much memory the nodes have. Clamping cores to 19 and
// pricing THAT container's hugepages would find a comfortable fit at 200000 MiB per node (174159 MiB
// needed) and admit a plan deriveComputeLayout rejects outright. One more compute node closes the gap
// (ceil(96/6) = 16 cores, within the cap), which is the control below.
func TestAutoFullDrivesComputeHugepages_CoreCapBindsBeforeHugepages(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 200000, 5)...)

	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected a violation when the core cap alone makes the plan infeasible, got %v", errs)
	}
	detail := errs[0].Detail
	// The message must name cores, not memory: the nodes have hugepages to spare, and sending the
	// operator after memory here would be a dead end.
	for _, want := range []string{"96 compute core(s)", "top out at 95 compute core(s)", "at most 19 core(s) each"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q, got: %s", want, detail)
		}
	}
	if strings.Contains(detail, "the most any compute-eligible node has free") {
		t.Errorf("core shortfall must not be reported as a hugepages shortfall, got: %s", detail)
	}

	// Control: a sixth compute node brings ceil(96/6)=16 cores under the cap, and 200000 MiB per node
	// covers the 145426 MiB that layout needs.
	c = fakeClientWithNodes(t, afdLabFleet(t, 200000, 6)...)
	if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("expected no violation once the cores fit under the cap, got %v", errs)
	}
}

// TestAutoFullDrivesComputeHugepages_PinnedComputeCoresHonoredVerbatim: UsesAutoFullDrives doesn't
// consult ComputeCores, so a pin coexists with AFD mode. deriveComputeLayout's specCores branch
// honors it exactly and derives count from it (see
// TestDeriveComputeLayout_AgreesWithAutoFullDrivesHugepagesValidator's pinned-cores case for the
// same numbers), rather than the unpinned sweep's opposite direction of deriving cores from count —
// so a pin must not be silently ignored.
func TestAutoFullDrivesComputeHugepages_PinnedComputeCoresHonoredVerbatim(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	// 96 required compute cores (48 drive cores × 2.0). Pinned at 18 cores/container that's
	// ceil(96/18)=6 containers, but only 5 compute-eligible nodes -- infeasible on node count, not
	// hugepages, however much memory those 5 nodes have.
	c := fakeClientWithNodes(t, afdLabFleet(t, 200000, 5)...)
	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{ComputeCores: 18}))
	if len(errs) != 1 {
		t.Fatalf("expected a violation when the pin's derived count exceeds the compute node count, got %v", errs)
	}
	for _, want := range []string{"pinned at 18", "6 compute container(s)", "only 5 node(s) are compute-eligible"} {
		if !strings.Contains(errs[0].Detail, want) {
			t.Errorf("detail missing %q, got: %s", want, errs[0].Detail)
		}
	}

	// A sixth compute node supplies the 6th container the pin needs, and 200000 MiB covers its share.
	c = fakeClientWithNodes(t, afdLabFleet(t, 200000, 6)...)
	if errs = v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{ComputeCores: 18})); len(errs) != 0 {
		t.Errorf("expected no violation once the pin's derived count fits the compute node count, got %v", errs)
	}

	// Same 6-node fleet, but too little memory for 18 cores/container: a hugepages shortfall, not a
	// node-count one, and the message must name memory instead.
	c = fakeClientWithNodes(t, afdLabFleet(t, 10000, 6)...)
	errs = v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{ComputeCores: 18}))
	if len(errs) != 1 {
		t.Fatalf("expected a violation when the pin's hugepages do not fit, got %v", errs)
	}
	if !strings.Contains(errs[0].Detail, "MiB free after drive placement") {
		t.Errorf("expected a hugepages shortfall message, got: %s", errs[0].Detail)
	}
}

// TestAutoFullDrivesComputeHugepages_ProjectsFullDerivedCores is the regression guard for the
// projection rule, and it runs in the direction that matters. There is no co-sizing search: drive
// cores are min(drives, 19) and are never reduced to make compute fit. At 100000 MiB per node the
// fleet needs 109070 MiB per compute container at the full 6 cores/node and must be REJECTED —
// projecting any reduced core count (e.g. 1/node, which would ask only 91430 MiB) would wave through
// a cluster the planner immediately declares infeasible.
func TestAutoFullDrivesComputeHugepages_ProjectsFullDerivedCores(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 100000, 8)...)

	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("drive cores must be projected at the full derived count (48), not a reduced one; got %v", errs)
	}
	if !strings.Contains(errs[0].Detail, "48 drive core") {
		t.Errorf("expected the full derived core total, got: %s", errs[0].Detail)
	}
}

// TestAutoFullDrivesComputeHugepages_PinnedDriveCoresHonoredVerbatim: a driveCores pin replaces the
// derived count outright, in both directions. Pinning 2 cores/node on the fleet above cuts compute
// demand from 96 to 32 cores and brings it within reach — every drive is still claimed, just run on
// fewer cores.
func TestAutoFullDrivesComputeHugepages_PinnedDriveCoresHonoredVerbatim(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 100000, 8)...)

	if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{DriveCores: 2})); len(errs) != 0 {
		t.Errorf("a lower driveCores pin must be honored verbatim and lower compute demand, got %v", errs)
	}
	// The same fleet at the derived 6 cores/node is rejected — see _ProjectsFullDerivedCores.
	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{DriveCores: 6}))
	if len(errs) != 1 {
		t.Fatalf("expected a pin equal to the derived count to reject like the unpinned case, got %v", errs)
	}
	if !strings.Contains(errs[0].Detail, "48 drive core") {
		t.Errorf("expected the pinned core total in the message, got: %s", errs[0].Detail)
	}
}

// TestAutoFullDrivesComputeHugepages_FloorAboveNodeCountSkipped: when the form-cluster floor already
// exceeds the compute-eligible node count the cluster is infeasible for a different reason, and this
// message would misattribute it to hugepages.
func TestAutoFullDrivesComputeHugepages_FloorAboveNodeCountSkipped(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t) // FormClusterMinComputeContainers = 5
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	// 3 compute-eligible nodes, far too small — but the floor of 5 is unreachable first.
	c := fakeClientWithNodes(t, afdLabFleet(t, 1000, 3)...)

	if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("expected no violation when the form-cluster floor is unreachable, got %v", errs)
	}
}

// TestAutoFullDrivesComputeHugepages_NumDrivesPinLowersTheClaim exercises the fourth remedy: pinning
// numDrives lower cuts the claimed capacity and with it the per-container hugepages share.
func TestAutoFullDrivesComputeHugepages_NumDrivesPinLowersTheClaim(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 60000, 8)...)

	if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{NumDrives: 1})); len(errs) != 0 {
		t.Errorf("expected numDrives=1 to bring the claim within reach, got %v", errs)
	}
}

// TestAutoFullDrivesComputeHugepages_NodeTooSmallForOneCore: when no node can host even a single-core
// compute container, the message must say the shortfall is per-node hugepages, not node count.
func TestAutoFullDrivesComputeHugepages_NodeTooSmallForOneCore(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 1000, 8)...)

	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	if !strings.Contains(errs[0].Detail, "no number of nodes of that size is enough") {
		t.Errorf("expected the per-node-shortfall wording, got: %s", errs[0].Detail)
	}
}

// TestAutoFullDrivesComputeHugepages_CapSaturatedNote: once computeMaxHugepagesMiB clamps the base,
// raising the TLC ratio cannot move it, so the message must say which lever still works rather than
// listing one that does nothing.
func TestAutoFullDrivesComputeHugepages_CapSaturatedNote(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	prev := globalconfig.Config.ComputeMaxHugepagesMiB
	globalconfig.Config.ComputeMaxHugepagesMiB = 20000
	t.Cleanup(func() { globalconfig.Config.ComputeMaxHugepagesMiB = prev })

	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()
	c := fakeClientWithNodes(t, afdLabFleet(t, 19000, 8)...)

	errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	if !strings.Contains(errs[0].Detail, "already clamped at computeMaxHugepagesMiB") {
		t.Errorf("expected the cap-saturation note, got: %s", errs[0].Detail)
	}
}

// TestAutoFullDrivesComputeHugepages_Skips covers every path that must stay silent.
func TestAutoFullDrivesComputeHugepages_Skips(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	// Bootstrap: drive-role nodes exist but none is signed yet, so there is nothing to project from.
	t.Run("pre-signing drive nodes", func(t *testing.T) {
		nodes := []*corev1.Node{
			driveRoleNode(t, "drive-0", afdDriveLabels, nil),
			driveRoleNode(t, "drive-1", afdDriveLabels, nil),
			computeRoleNode("compute-0", 1000),
		}
		if errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
			t.Errorf("expected no violation pre-signing, got %v", errs)
		}
	})

	t.Run("no drive-role nodes matched", func(t *testing.T) {
		c := fakeClientWithNodes(t, computeRoleNode("compute-0", 1000))
		if errs := v.Validate(ctx, c, afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
			t.Errorf("expected no violation, got %v", errs)
		}
	})

	// clusterSelectedNodesCount owns "the compute selector matches nothing".
	t.Run("no compute-eligible nodes matched", func(t *testing.T) {
		var nodes []*corev1.Node
		for i := 0; i < 8; i++ {
			caps := make([]int, 6)
			for j := range caps {
				caps[j] = 14307
			}
			nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("drive-%d", i), afdDriveLabels, caps))
		}
		if errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), afdCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
			t.Errorf("expected no violation, got %v", errs)
		}
	})

	// Every other sizing mode is out of scope: the planner can shrink its capacity target there.
	t.Run("other sizing modes", func(t *testing.T) {
		c := fakeClientWithNodes(t, afdLabFleet(t, 60000, 8)...)
		for name, dyn := range map[string]*weka.WekaClusterTemplate{
			"clusterCapacity":   {ClusterCapacity: "500TiB"},
			"containerCapacity": {ContainerCapacity: 6000},
			"counts":            {ComputeContainers: 6, DriveContainers: 6},
		} {
			t.Run(name, func(t *testing.T) {
				if errs := v.Validate(ctx, c, afdCluster(dyn)); len(errs) != 0 {
					t.Errorf("expected no violation, got %v", errs)
				}
			})
		}
	})
}

// TestAutoFullDrivesComputeHugepages_NilTemplate: a nil dynamicTemplate is the mode's default shape and
// must be evaluated, not skipped.
func TestAutoFullDrivesComputeHugepages_NilTemplate(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	v := &clusterAutoFullDrivesComputeHugepages{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, afdLabFleet(t, 60000, 8)...)

	cluster := afdCluster(nil)
	if errs := v.Validate(ctx, c, cluster); len(errs) != 1 {
		t.Fatalf("expected a nil template to be evaluated as auto-full-drives, got %v", errs)
	}
}

// A drive container reserves hugepages per CORE *and* per DRIVE (1400/core + 200/drive + DPDK/core, the
// figure its pod requests). This projection charged per core only, which under-reserved by 200 MiB per drive
// on exactly the shape this mode creates — a node holding more drives than cores — and so reported room for
// compute that the drive container had already taken.
//
// The fixture is the doc's 8-node hyperconverged fleet with driveCores pinned to 1, so each node holds 6
// drives on 1 core. Correct accounting reserves 1400 + 6*200 + 64 = 2664 MiB per node; cores-only reserved
// 1664. At 93500 MiB allocatable the difference is decisive: the compute container needs 91430 MiB, which
// fits under the old figure and does not under the real one. Verified by mutation — restoring the cores-only
// arithmetic makes this fleet pass admission and then fail at runtime.
func TestAutoFullDrivesComputeHugepages_ChargesDriveReservationPerDrive(t *testing.T) {
	setAutoFullDrivesHugepagesConfig(t)
	ctx := context.Background()
	v := clusterAutoFullDrivesComputeHugepages{}
	pinned := &weka.WekaClusterTemplate{DriveCores: 1}

	// Just under the real threshold: must be rejected.
	tooTight := fakeClientWithNodes(t, afdHyperconvergedFleet(t, 8, 93500)...)
	if errs := v.Validate(ctx, tooTight, afdCluster(pinned)); len(errs) == 0 {
		t.Error("fleet admitted at 93500 MiB — the drive reservation is being charged per core only, so " +
			"admission is offering compute the drive container's per-drive hugepages")
	}

	// Above it: must still be accepted, so the check is not simply rejecting everything.
	roomy := fakeClientWithNodes(t, afdHyperconvergedFleet(t, 8, 94500)...)
	if errs := v.Validate(ctx, roomy, afdCluster(pinned)); len(errs) != 0 {
		t.Errorf("fleet rejected at 94500 MiB, where compute genuinely fits: %v", errs)
	}
}
