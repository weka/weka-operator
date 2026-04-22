package allocator

import (
	"context"
	"fmt"
	"slices"

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
	Mode            weka.WekaContainerMode
	Hugepages       int
	HugepagesOffset int
	HugePageSize    string
	// Flags to indicate whether hugepages values were explicitly set by the user (vs auto-calculated by the operator).
	HugepagesUserSet       bool
	HugepagesOffsetUserSet bool
	// For reference, the DPDK base memory is tracked here but not added to hugepages when user-set values are provided.
	DpdkBaseMemoryMb int
}

func (c *ContainerHugepages) ShouldPropagateHugepages() bool {
	if c.HugepagesUserSet {
		return true
	}
	if globalconfig.Config.HugepagesUpdate.Compute && c.Mode == weka.WekaContainerModeCompute {
		return true
	}
	if globalconfig.Config.HugepagesUpdate.Drive && c.Mode == weka.WekaContainerModeDrive {
		return true
	}
	if slices.Contains([]weka.WekaContainerMode{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute}, c.Mode) {
		return false
	}
	return true
}

func (c *ContainerHugepages) ShouldPropagateHugepagesOffset() bool {
	if c.HugepagesOffsetUserSet {
		return true
	}
	if globalconfig.Config.HugepagesUpdate.Compute && c.Mode == weka.WekaContainerModeCompute {
		return true
	}
	if globalconfig.Config.HugepagesUpdate.Drive && c.Mode == weka.WekaContainerModeDrive {
		return true
	}
	if slices.Contains([]weka.WekaContainerMode{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute}, c.Mode) {
		return false
	}
	return true
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

// GetWekaClusterTemplate builds cluster ClusterTemplate from config, setting defaults for container
// counts and cores. Does not include hugepages, which are computed separately.
func GetWekaClusterTemplate(config *weka.WekaClusterTemplate) ClusterTemplate {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	// if we don't set numDrives or containerCapacity, default to 1 drive (full-drives mode with 1 drive per container)
	if config.NumDrives == 0 && config.ContainerCapacity == 0 {
		config.NumDrives = 1
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

func GetContainerHugepages(ctx context.Context, k8sClient client.Client, template ClusterTemplate, cluster *weka.WekaCluster, containers []*weka.WekaContainer, role string) (ContainerHugepages, error) { //nolint:gocritic // hugeParam: ClusterTemplate is passed by value intentionally
	hp := ContainerHugepages{
		HugePageSize: "2Mi",
		Mode:         weka.WekaContainerMode(role),
	}

	dynamicTemplate := cluster.Spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}

	// Track whether the user explicitly set hugepages/offset for this role.
	// When user-set, the value already represents the total (weka + DPDK), so
	// dpdkTotalMemory must NOT be added. When auto-calculated, it covers only
	// weka-process memory and DPDK must be added to reach the total.

	var numCores int
	switch role {
	case "envoy", "telemetry":
		return hp, nil // Envoy and telemetry containers don't require hugepages
	case "drive":
		if dynamicTemplate.DriveHugepages > 0 {
			hp.Hugepages = dynamicTemplate.DriveHugepages
			hp.HugepagesUserSet = true
		} else {
			hp.Hugepages = CalculateDriveHugepages(template)
		}
		if dynamicTemplate.DriveHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.DriveHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = CalculateDriveHugepagesOffset(template)
		}
		numCores = template.Cores.Drive
	case "compute":
		if dynamicTemplate.ComputeHugepages == 0 {
			hpComputed, err := calculateDynamicComputeHugepages(ctx, k8sClient, template, cluster, containers)
			if err != nil {
				return hp, fmt.Errorf("failed to calculate dynamic compute hugepages: %w", err)
			}
			hp.Hugepages = hpComputed
		} else {
			hp.Hugepages = dynamicTemplate.ComputeHugepages
			hp.HugepagesUserSet = true
		}
		if dynamicTemplate.ComputeHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.ComputeHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Compute
	case "s3":
		if dynamicTemplate.S3FrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.S3FrontendHugepages
			hp.HugepagesUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.S3
		}
		if dynamicTemplate.S3FrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.S3FrontendHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.S3
	case "nfs":
		if dynamicTemplate.NfsFrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.NfsFrontendHugepages
			hp.HugepagesUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.Nfs
		}
		if dynamicTemplate.NfsFrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.NfsFrontendHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Nfs
	case "smbw":
		if dynamicTemplate.SmbwFrontendHugepages > 0 {
			hp.Hugepages = dynamicTemplate.SmbwFrontendHugepages
			hp.HugepagesUserSet = true
		} else {
			hp.Hugepages = 1400 * template.Cores.Smbw
		}
		if dynamicTemplate.SmbwFrontendHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.SmbwFrontendHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.Smbw
	case "data-services":
		if dynamicTemplate.DataServicesHugepages > 0 {
			hp.Hugepages = dynamicTemplate.DataServicesHugepages
			hp.HugepagesUserSet = true
		} else {
			hp.Hugepages = 1536 // 1.5GB default
		}
		if dynamicTemplate.DataServicesHugepagesOffset > 0 {
			hp.HugepagesOffset = dynamicTemplate.DataServicesHugepagesOffset
			hp.HugepagesOffsetUserSet = true
		} else {
			hp.HugepagesOffset = 200
		}
		numCores = template.Cores.DataServices
	}

	// Add DPDK base memory only to auto-calculated values. User-set values
	// already represent the total hugepages allocation (weka + DPDK).
	dpdkBaseMemoryMb := utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, role)
	dpdkTotalMemory := dpdkBaseMemoryMb * numCores
	if !hp.HugepagesUserSet {
		hp.Hugepages += dpdkTotalMemory
	}
	if !hp.HugepagesOffsetUserSet {
		hp.HugepagesOffset += dpdkTotalMemory
	}

	hp.DpdkBaseMemoryMb = dpdkBaseMemoryMb

	return hp, nil
}
