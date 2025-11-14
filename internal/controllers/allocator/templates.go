package allocator

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"
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

	if config.NumDrives == 0 {
		config.NumDrives = 1
	}

	if config.DataServicesCores == 0 {
		config.DataServicesCores = 1
	}

	if config.DriveHugepages == 0 {
		config.DriveHugepages = 1400*config.DriveCores + 200*config.NumDrives
	}

	if config.DriveHugepagesOffset == 0 {
		config.DriveHugepagesOffset = 200 * config.NumDrives
	}

	if config.ComputeHugepages == 0 {
		config.ComputeHugepages = 3000 * config.ComputeCores
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
