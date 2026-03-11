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
		// Traditional mode without capacity in spec: try ready-cluster path first, fall back to node annotations
		readyClusterCap, readyErr := computeMaxNodeDriveCapacityForReadyCluster(containers, template.Containers.Drive, template.NumDrives)
		if readyErr == nil {
			// Ready-cluster path succeeded: result is already the total capacity
			totalRawCapacityGiB = readyClusterCap
		} else {
			// Fall back to node-annotation sampling
			driveNodeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)

			maxNodeCap, err := computeMaxNodeDriveCapacityForInitCluster(ctx, k8sClient, driveNodeSelector, template.Containers.Drive, template.NumDrives)
			if err != nil {
				return 0, fmt.Errorf("failed to compute node drive capacity: %w", err)
			}

			totalRawCapacityGiB = template.Containers.Drive * maxNodeCap
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

func ComputeTotalCapacityFromContainers(containers []*weka.WekaContainer, numDriveContainers, numDrives int) (int, error) {
	var goodContainersCapacitySumBytes int64
	goodContainersCount := 0
	for _, c := range containers {
		if !c.IsDriveContainer() || len(c.Status.AddedDrives) == 0 {
			continue
		}
		containerBytes := int64(0)
		addedContainerDrives := 0
		allHaveSize := true
		for _, drive := range c.Status.AddedDrives {
			if drive.SizeBytes == 0 || drive.Status != "ACTIVE" {
				allHaveSize = false
				break
			}
			containerBytes += drive.SizeBytes
			addedContainerDrives++
		}
		if !allHaveSize {
			continue
		}
		// make sure we only count containers that have the expected number of drives, to avoid underestimating capacity due to some containers not reporting drive sizes yet.
		if addedContainerDrives != numDrives {
			continue
		}

		goodContainersCapacitySumBytes += containerBytes
		goodContainersCount++
	}
	if goodContainersCount < 5 {
		return 0, fmt.Errorf("not enough drive containers with valid AddedDrives capacity (found %d, need at least 5)", goodContainersCount)
	}
	avgPerContainer := goodContainersCapacitySumBytes / int64(goodContainersCount)
	totalRawBytes := avgPerContainer * int64(numDriveContainers)
	return int(totalRawBytes / (1024 * 1024 * 1024)), nil
}

// if cluster is Ready, we don't need to fetch nodes using nodeSelector and compute capacity based
// on node annotations, because we already have real capacity info from the running drive containers.
func computeMaxNodeDriveCapacityForReadyCluster(containers []*weka.WekaContainer, numDriveContainers, numDrives int) (int, error) {
	return ComputeTotalCapacityFromContainers(containers, numDriveContainers, numDrives)
}
