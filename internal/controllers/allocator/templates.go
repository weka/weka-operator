package allocator

import (
	"context"
	"fmt"
	"slices"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
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

// GetWekaContainerNumbers returns the per-role container counts from config's own count-based fields
// (ComputeContainers/DriveContainers/...), falling back to FormClusterMinComputeContainers/
// FormClusterMinDriveContainers when unset. That floor still fires under planner-managed templates,
// where the values are placeholders only; funcs_clusterization.go/funcs_upgrade.go rely on them staying non-zero.
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
		Drive:        getDriveCores(config),
		S3:           util.GetNonZeroOrDefault(config.S3Cores, 1),
		Nfs:          util.GetNonZeroOrDefault(config.NfsCores, 1),
		Smbw:         util.GetNonZeroOrDefault(config.SmbwCores, 1),
		DataServices: util.GetNonZeroOrDefault(config.DataServicesCores, 1),
		Envoy:        util.GetNonZeroOrDefault(config.EnvoyCores, 1),
	}
}

// getDriveCores returns the drive-core count. An explicit config.DriveCores always wins outright and is
// never raised, lowered, or clamped by capacity-based derivation — a value below capacity now surfaces
// as an admission warning (clusterDriveCoresBelowCapacity) instead of being silently overridden. When
// unset (0), cores are derived from capacity via DerivedDriveCores.
func getDriveCores(config *weka.WekaClusterTemplate) int {
	if config.DriveCores > 0 {
		return config.DriveCores
	}
	if derived, ok := DerivedDriveCores(config); ok {
		return derived
	}
	return 1
}

// RequiredDriveCoresForTemplate returns the drive-core count the template's capacity basis actually
// requires, ignoring config.DriveCores and applying no cap; ok is false with no capacity basis (pure
// full-drives, or planner-assigned clusterCapacity/auto-full-drives). No NumDrives-only branch: numDrives
// decouples from cores, so numDrives without driveCapacity falls through to 0, false.
func RequiredDriveCoresForTemplate(config *weka.WekaClusterTemplate) (cores int, ok bool) {
	if config == nil {
		return 0, false
	}
	if config.ContainerCapacity > 0 {
		tlcGiB, qlcGiB := weka.GetTlcQlcCapacity(config.ContainerCapacity, config.DriveTypesRatio)
		return capacityplanner.RequiredDriveCores(tlcGiB, qlcGiB, CapacityConstraintsFromConfig()), true
	}
	if config.NumDrives > 0 && config.DriveCapacity > 0 {
		// numDrives+driveCapacity is TLC-only (mirrors DriveContainerCapacities in
		// internal/capacityplanner/inventory/collect.go): driveCapacity is per-drive GiB, so total
		// TLC capacity is driveCapacity*numDrives.
		tlcGiB := config.DriveCapacity * config.NumDrives
		return capacityplanner.RequiredDriveCores(tlcGiB, 0, CapacityConstraintsFromConfig()), true
	}
	return 0, false
}

// DerivedDriveCores is RequiredDriveCoresForTemplate capped at what the template can actually be
// assigned, for callers that need a core count to hand the reconciler rather than a requirement to
// check. Callers testing whether the configured capacity is reachable at all must use the uncapped
// figure — the cap makes an unreachable capacity look satisfied.
func DerivedDriveCores(config *weka.WekaClusterTemplate) (cores int, ok bool) {
	required, ok := RequiredDriveCoresForTemplate(config)
	if !ok {
		return 0, false
	}
	if config.NumDrives > 0 && config.DriveCapacity > 0 {
		// CEL (wekacluster_types.go) requires numDrives >= driveCores here, since each drive core needs
		// at least one virtual drive. Capping only clamps our derived value, never an explicit user
		// setting; when the requirement exceeds numDrives, clusterNumDrivesBelowRequiredCores reports the
		// unreachable capacity instead of letting the cap bury it.
		return min(required, config.NumDrives), true
	}
	return required, true
}

func GetDefaultDataServicesExtraCores(config *weka.WekaClusterTemplate) int {
	if config.DataServicesExtraCores != nil {
		return *config.DataServicesExtraCores
	}
	if config.GetDataServicesFeCores() > 0 {
		return 4
	}
	return 0
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
		DataServices: GetDefaultDataServicesExtraCores(config),
	}
}

