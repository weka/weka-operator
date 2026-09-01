package allocator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// ErrDriveAllocationPending marks a capacity figure that cannot be derived yet because no drive container
// has allocated the drive count being asked about — the state every raise of numDrives passes through
// before containers pick up the new count. Callers must defer and retry, not fail: retrying lets the
// capacity resolve once drives are allocated, while failing would deadlock the raise itself.
var ErrDriveAllocationPending = errors.New("drive allocation pending")

// CalculateDriveHugepages and CalculateDriveHugepagesOffset are the template-shaped entry points to the
// one drive-hugepages formula (capacityplanner.DriveContainerHugepages{,Offset}MiB); DPDK is added by the
// caller. Planner-managed modes skip this path — their per-container drive counts can't fit a cluster-wide
// ClusterTemplate — and use DriveHugepagesFromPlan instead.
func CalculateDriveHugepages(template ClusterTemplate) int { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	return capacityplanner.DriveContainerHugepagesMiB(template.Cores.Drive, template.NumDrives, noDpdkConstraints)
}

func CalculateDriveHugepagesOffset(template ClusterTemplate) int { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	return capacityplanner.DriveContainerHugepagesOffsetMiB(template.Cores.Drive, template.NumDrives, noDpdkConstraints)
}

// noDpdkConstraints carries only the coefficients the drive-hugepages formula reads, with the per-core DPDK
// term zeroed: GetContainerHugepages adds DPDK itself, after these, so including it here would double-count.
var noDpdkConstraints = &capacityplanner.CapacityConstraints{HugepagesPerCoreMiB: capacityplanner.HugepagesPerCoreMiB}

// ErrPlannerManagedComputeHugepages is returned when GetContainerHugepages is called for a cluster whose
// compute sizing is planner-managed (auto full drives). Callers must treat it as fatal rather than as an
// ordinary transient sizing failure — see calculateDynamicComputeHugepages for why.
var ErrPlannerManagedComputeHugepages = errors.New("auto full drives compute hugepages come from the planner's " +
	"ComputeLayout via ComputeHugepagesFromPlan; GetContainerHugepages must not be used for planner-managed compute")

func calculateDynamicComputeHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster, containers []*weka.WekaContainer) (hp int, err error) { //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	var totalRawCapacityGiB int

	switch {
	case cluster.Spec.Dynamic != nil && cluster.Spec.Dynamic.UsesClusterCapacity():
		// Heterogeneous drive containers have no uniform ContainerCapacity/NumDrives to extrapolate
		// from, so raw capacity comes directly from the cluster-capacity target the FD planner uses.
		clusterCapGiB, ccErr := cluster.Spec.Dynamic.GetClusterCapacityGiB()
		if ccErr != nil {
			return 0, fmt.Errorf("clusterCapacity compute hugepages: %w", ccErr)
		}
		// Effective protection matches what the FD planner/webhook use; raw spec 0/0/0 would divide by zero.
		sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(
			cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare,
		)
		totalRawCapacityGiB = capacityplanner.RawCapacityGiB(clusterCapGiB, sw, rl, hs)
	case cluster.Spec.Dynamic.UsesAutoFullDrives():
		// Auto full drives sizes compute from the planner's own per-container figure
		// (ComputeHugepagesFromPlan over plan.ComputeLayout); nothing should reach this path. Kept rather
		// than removed so a stray caller fails loudly instead of falling into a template-based case and
		// under-counting a heterogeneous fleet.
		return 0, ErrPlannerManagedComputeHugepages
	case template.ContainerCapacity > 0:
		totalRawCapacityGiB = template.ContainerCapacity * template.Containers.Drive
	case template.NumDrives > 0 && template.DriveCapacity > 0:
		totalRawCapacityGiB = template.NumDrives * template.DriveCapacity * template.Containers.Drive
	case template.Containers.Drive > 0:
		// Full-drives mode: capacity is derived from the most recently created drive container's
		// allocation, looked up in the weka-full-drives node annotation.
		totalRawCapacityGiB, err = ComputeCapacityFromMostRecentDriveContainerAllocation(
			ctx, k8sClient, containers, template.Containers.Drive, template.NumDrives,
		)
		if err != nil {
			return 0, err
		}
	default:
		return 0, errors.New("either containerCapacity or numDrives must be specified for dynamic template")
	}

	if totalRawCapacityGiB > 0 {
		hp = ComputeCapacityBasedHugepages(
			ctx, totalRawCapacityGiB, template.Containers.Compute, template.Cores.Compute, template.DriveTypesRatio,
		)
	} else {
		hp = 3000 * template.Cores.Compute // fallback minimum when capacity is unknown
	}

	return
}

