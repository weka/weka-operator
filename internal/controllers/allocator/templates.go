package allocator

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/pkg/util"
)

type IntPerWekaRole struct {
	Compute      int
	Drive        int
	S3           int
	Envoy        int
	Nfs          int
	Smbw         int
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
		Smbw:         config.SmbwContainers,
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
		Smbw:         util.GetNonZeroOrDefault(config.SmbwCores, 1),
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
		Smbw:         util.GetNonZeroOrDefault(config.SmbwExtraCores, 1),
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

func GetContainerHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster, containers []*weka.WekaContainer, role string) (*ContainerHugepages, error) {
	hp := &ContainerHugepages{
		HugePageSize: "2Mi",
	}

	dynamicTemplate := cluster.Spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}

	// Track whether the user explicitly set hugepages/offset for this role.
	// When user-set, the value already represents the total (weka + DPDK), so
	// dpdkTotalMemory must NOT be added. When auto-calculated, it covers only
	// weka-process memory and DPDK must be added to reach the total.
	var hugepagesIsUserSet, hugepagesOffsetIsUserSet bool

	var numCores int
	switch role {
	case "envoy", "telemetry":
		return hp, nil // Envoy and telemetry containers don't require hugepages
	case "drive":
		if dynamicTemplate.DriveHugepages > 0 {
			hp.Hugepages = dynamicTemplate.DriveHugepages
			hugepagesIsUserSet = true
		} else {
			hp.Hugepages = CalculateDriveHugepages(template)
		}
		if dynamicTemplate.DriveHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.DriveHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = CalculateDriveHugepagesOffset(template)
		}
		numCores = template.Cores.Drive
	case "compute":
		if dynamicTemplate.ComputeHugepages == 0 {
			hpComputed, err := calculateDynamicComputeHugepages(ctx, k8sClient, template, cluster, containers)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate dynamic compute hugepages: %w", err)
			}
			hp.Hugepages = hpComputed
		} else {
			hp.Hugepages = dynamicTemplate.ComputeHugepages
			hugepagesIsUserSet = true
		}
		if dynamicTemplate.ComputeHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.ComputeHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Compute
	case "s3":
		if dynamicTemplate.S3FrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.S3FrontendHugepages
			hugepagesIsUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.S3
		}
		if dynamicTemplate.S3FrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.S3FrontendHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.S3
	case "nfs":
		if dynamicTemplate.NfsFrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.NfsFrontendHugepages
			hugepagesIsUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.Nfs
		}
		if dynamicTemplate.NfsFrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.NfsFrontendHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Nfs
	case "smbw":
		if dynamicTemplate.SmbwFrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.SmbwFrontendHugepages
			hugepagesIsUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.Smbw
		}
		if dynamicTemplate.SmbwFrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.SmbwFrontendHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Smbw
	case "data-services":
		if dynamicTemplate.DataServicesHugepages > 0 {
			hp.Hugepages = dynamicTemplate.DataServicesHugepages
			hugepagesIsUserSet = true
		} else {
			hp.Hugepages = 1536 // 1.5GB default
		}
		if dynamicTemplate.DataServicesHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.DataServicesHugepagesOffset
			hugepagesOffsetIsUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.DataServices
	}

	// Add DPDK base memory only to auto-calculated values. User-set values
	// already represent the total hugepages allocation (weka + DPDK).
	dpdkBaseMemoryMb := utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, role)
	dpdkTotalMemory := dpdkBaseMemoryMb * numCores
	if !hugepagesIsUserSet {
		hp.Hugepages += dpdkTotalMemory
	}
	if !hugepagesOffsetIsUserSet {
		hp.HugepagesOffset += dpdkTotalMemory
	}

	return hp, nil
}
