package allocator

import (
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/util"
)

type ClusterTemplate struct {
	DriveCores          int
	ComputeCores        int
	EnvoyCores          int
	S3Cores             int
	S3ExtraCores        int
	ComputeContainers   int
	DriveContainers     int
	S3Containers        int
	NumDrives           int
	DriveHugepages      int
	ComputeHugepages    int
	HugePageSize        string
	HugePagesOverride   string
	S3FrontendHugepages int
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

	if config.S3ExtraCores == 0 {
		config.S3ExtraCores = 1
	}

	if config.NumDrives == 0 {
		config.NumDrives = 1
	}

	if config.DriveHugepages == 0 {
		config.DriveHugepages = 1500 * config.DriveCores
	}

	if config.ComputeHugepages == 0 {
		config.ComputeHugepages = 3000 * config.ComputeCores
	}

	if config.S3FrontendHugepages == 0 {
		config.S3FrontendHugepages = 1400 * config.S3Cores
	}

	if config.EnvoyCores == 0 {
		config.EnvoyCores = 1
	}

	return ClusterTemplate{
		DriveCores:          config.DriveCores,
		ComputeCores:        config.ComputeCores,
		ComputeContainers:   *config.ComputeContainers,
		DriveContainers:     *config.DriveContainers,
		S3Containers:        config.S3Containers,
		S3Cores:             config.S3Cores,
		S3ExtraCores:        config.S3ExtraCores,
		NumDrives:           config.NumDrives,
		DriveHugepages:      config.DriveHugepages,
		ComputeHugepages:    config.ComputeHugepages,
		S3FrontendHugepages: config.S3FrontendHugepages,
		HugePageSize:        hgSize,
		EnvoyCores:          config.EnvoyCores,
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
}