// ComputeCapacityFromMostRecentDriveContainerAllocation determines the total cluster drive capacity
// by finding the most recently created drive container that has all its drives allocated, reading the
// capacity of those drives from the weka-full-drives node annotation, and extrapolating to all drive containers.
//
// containers is already provided by the caller — no additional list API call is made here.
// The only additional API call is a single node GET for the reference container's annotation.
func ComputeCapacityFromMostRecentDriveContainerAllocation(
	ctx context.Context,
	k8sClient client.Client,
	containers []*weka.WekaContainer,
	numDriveContainers int,
	numDrives int,
) (int, error) {
	if numDrives <= 0 {
		return 0, fmt.Errorf("numDrives must be > 0 for full-drives mode hugepages calculation, got %d", numDrives)
	}

	var candidates []*weka.WekaContainer
	for _, c := range containers {
		if !c.IsDriveContainer() {
			continue
		}
		if c.Status.Allocations == nil || len(c.Status.Allocations.Drives) != numDrives {
			continue
		}
		candidates = append(candidates, c)
	}

	if len(candidates) == 0 {
		return 0, fmt.Errorf("%w: no drive containers with %d allocated drives found yet", ErrDriveAllocationPending, numDrives)
	}

	// Sort by creation timestamp descending, name as a tiebreaker — otherwise containers created in
	// the same second could pick a non-deterministic reference and cause a perpetual spec-hash flip-flop.
	slices.SortFunc(candidates, func(a, b *weka.WekaContainer) int {
		if ts := cmp.Compare(
			b.CreationTimestamp.UnixNano(),
			a.CreationTimestamp.UnixNano(),
		); ts != 0 {
			return ts
		}
		return cmp.Compare(a.Name, b.Name)
	})

	ref := candidates[0]

	nodeName := ref.Status.NodeAffinity
	if nodeName == "" {
		return 0, fmt.Errorf("reference drive container %s has no node affinity in status", ref.Name)
	}
	capacityBySerial, err := nodeDriveCapacityBySerial(ctx, k8sClient, nodeName)
	if err != nil {
		return 0, err
	}

	perContainerGiB := 0
	for _, serial := range ref.Status.Allocations.Drives {
		capGiB, ok := capacityBySerial[serial]
		if !ok {
			return 0, fmt.Errorf("allocated drive %s on node %s not found in %s annotation", serial, nodeName, consts.AnnotationWekaFullDrives)
		}
		if capGiB == 0 {
			return 0, fmt.Errorf("allocated drive %s on node %s has zero capacity in %s annotation", serial, nodeName, consts.AnnotationWekaFullDrives)
		}
		perContainerGiB += capGiB
	}

	// Extrapolated to all drive containers, assuming homogeneous drive capacity across them (valid for
	// Weka clusters, where all nodes must have identical drive configurations).
	totalGiB := perContainerGiB * numDriveContainers
	return totalGiB, nil
}

// nodeDriveCapacityBySerial fetches nodeName (one GET) and returns a serial -> capacity_gib lookup built
// from its weka-full-drives annotation via the canonical allocator reader, so blocked drives are excluded.
func nodeDriveCapacityBySerial(ctx context.Context, k8sClient client.Client, nodeName weka.NodeName) (map[string]int, error) {
	node := &v1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return nil, fmt.Errorf("failed to get node %s for drive capacity lookup: %w", nodeName, err)
	}

	info, err := ParseAllocatorNodeInfo(node)
	if err != nil {
		return nil, fmt.Errorf("failed to parse allocator node info for node %s: %w", nodeName, err)
	}
	entries := info.AvailableDrives
	if len(entries) == 0 {
		return nil, fmt.Errorf("node %s has no available (non-blocked) full drives with capacity", nodeName)
	}

	capacityBySerial := make(map[string]int, len(entries))
	for _, e := range entries {
		capacityBySerial[e.Serial] = e.CapacityGiB
	}
	return capacityBySerial, nil
}

// ComputeCapacityBasedHugepages calculates compute hugepages using TLC/QLC-aware capacity ratios.
func ComputeCapacityBasedHugepages(ctx context.Context, totalRawCapacityGiB, computeContainers, computeCores int, driveTypesRatio *weka.DriveTypesRatio) int {
	_, logger := instrumentation.CreateLogSpan(ctx, "ComputeCapacityBasedHugepages")
	defer logger.End()

	capacityBased := 0
	if computeContainers > 0 && totalRawCapacityGiB > 0 {
		tlcCapGiB, qlcCapGiB := weka.GetTlcQlcCapacity(totalRawCapacityGiB, driveTypesRatio)

		hugepagesTlcRatio := globalconfig.Config.DriveSharing.HugepagesTlcRatio
		if hugepagesTlcRatio == 0 {
			hugepagesTlcRatio = 1000
		}
		hugepagesQlcRatio := globalconfig.Config.DriveSharing.HugepagesQlcRatio
		if hugepagesQlcRatio == 0 {
			hugepagesQlcRatio = 6000
		}

		// Compute cluster-wide hugepages in MiB from TLC and QLC capacities
		// Formula: (tlcGiB * 1024 / tlcRatio) + (qlcGiB * 1024 / qlcRatio)
		clusterHugepagesMiB := 0
		if hugepagesTlcRatio > 0 {
			clusterHugepagesMiB += tlcCapGiB * 1024 / hugepagesTlcRatio
		}
		if hugepagesQlcRatio > 0 {
			clusterHugepagesMiB += qlcCapGiB * 1024 / hugepagesQlcRatio
		}

		capacityBased = clusterHugepagesMiB / computeContainers
	}

	perCoreComponent := 1700 * computeCores
	minHugepages := 3000 * computeCores
	hugepages := max(capacityBased+perCoreComponent, minHugepages)

	if hugepages%2 != 0 { // must be divisible by 2
		hugepages++
	}

	maxMiB := globalconfig.Config.ComputeMaxHugepagesMiB // must be even — validated at Helm level
	if maxMiB > 0 && hugepages > maxMiB {
		hugepages = maxMiB
	}

	logger.Debug("Calculated compute hugepages",
		"totalRawCapacityGiB", totalRawCapacityGiB,
		"hugepages", hugepages)

	return hugepages
}
