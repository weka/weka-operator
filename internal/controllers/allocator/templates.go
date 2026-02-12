package allocator

import (
	"context"
	"fmt"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

// ClusterLayout contains container counts, cores, and drive configuration fields.
// It is returned by GetTemplateByName for callers that don't need hugepages.
type ClusterLayout struct {
	DriveCores             int
	DriveExtraCores        int
	ComputeCores           int
	ComputeExtraCores      int
	EnvoyCores             int
	S3Cores                int
	S3ExtraCores           int
	NfsCores               int
	NfsExtraCores          int
	ComputeContainers      int
	DriveContainers        int
	S3Containers           int
	NfsContainers          int
	NumDrives              int
	DriveCapacity          int
	ContainerCapacity      int
	DriveTypesRatio        *v1alpha1.DriveTypesRatio
	DataServicesCores      int
	DataServicesExtraCores int
	DataServicesContainers int
}

// ClusterTemplate embeds ClusterLayout and adds hugepages fields.
// It is returned by GetEnrichedTemplate for the container creation path.
type ClusterTemplate struct {
	ClusterLayout
	DriveHugepages              int
	DriveHugepagesOffset        int
	ComputeHugepages            int
	ComputeHugepagesOffset      int
	HugePageSize                string
	HugePagesOverride           string
	S3FrontendHugepages         int
	S3FrontendHugepagesOffset   int
	NfsFrontendHugepages        int
	NfsFrontendHugepagesOffset  int
	DataServicesHugepages       int
	DataServicesHugepagesOffset int
}

func BuildDynamicTemplate(config *v1alpha1.WekaConfig) ClusterTemplate {
	hgSize := "2Mi"

	if config.DriveCores == 0 {
		config.DriveCores = 1
	}

	if config.ComputeCores == 0 {
		config.ComputeCores = 1
	}

	if config.ComputeContainers == nil {
		config.ComputeContainers = util.IntRef(6)
	}

	if config.DriveContainers == nil {
		config.DriveContainers = util.IntRef(6)
	}

	if config.S3Cores == 0 {
		config.S3Cores = 1
	}

	if config.NfsCores == 0 {
		config.NfsCores = 1
	}

	if config.S3ExtraCores == 0 {
		config.S3ExtraCores = 1
	}

	if config.NfsExtraCores == 0 {
		config.NfsExtraCores = 1
	}

	if config.NumDrives == 0 && config.ContainerCapacity == 0 {
		config.NumDrives = 1
	}

	if config.DataServicesCores == 0 {
		config.DataServicesCores = 1
	}

	if config.DriveHugepages == 0 {
		if config.NumDrives > 0 {
			config.DriveHugepages = 1400*config.DriveCores + 200*config.NumDrives
		} else {
			config.DriveHugepages = 1600 * config.DriveCores

		}
	}

	if config.DriveHugepagesOffset == 0 {
		if config.NumDrives > 0 {
			config.DriveHugepagesOffset = 200 * config.NumDrives
		} else {
			config.DriveHugepagesOffset = 200 * config.DriveCores
		}
	}

	if config.ComputeHugepagesOffset == 0 {
		config.ComputeHugepagesOffset = 200
	}

	if config.S3FrontendHugepages == 0 {
		config.S3FrontendHugepages = 1400 * config.S3Cores
	}

	if config.NfsFrontendHugepages == 0 {
		config.NfsFrontendHugepages = 1400 * config.NfsCores
	}

	if config.S3FrontendHugepagesOffset == 0 {
		config.S3FrontendHugepagesOffset = 200
	}

	if config.NfsFrontendHugepagesOffset == 0 {
		config.NfsFrontendHugepagesOffset = 200
	}

	if config.DataServicesHugepages == 0 {
		config.DataServicesHugepages = 1536 // 1.5GB default
	}

	if config.DataServicesHugepagesOffset == 0 {
		config.DataServicesHugepagesOffset = 200
	}

	if config.EnvoyCores == 0 {
		config.EnvoyCores = 1
	}

	// Apply global default driveTypesRatio when using drive sharing
	// Drive sharing is enabled when containerCapacity > 0
	if config.DriveTypesRatio == nil && config.ContainerCapacity > 0 {
		ratio := globalconfig.Config.DriveSharing.DriveTypesRatio
		// Only apply if non-zero ratio is configured
		if ratio.Tlc > 0 || ratio.Qlc > 0 {
			config.DriveTypesRatio = &v1alpha1.DriveTypesRatio{
				Tlc: ratio.Tlc,
				Qlc: ratio.Qlc,
			}
		}
	}

	if config.ComputeHugepages == 0 {
		var totalRawCapacityGiB int
		if config.ContainerCapacity > 0 {
			totalRawCapacityGiB = *config.DriveContainers * config.ContainerCapacity
		} else if config.NumDrives > 0 && config.DriveCapacity > 0 {
			totalRawCapacityGiB = *config.DriveContainers * config.NumDrives * config.DriveCapacity
		}

		capacityBased := 0
		if *config.ComputeContainers > 0 && totalRawCapacityGiB > 0 {
			tlcCapGiB, qlcCapGiB := v1alpha1.GetTlcQlcCapacity(totalRawCapacityGiB, config.DriveTypesRatio)

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
			// The *1024 converts GiB capacity to MiB hugepages before applying ratio
			clusterHugepagesMiB := 0
			if hugepagesTlcRatio > 0 {
				clusterHugepagesMiB += tlcCapGiB * 1024 / hugepagesTlcRatio
			}
			if hugepagesQlcRatio > 0 {
				clusterHugepagesMiB += qlcCapGiB * 1024 / hugepagesQlcRatio
			}

			// Per-container capacity hugepages
			capacityBased = clusterHugepagesMiB / *config.ComputeContainers
		}

		perCoreComponent := 1700 * config.ComputeCores
		config.ComputeHugepages = capacityBased + perCoreComponent

		minHugepages := 3000 * config.ComputeCores
		config.ComputeHugepages = max(config.ComputeHugepages, minHugepages)
		// Must be devidable by 2, ceil-ing up to nearest even number if not:
		if config.ComputeHugepages%2 != 0 {
			config.ComputeHugepages++
		}
	}

	return ClusterTemplate{
		ClusterLayout: ClusterLayout{
			DriveCores:             config.DriveCores,
			DriveExtraCores:        config.DriveExtraCores,
			ComputeCores:           config.ComputeCores,
			ComputeExtraCores:      config.ComputeExtraCores,
			ComputeContainers:      *config.ComputeContainers,
			DriveContainers:        *config.DriveContainers,
			S3Containers:           config.S3Containers,
			S3Cores:                config.S3Cores,
			S3ExtraCores:           config.S3ExtraCores,
			NfsContainers:          config.NfsContainers,
			NumDrives:              config.NumDrives,
			DriveCapacity:          config.DriveCapacity,
			ContainerCapacity:      config.ContainerCapacity,
			DriveTypesRatio:        config.DriveTypesRatio,
			EnvoyCores:             config.EnvoyCores,
			NfsCores:               config.NfsCores,
			NfsExtraCores:          config.NfsExtraCores,
			DataServicesContainers: config.DataServicesContainers,
			DataServicesCores:      config.DataServicesCores,
			DataServicesExtraCores: config.DataServicesExtraCores,
		},
		DriveHugepages:              config.DriveHugepages,
		DriveHugepagesOffset:        config.DriveHugepagesOffset,
		ComputeHugepages:            config.ComputeHugepages,
		ComputeHugepagesOffset:      config.ComputeHugepagesOffset,
		S3FrontendHugepages:         config.S3FrontendHugepages,
		S3FrontendHugepagesOffset:   config.S3FrontendHugepagesOffset,
		HugePageSize:                hgSize,
		NfsFrontendHugepages:        config.NfsFrontendHugepages,
		NfsFrontendHugepagesOffset:  config.NfsFrontendHugepagesOffset,
		DataServicesHugepages:       config.DataServicesHugepages,
		DataServicesHugepagesOffset: config.DataServicesHugepagesOffset,
	}

}

// computeHugepagesForCompute calculates compute hugepages from cores and raw capacity.
func computeHugepagesForCompute(computeCores, totalRawCapacityGiB, computeContainers int) int {
	capacityComponent := 0
	if computeContainers > 0 && totalRawCapacityGiB > 0 {
		capacityComponent = totalRawCapacityGiB / computeContainers
	}
	perCoreComponent := 1700 * computeCores
	minHugepages := 3000 * computeCores
	return max(capacityComponent+perCoreComponent, minHugepages)
}

// GetTemplateByName returns the ClusterLayout (no hugepages) for the named template.
// Use GetEnrichedTemplate when hugepages fields are needed.
func GetTemplateByName(name string, cluster v1alpha1.WekaCluster) (ClusterLayout, bool) {
	if name == "dynamic" {
		return BuildDynamicTemplate(cluster.Spec.Dynamic).ClusterLayout, true
	}

	template, ok := WekaClusterTemplates[name]
	return template.ClusterLayout, ok
}

// GetEnrichedTemplate returns a full ClusterTemplate (including hugepages) for the named template.
// For dynamic templates, it enriches compute hugepages with actual drive capacity from node
// annotations when the spec doesn't provide capacity info (traditional drive mode).
func GetEnrichedTemplate(ctx context.Context, k8sClient client.Client, name string, cluster v1alpha1.WekaCluster) (*ClusterTemplate, error) {
	if name != "dynamic" {
		template, ok := WekaClusterTemplates[name]
		if !ok {
			return nil, nil
		}
		return &template, nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil, nil
	}

	tmpl := BuildDynamicTemplate(cluster.Spec.Dynamic)

	// Enrich compute hugepages with actual drive capacity from nodes
	// when the spec doesn't provide capacity info (traditional drive mode)
	if tmpl.ContainerCapacity == 0 && tmpl.DriveCapacity == 0 &&
		tmpl.ComputeHugepages == 3000*tmpl.ComputeCores {

		maxNodeCap, err := computeMaxNodeDriveCapacity(ctx, k8sClient, cluster.Spec.NodeSelector, tmpl.NumDrives)
		if err != nil {
			return nil, fmt.Errorf("failed to compute node drive capacity: %w", err)
		}
		if maxNodeCap > 0 {
			totalRawCapacity := tmpl.DriveContainers * maxNodeCap
			tmpl.ComputeHugepages = computeHugepagesForCompute(
				tmpl.ComputeCores, totalRawCapacity, tmpl.ComputeContainers)
		}
	}

	return &tmpl, nil
}

var WekaClusterTemplates = map[string]ClusterTemplate{
	"small_s3": {
		ClusterLayout: ClusterLayout{
			DriveCores:        1,
			ComputeCores:      1,
			ComputeContainers: 6,
			DriveContainers:   6,
			S3Containers:      6,
			S3Cores:           1,
			S3ExtraCores:      1,
			NumDrives:         1,
			EnvoyCores:        1,
		},
		DriveHugepages:      1500,
		ComputeHugepages:    3000,
		S3FrontendHugepages: 1400,
		HugePageSize:        "2Mi",
	},
	"small": {
		ClusterLayout: ClusterLayout{
			DriveCores:        1,
			ComputeCores:      1,
			ComputeContainers: 6,
			DriveContainers:   6,
			NumDrives:         1,
			EnvoyCores:        1,
		},
		DriveHugepages:   1500,
		ComputeHugepages: 3000,
		HugePageSize:     "2Mi",
	},
	"large": {
		ClusterLayout: ClusterLayout{
			DriveCores:        1,
			ComputeCores:      1,
			ComputeContainers: 20,
			DriveContainers:   20,
			NumDrives:         1,
			EnvoyCores:        2,
			S3Containers:      5,
			S3Cores:           1,
			S3ExtraCores:      2,
		},
		DriveHugepages:   1500,
		ComputeHugepages: 3000,
		HugePageSize:     "2Mi",
	},
	"small_nfs": {
		ClusterLayout: ClusterLayout{
			DriveCores:        1,
			ComputeCores:      1,
			ComputeContainers: 6,
			DriveContainers:   6,
			NfsContainers:     2,
			NumDrives:         1,
			EnvoyCores:        1,
		},
		DriveHugepages:       1500,
		ComputeHugepages:     3000,
		NfsFrontendHugepages: 1200,
		HugePageSize:         "2Mi",
	},
}
