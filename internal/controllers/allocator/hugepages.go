package allocator

import (
	"context"
	"errors"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

func calculateDriveHugepages(template ClusterTemplate) int {
	if template.NumDrives > 0 {
		return 1400*template.Cores.Drive + 200*template.NumDrives
	} else {
		return 1600 * template.Cores.Drive
	}
}

func calculateDriveHugepagesOffset(template ClusterTemplate) int {
	if template.NumDrives > 0 {
		return 200 * template.NumDrives
	} else {
		return 200 * template.Cores.Drive
	}
}

// Compute hugepages (capacity-based)
func calculateDynamicComputeHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster) (hp int, err error) {
	var totalRawCapacityGiB int

	if template.ContainerCapacity > 0 {
		// Drive-sharing mode - full capacity per drive container is known
		totalRawCapacityGiB = template.ContainerCapacity * template.Containers.Drive
	} else if template.NumDrives > 0 && template.DriveCapacity > 0 {
		// Drive-sharing mode with explicit drive count and capacity
		totalRawCapacityGiB = template.NumDrives * template.DriveCapacity * template.Containers.Drive
	} else if template.Containers.Drive > 0 {
		// Traditional mode without capacity in spec: read from node annotations
		driveNodeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)

		maxNodeCap, err := computeMaxNodeDriveCapacity(ctx, k8sClient, driveNodeSelector, template.NumDrives)
		if err != nil {
			return 0, fmt.Errorf("failed to compute node drive capacity: %w", err)
		}

		totalRawCapacityGiB = template.Containers.Drive * maxNodeCap
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

	logger.Info("Calculated compute hugepages",
		"totalRawCapacityGiB", totalRawCapacityGiB,
		"hugepages", hugepages)

	return hugepages
}
