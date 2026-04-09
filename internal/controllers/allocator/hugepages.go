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

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

func CalculateDriveHugepages(template ClusterTemplate) int {
	if template.NumDrives > 0 {
		return 1400*template.Cores.Drive + 200*template.NumDrives
	} else {
		return 1600 * template.Cores.Drive
	}
}

func CalculateDriveHugepagesOffset(template ClusterTemplate) int {
	if template.NumDrives > 0 {
		return 200 * template.NumDrives
	} else {
		return 200 * template.Cores.Drive
	}
}

// Compute hugepages (capacity-based)
func calculateDynamicComputeHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster, containers []*weka.WekaContainer) (hp int, err error) {
	var totalRawCapacityGiB int

	if template.ContainerCapacity > 0 {
		// Drive-sharing mode - full capacity per drive container is known
		totalRawCapacityGiB = template.ContainerCapacity * template.Containers.Drive
	} else if template.NumDrives > 0 && template.DriveCapacity > 0 {
		// Drive-sharing mode with explicit drive count and capacity
		totalRawCapacityGiB = template.NumDrives * template.DriveCapacity * template.Containers.Drive
	} else if template.Containers.Drive > 0 {
		// Full-drives mode: unified path for both new and ready clusters.
		// Capacity is derived from the most recently created drive container's allocation
		// looked up in the weka-full-drives node annotation.
		totalRawCapacityGiB, err = ComputeCapacityFromMostRecentDriveContainerAllocation(
			ctx, k8sClient, containers, template.Containers.Drive, template.NumDrives,
		)
		if err != nil {
			return 0, err
		}
	} else {
		return 0, errors.New("either containerCapacity or numDrives must be specified for dynamic template")
	}

	if totalRawCapacityGiB > 0 {
		hp = ComputeCapacityBasedHugepages(
			ctx, totalRawCapacityGiB, template.Containers.Compute, template.Cores.Compute, template.DriveTypesRatio,
		)
	} else {
		// Fallback minimum when capacity is unknown
		hp = 3000 * template.Cores.Compute
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

	// Collect drive containers that have exactly numDrives allocated
	var candidates []*weka.WekaContainer
	for _, c := range containers {
		if !c.IsDriveContainer() {
			continue
		}
		// full-drives mode assumes numDrives > 0
		if c.Status.Allocations == nil || len(c.Status.Allocations.Drives) != numDrives {
			continue
		}
		candidates = append(candidates, c)
	}

	if len(candidates) == 0 {
		return 0, fmt.Errorf("no drive containers with %d allocated drives found yet", numDrives)
	}

	// Sort by creation timestamp descending — most recently created first.
	// Name is a secondary key to break ties deterministically (all containers
	// created in the same second would otherwise produce a non-deterministic
	// reference container and cause a perpetual spec-hash flip-flop).
	slices.SortFunc(candidates, func(a, b *weka.WekaContainer) int {
		if ts := cmp.Compare(
			b.CreationTimestamp.UnixNano(),
			a.CreationTimestamp.UnixNano(),
		); ts != 0 {
			return ts
		}
		return cmp.Compare(a.Name, b.Name)
	})

	// Use the most recently created candidate as the reference
	ref := candidates[0]

	// Fetch the node's weka-full-drives annotation (one GET)
	nodeName := ref.Status.NodeAffinity
	if nodeName == "" {
		return 0, fmt.Errorf("reference drive container %s has no node affinity in status", ref.Name)
	}
	node := &v1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return 0, fmt.Errorf("failed to get node %s for drive capacity lookup: %w", nodeName, err)
	}

	fullAnnotation := node.Annotations[consts.AnnotationWekaFullDrives]
	if fullAnnotation == "" {
		return 0, fmt.Errorf("node %s has no %s annotation yet", nodeName, consts.AnnotationWekaFullDrives)
	}
	entries, err := domain.ReadDriveAnnotations(fullAnnotation)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s annotation on node %s: %w", consts.AnnotationWekaFullDrives, nodeName, err)
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("node %s has %s annotation but all entries have zero capacity", nodeName, consts.AnnotationWekaFullDrives)
	}

	// Build serial → capacity_gib lookup
	capacityBySerial := make(map[string]int, len(entries))
	for _, e := range entries {
		capacityBySerial[e.Serial] = e.CapacityGiB
	}

	// Sum capacity for the reference container's allocated drives
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

	// Extrapolate to all drive containers.
	// Assumes homogeneous drive capacity across containers — a valid assumption for Weka
	// clusters where all nodes must have identical drive configurations.
	totalGiB := perContainerGiB * numDriveContainers
	return totalGiB, nil
}

// ComputeCapacityBasedHugepages calculates compute hugepages using TLC/QLC-aware capacity ratios.
func ComputeCapacityBasedHugepages(ctx context.Context, totalRawCapacityGiB, computeContainers, computeCores int, driveTypesRatio *weka.DriveTypesRatio) int {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ComputeCapacityBasedHugepages")
	defer end()

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

	// Must be divisible by 2, ceiling up to nearest even number if not
	if hugepages%2 != 0 {
		hugepages++
	}

	// Apply max cap if configured (must be even — validated at Helm level)
	maxMiB := globalconfig.Config.ComputeMaxHugepagesMiB
	if maxMiB > 0 && hugepages > maxMiB {
		hugepages = maxMiB
	}

	logger.Debug("Calculated compute hugepages",
		"totalRawCapacityGiB", totalRawCapacityGiB,
		"hugepages", hugepages)

	return hugepages
}