// IsPlannerManaged reports whether the capacity planner (clusterCapacity or auto full drives) owns this
// template's drive/compute container sizing, rather than the template's own count-based fields. A nil
// config is treated as planner-managed (auto full drives / daemonset mode): UsesAutoFullDrives already
// returns true on a nil receiver, matching a nil *weka.WekaClusterTemplate to "nothing set".
func IsPlannerManaged(config *weka.WekaClusterTemplate) bool {
	return config.UsesClusterCapacity() || config.UsesAutoFullDrives()
}

// GetWekaClusterTemplate builds cluster ClusterTemplate from config, setting defaults for container
// counts and cores. Does not include hugepages, which are computed separately.
func GetWekaClusterTemplate(config *weka.WekaClusterTemplate) ClusterTemplate {
	if config == nil {
		config = &weka.WekaClusterTemplate{}
	}

	// Default to 1 drive (full-drives mode) unless planner-managed, where drive counts are
	// planner-assigned (clusterCapacity) or per-node signed drives (auto full drives), not count-based.
	if config.NumDrives == 0 && config.ContainerCapacity == 0 && !IsPlannerManaged(config) {
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

// ComputeHugepagesFromPlan builds compute-role ContainerHugepages from a capacity-planner-supplied
// per-container figure, bypassing GetContainerHugepages because that path needs every drive container's
// Status.Allocations (not yet populated at compute-creation time). The planner's figure already includes
// DPDK, so only the offset still adds it; an explicit computeHugepages/computeHugepagesOffset overrides.
func ComputeHugepagesFromPlan(cluster *weka.WekaCluster, plannedHugepagesMiB, cores int) ContainerHugepages {
	dynamicTemplate := cluster.Spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}
	dpdkBaseMemoryMb := utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeCompute)

	hp := ContainerHugepages{
		HugePageSize:     "2Mi",
		Mode:             weka.WekaContainerMode(weka.WekaContainerModeCompute),
		DpdkBaseMemoryMb: dpdkBaseMemoryMb,
		// HugepagesUserSet marks the value as an already-complete total (weka + DPDK) — which the
		// planner's figure is — so any later consumer of this struct does not add DPDK on top.
		Hugepages:        plannedHugepagesMiB,
		HugepagesUserSet: true,
	}
	if dynamicTemplate.ComputeHugepages > 0 {
		hp.Hugepages = dynamicTemplate.ComputeHugepages
	}
	if dynamicTemplate.ComputeHugepagesOffset > 0 {
		hp.HugepagesOffset = dynamicTemplate.ComputeHugepagesOffset
		hp.HugepagesOffsetUserSet = true
	} else {
		hp.HugepagesOffset = 200 + dpdkBaseMemoryMb*cores
	}
	return hp
}

// DriveHugepagesFromPlan builds drive-role ContainerHugepages for a planner-assigned (cores, drives) pair
// — the planner-managed counterpart of GetContainerHugepages(role="drive"), since a cluster-wide
// ClusterTemplate cannot carry per-container drive counts. An explicit dynamicTemplate.driveHugepages/
// driveHugepagesOffset still overrides, matching GetContainerHugepages.
func DriveHugepagesFromPlan(cluster *weka.WekaCluster, cores, drives int) ContainerHugepages {
	dynamicTemplate := cluster.Spec.Dynamic
	if dynamicTemplate == nil {
		dynamicTemplate = &weka.WekaClusterTemplate{}
	}
	dpdkBaseMemoryMb := utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeDrive)
	cons := &capacityplanner.CapacityConstraints{
		HugepagesPerCoreMiB: capacityplanner.HugepagesPerCoreMiB,
		DriveDpdkPerCoreMiB: dpdkBaseMemoryMb,
	}

	hp := ContainerHugepages{
		HugePageSize:     "2Mi",
		Mode:             weka.WekaContainerMode(weka.WekaContainerModeDrive),
		DpdkBaseMemoryMb: dpdkBaseMemoryMb,
		// HugepagesUserSet marks these as already-complete totals (weka + DPDK), so no later consumer adds
		// DPDK a second time.
		Hugepages:              capacityplanner.DriveContainerHugepagesMiB(cores, drives, cons),
		HugepagesUserSet:       true,
		HugepagesOffset:        capacityplanner.DriveContainerHugepagesOffsetMiB(cores, drives, cons),
		HugepagesOffsetUserSet: true,
	}
	if dynamicTemplate.DriveHugepages > 0 {
		hp.Hugepages = dynamicTemplate.DriveHugepages
	}
	if dynamicTemplate.DriveHugepagesOffset > 0 {
		hp.HugepagesOffset = dynamicTemplate.DriveHugepagesOffset
	}
	return hp
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
