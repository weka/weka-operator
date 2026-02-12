package allocator

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

type IntPerWekaRole struct {
	Compute      int
	Drive        int
	S3           int
	Envoy        int
	Nfs          int
	DataServices int
}

// ClusterTemplate contains container counts, cores, and drive configuration fields.
type ClusterTemplate struct {
	Containers        IntPerWekaRole
	Cores             IntPerWekaRole
	ExtraCores        IntPerWekaRole
	NumDrives         int
	DriveCapacity     int
	ContainerCapacity int
	DriveTypesRatio   *weka.DriveTypesRatio
}

type ContainerHugepages struct {
	Hugepages       int
	HugepagesOffset int
	HugePageSize    string
}

func GetWekaContainerNumbers(config *weka.WekaClusterTemplate) IntPerWekaRole {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	numbers := IntPerWekaRole{
		Compute:      config.ComputeContainers,
		Drive:        config.DriveContainers,
		S3:           config.S3Containers,
		Nfs:          config.NfsContainers,
		DataServices: config.DataServicesContainers,
	}

	if numbers.Compute == 0 {
		numbers.Compute = globalconfig.Consts.FormClusterMinComputeContainers
	}

	if numbers.Drive == 0 {
		numbers.Drive = globalconfig.Consts.FormClusterMinDriveContainers
	}

	return numbers
}

func GetWekaContainerCores(config *weka.WekaClusterTemplate) IntPerWekaRole {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	return IntPerWekaRole{
		Compute:      util.GetNonZeroOrDefault(config.ComputeCores, 1),
		Drive:        util.GetNonZeroOrDefault(config.DriveCores, 1),
		S3:           util.GetNonZeroOrDefault(config.S3Cores, 1),
		Nfs:          util.GetNonZeroOrDefault(config.NfsCores, 1),
		DataServices: util.GetNonZeroOrDefault(config.DataServicesCores, 1),
		Envoy:        util.GetNonZeroOrDefault(config.EnvoyCores, 1),
	}
}

func GetWekaContainerExtraCores(config *weka.WekaClusterTemplate) IntPerWekaRole {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	return IntPerWekaRole{
		Compute:      config.ComputeExtraCores,
		Drive:        config.DriveExtraCores,
		S3:           util.GetNonZeroOrDefault(config.S3ExtraCores, 1),
		Nfs:          util.GetNonZeroOrDefault(config.NfsExtraCores, 1),
		DataServices: config.DataServicesExtraCores,
	}
}

func GetDriveTypesRatio(config *weka.WekaClusterTemplate) *weka.DriveTypesRatio {
	// Apply global default driveTypesRatio when using drive sharing
	// Drive sharing is enabled when containerCapacity > 0
	if config != nil && config.DriveTypesRatio == nil && config.ContainerCapacity > 0 {
		ratio := globalconfig.Config.DriveSharing.DriveTypesRatio
		// Only apply if non-zero ratio is configured
		if ratio.Tlc > 0 || ratio.Qlc > 0 {
			return &weka.DriveTypesRatio{
				Tlc: ratio.Tlc,
				Qlc: ratio.Qlc,
			}
		}
	}

	return nil
}

// GetWekaClusterTemplate builds cluster ClusterTemplate from config, setting defaults for container
// counts and cores. Does not include hugepages, which are computed separately.
func GetWekaClusterTemplate(config *weka.WekaClusterTemplate) ClusterTemplate {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	return ClusterTemplate{
		Containers:        GetWekaContainerNumbers(config),
		Cores:             GetWekaContainerCores(config),
		ExtraCores:        GetWekaContainerExtraCores(config),
		NumDrives:         config.NumDrives,
		DriveCapacity:     config.DriveCapacity,
		ContainerCapacity: config.ContainerCapacity,
		DriveTypesRatio:   config.DriveTypesRatio,
	}
}

func GetContainerHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster, role string) (*ContainerHugepages, error) {
	hp := &ContainerHugepages{
		HugePageSize: "2Mi",
	}

	dynamicTemplate := cluster.Spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}

	switch role {
	case "drive":
		hp.Hugepages = util.GetNonZeroOrDefault(
			dynamicTemplate.DriveHugepages,
			calculateDriveHugepages(template),
		)
		hp.HugepagesOffset = util.GetNonZeroOrDefault(
			dynamicTemplate.DriveHugepagesOffset,
			calculateDriveHugepagesOffset(template),
		)
	case "compute":
		if dynamicTemplate.ComputeHugepages == 0 {
			hpComputed, err := calculateDynamicComputeHugepages(ctx, k8sClient, template, cluster)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate dynamic compute hugepages: %w", err)
			}
			hp.Hugepages = hpComputed
		} else {
			hp.Hugepages = dynamicTemplate.ComputeHugepages
		}
		hp.HugepagesOffset = util.GetNonZeroOrDefault(
			dynamicTemplate.ComputeHugepagesOffset,
			200,
		)
	case "s3":
		hp.Hugepages = util.GetNonZeroOrDefault(
			dynamicTemplate.S3FrontendHugepages,
			1400*template.Cores.S3,
		)
		hp.HugepagesOffset = util.GetNonZeroOrDefault(
			dynamicTemplate.S3FrontendHugepagesOffset,
			200,
		)
	case "nfs":
		hp.Hugepages = util.GetNonZeroOrDefault(
			dynamicTemplate.NfsFrontendHugepages,
			1400*template.Cores.Nfs,
		)
		hp.HugepagesOffset = util.GetNonZeroOrDefault(
			dynamicTemplate.NfsFrontendHugepagesOffset,
			200,
		)
	case "data-services":
		hp.Hugepages = util.GetNonZeroOrDefault(
			dynamicTemplate.DataServicesHugepages,
			1536, // 1.5GB default
		)
		hp.HugepagesOffset = util.GetNonZeroOrDefault(
			dynamicTemplate.DataServicesHugepagesOffset,
			200,
		)
	}

	return hp, nil
}
