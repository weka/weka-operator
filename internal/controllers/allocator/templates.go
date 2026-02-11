package allocator

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

type ClusterTemplate struct {
	DriveCores                  int
	DriveExtraCores             int
	ComputeCores                int
	ComputeExtraCores           int
	EnvoyCores                  int
	S3Cores                     int
	S3ExtraCores                int
	NfsCores                    int
	NfsExtraCores               int
	ComputeContainers           int
	DriveContainers             int
	S3Containers                int
	NfsContainers               int
	NumDrives                   int
	DriveCapacity               int
	ContainerCapacity           int
	DriveTypesRatio             *v1alpha1.DriveTypesRatio
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
	DataServicesCores           int
	DataServicesExtraCores      int
	DataServicesContainers      int
	DataServicesHugepages       int
	DataServicesHugepagesOffset int
	SmbwCores                   int
	SmbwExtraCores              int
	SmbwContainers              int
	SmbwFrontendHugepages       int
	SmbwFrontendHugepagesOffset int
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

	if config.SmbwCores == 0 {
		config.SmbwCores = 1
	}

	if config.SmbwExtraCores == 0 {
		config.SmbwExtraCores = 1
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

	if config.SmbwFrontendHugepages == 0 {
		config.SmbwFrontendHugepages = 1400 * config.SmbwCores
	}

	if config.SmbwFrontendHugepagesOffset == 0 {
		config.SmbwFrontendHugepagesOffset = 200
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
		DriveCores:                  config.DriveCores,
		DriveExtraCores:             config.DriveExtraCores,
		ComputeCores:                config.ComputeCores,
		ComputeExtraCores:           config.ComputeExtraCores,
		ComputeContainers:           *config.ComputeContainers,
		DriveContainers:             *config.DriveContainers,
		S3Containers:                config.S3Containers,
		S3Cores:                     config.S3Cores,
		S3ExtraCores:                config.S3ExtraCores,
		NfsContainers:               config.NfsContainers,
		NumDrives:                   config.NumDrives,
		DriveCapacity:               config.DriveCapacity,
		ContainerCapacity:           config.ContainerCapacity,
		DriveTypesRatio:             config.DriveTypesRatio,
		DriveHugepages:              config.DriveHugepages,
		DriveHugepagesOffset:        config.DriveHugepagesOffset,
		ComputeHugepages:            config.ComputeHugepages,
		ComputeHugepagesOffset:      config.ComputeHugepagesOffset,
		S3FrontendHugepages:         config.S3FrontendHugepages,
		S3FrontendHugepagesOffset:   config.S3FrontendHugepagesOffset,
		HugePageSize:                hgSize,
		EnvoyCores:                  config.EnvoyCores,
		NfsCores:                    config.NfsCores,
		NfsExtraCores:               config.NfsExtraCores,
		NfsFrontendHugepages:        config.NfsFrontendHugepages,
		NfsFrontendHugepagesOffset:  config.NfsFrontendHugepagesOffset,
		DataServicesContainers:      config.DataServicesContainers,
		DataServicesCores:           config.DataServicesCores,
		DataServicesExtraCores:      config.DataServicesExtraCores,
		DataServicesHugepages:       config.DataServicesHugepages,
		DataServicesHugepagesOffset: config.DataServicesHugepagesOffset,
		SmbwContainers:              config.SmbwContainers,
		SmbwCores:                   config.SmbwCores,
		SmbwExtraCores:              config.SmbwExtraCores,
		SmbwFrontendHugepages:       config.SmbwFrontendHugepages,
		SmbwFrontendHugepagesOffset: config.SmbwFrontendHugepagesOffset,
	}

}

func GetTemplateByName(name string, cluster v1alpha1.WekaCluster) (ClusterTemplate, bool) {
	if name == "dynamic" {
		return BuildDynamicTemplate(cluster.Spec.Dynamic), true
	}

	template, ok := WekaClusterTemplates[name]
	return template, ok
}

var WekaClusterTemplates = map[string]ClusterTemplate{
	"small_s3": {
		DriveCores:          1,
		ComputeCores:        1,
		ComputeContainers:   6,
		DriveContainers:     6,
		S3Containers:        6,
		S3Cores:             1,
		S3ExtraCores:        1,
		NumDrives:           1,
		DriveHugepages:      1500,
		ComputeHugepages:    3000,
		S3FrontendHugepages: 1400,
		HugePageSize:        "2Mi",
		EnvoyCores:          1,
	},
	"small": {
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 6,
		DriveContainers:   6,
		NumDrives:         1,
		DriveHugepages:    1500,
		ComputeHugepages:  3000,
		HugePageSize:      "2Mi",
		EnvoyCores:        1,
	},
	"large": {
		DriveCores:        1,
		ComputeCores:      1,
		ComputeContainers: 20,
		DriveContainers:   20,
		NumDrives:         1,
		DriveHugepages:    1500,
		ComputeHugepages:  3000,
		HugePageSize:      "2Mi",
		EnvoyCores:        2,
		S3Containers:      5,
		S3Cores:           1,
		S3ExtraCores:      2,
	},
	"small_nfs": {
		DriveCores:           1,
		ComputeCores:         1,
		ComputeContainers:    6,
		DriveContainers:      6,
		NfsContainers:        2,
		NumDrives:            1,
		DriveHugepages:       1500,
		ComputeHugepages:     3000,
		NfsFrontendHugepages: 1200,
		HugePageSize:         "2Mi",
		EnvoyCores:           1,
	},
}
