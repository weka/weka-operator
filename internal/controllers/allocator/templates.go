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

// BuildDynamicTemplate builds cluster layout (structure) from config, setting defaults for container
// counts and cores. Does not compute hugepages - use GetEnrichedTemplate for full ClusterTemplate
// with hugepages calculated.
func BuildDynamicTemplate(config *v1alpha1.WekaConfig) ClusterLayout {
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

	return ClusterLayout{
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
	}

}

// ComputeCapacityBasedHugepages calculates compute hugepages using TLC/QLC-aware capacity ratios.
func ComputeCapacityBasedHugepages(totalRawCapacityGiB, computeContainers, computeCores int, driveTypesRatio *v1alpha1.DriveTypesRatio) int {
	capacityBased := 0
	if computeContainers > 0 && totalRawCapacityGiB > 0 {
		tlcCapGiB, qlcCapGiB := v1alpha1.GetTlcQlcCapacity(totalRawCapacityGiB, driveTypesRatio)

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

	return hugepages
}

// GetTemplateByName returns the ClusterLayout (no hugepages) for the named template.
// Use GetEnrichedTemplate when hugepages fields are needed.
func GetTemplateByName(name string, cluster v1alpha1.WekaCluster) (ClusterLayout, bool) {
	if name == "dynamic" {
		return BuildDynamicTemplate(cluster.Spec.Dynamic), true
	}

	template, ok := WekaClusterTemplates[name]
	return template.ClusterLayout, ok
}

// GetEnrichedTemplate returns a full ClusterTemplate (including hugepages) for the named template.
// For dynamic templates, it computes all hugepages defaults for fields not explicitly set by the user.
// ComputeHugepages is capacity-based when drive capacity is available, otherwise falls back to minimum.
// For traditional mode without explicit capacity, it reads drive capacity from node annotations.
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

	layout := BuildDynamicTemplate(cluster.Spec.Dynamic)
	spec := cluster.Spec.Dynamic

	// Initialize template with layout and set hugepages defaults for fields not explicitly set by user
	tmpl := &ClusterTemplate{
		ClusterLayout: layout,
		HugePageSize:  "2Mi",
	}

	// Drive hugepages
	if spec.DriveHugepages == 0 {
		if layout.NumDrives > 0 {
			tmpl.DriveHugepages = 1400*layout.DriveCores + 200*layout.NumDrives
		} else {
			tmpl.DriveHugepages = 1600 * layout.DriveCores
		}
	} else {
		tmpl.DriveHugepages = spec.DriveHugepages
	}

	if spec.DriveHugepagesOffset == 0 {
		if layout.NumDrives > 0 {
			tmpl.DriveHugepagesOffset = 200 * layout.NumDrives
		} else {
			tmpl.DriveHugepagesOffset = 200 * layout.DriveCores
		}
	} else {
		tmpl.DriveHugepagesOffset = spec.DriveHugepagesOffset
	}

	// Compute hugepages (capacity-based)
	if spec.ComputeHugepages == 0 {
		var totalRawCapacityGiB int
		if layout.ContainerCapacity > 0 {
			// Drive-sharing mode: capacity per container × number of drive containers
			totalRawCapacityGiB = layout.DriveContainers * layout.ContainerCapacity
		} else if layout.NumDrives > 0 && layout.DriveCapacity > 0 {
			// Traditional mode with explicit drive capacity in spec
			totalRawCapacityGiB = layout.DriveContainers * layout.NumDrives * layout.DriveCapacity
		} else if layout.NumDrives > 0 {
			// Traditional mode without capacity in spec: read from node annotations
			maxNodeCap, err := computeMaxNodeDriveCapacity(ctx, k8sClient, cluster.Spec.NodeSelector, layout.NumDrives)
			if err != nil {
				return nil, fmt.Errorf("failed to compute node drive capacity: %w", err)
			}
			totalRawCapacityGiB = layout.DriveContainers * maxNodeCap
		}

		if totalRawCapacityGiB > 0 {
			tmpl.ComputeHugepages = ComputeCapacityBasedHugepages(
				totalRawCapacityGiB, layout.ComputeContainers, layout.ComputeCores, layout.DriveTypesRatio)
		} else {
			// Fallback minimum when capacity is unknown
			tmpl.ComputeHugepages = 3000 * layout.ComputeCores
		}
	} else {
		tmpl.ComputeHugepages = spec.ComputeHugepages
	}

	if spec.ComputeHugepagesOffset == 0 {
		tmpl.ComputeHugepagesOffset = 200
	} else {
		tmpl.ComputeHugepagesOffset = spec.ComputeHugepagesOffset
	}

	// S3 frontend hugepages
	if spec.S3FrontendHugepages == 0 {
		tmpl.S3FrontendHugepages = 1400 * layout.S3Cores
	} else {
		tmpl.S3FrontendHugepages = spec.S3FrontendHugepages
	}

	if spec.S3FrontendHugepagesOffset == 0 {
		tmpl.S3FrontendHugepagesOffset = 200
	} else {
		tmpl.S3FrontendHugepagesOffset = spec.S3FrontendHugepagesOffset
	}

	// NFS frontend hugepages
	if spec.NfsFrontendHugepages == 0 {
		tmpl.NfsFrontendHugepages = 1400 * layout.NfsCores
	} else {
		tmpl.NfsFrontendHugepages = spec.NfsFrontendHugepages
	}

	if spec.NfsFrontendHugepagesOffset == 0 {
		tmpl.NfsFrontendHugepagesOffset = 200
	} else {
		tmpl.NfsFrontendHugepagesOffset = spec.NfsFrontendHugepagesOffset
	}

	// Data services hugepages
	if spec.DataServicesHugepages == 0 {
		tmpl.DataServicesHugepages = 1536 // 1.5GB default
	} else {
		tmpl.DataServicesHugepages = spec.DataServicesHugepages
	}

	if spec.DataServicesHugepagesOffset == 0 {
		tmpl.DataServicesHugepagesOffset = 200
	} else {
		tmpl.DataServicesHugepagesOffset = spec.DataServicesHugepagesOffset
	}

	return tmpl, nil
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
