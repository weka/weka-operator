package v1alpha1

import (
	"fmt"
	"net"
	"slices"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
)

type NodeName types.NodeName

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Weka container status",priority=0
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode",description="Weka container mode",priority=0
// +kubebuilder:printcolumn:name="Management IPs",type="string",JSONPath=".status.printer.managementIPs",description="Management IPs",priority=0
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.printer.nodeAffinity",description="Node affinity of container",priority=0
// +kubebuilder:printcolumn:name="Processes",type="string",JSONPath=".status.printer.processes",description="Number of processes per state",priority=1
// +kubebuilder:printcolumn:name="Drives",type="string",JSONPath=".status.printer.drives",description="Number of drives per state",priority=1
// +kubebuilder:printcolumn:name="Mounts",type="string",JSONPath=".status.printer.activeMounts",description="Number of active mounts",priority=1
// +kubebuilder:printcolumn:name="CPU",type="string",JSONPath=".status.stats.cpuUtilization",description="CPU Utilization",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation",priority=0
// +kubebuilder:printcolumn:name="Weka cID",type="string",JSONPath=".status.containerID",description="Weka container ID",priority=1
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

func (c *WekaContainer) GetHostIps(subnets []string) []string {
	mngmtIps := c.Status.GetManagementIps()
	hostIps := make([]string, 0, len(mngmtIps))
	port := c.GetPort()
	parsedSubnets := []net.IPNet{}
	for _, subnetstr := range subnets {
		_, subnet, err := net.ParseCIDR(subnetstr)
		if err != nil {
			//log/ctx
			continue
		}
		parsedSubnets = append(parsedSubnets, *subnet)
	}
	for _, ip := range mngmtIps {
		if len(parsedSubnets) > 0 {
			parsedIp := net.ParseIP(ip)
			if parsedIp == nil {
				//log/ctx
				continue
			}
			ipInSubnet := false
			for _, subnet := range parsedSubnets {
				if subnet.Contains(parsedIp) {
					ipInSubnet = true
					break
				}
			}
			if !ipInSubnet {
				//log/ctx
				continue
			}
		}

		if c.Spec.Ipv6 {
			hostIps = append(hostIps, fmt.Sprintf("[%s]:%d", ip, port))
		} else {
			hostIps = append(hostIps, fmt.Sprintf("%s:%d", ip, port))
		}
	}
	return hostIps
}

type WekaContainerMode string

const (
	WekaContainerModeDist           = "dist"
	WekaContainerModeDriversDist    = "drivers-dist"
	WekaContainerModeDriversLoader  = "drivers-loader"
	WekaContainerModeDriversBuilder = "drivers-builder"
	WekaContainerModeCompute        = "compute"
	WekaContainerModeDrive          = "drive"
	WekaContainerModeClient         = "client"
	WekaContainerModeDiscovery      = "discovery"
	WekaContainerModeS3             = "s3"
	WekaContainerModeNfs            = "nfs"
	WekaContainerModeSmbw           = "smbw"
	WekaContainerModeDataServices   = "data-services"
	WekaContainerModeEnvoy          = "envoy"
	WekaContainerModeSSDProxy       = "ssdproxy"
	WekaContainerModeTelemetry      = "telemetry"
	WekaContainerModeAdhocOpWC      = "adhoc-op-with-container"
	WekaContainerModeAdhocOp        = "adhoc-op"
	PersistencePathBase             = "/opt/k8s-weka"
	PersistencePathBaseCos          = "/mnt/stateful_partition/k8s-weka"
	PersistencePathBaseRhCos        = "/root/k8s-weka"
	OsNameOpenshift                 = "rhcos"
	OsNameCos                       = "cos"
	// Static is fine, since we will not rely on host network here
	StaticPortAdhocyWCOperations      = 60040
	StaticPortAdhocyWCOperationsAgent = 60039
)

type S3Params struct {
	EnvoyPort      int `json:"envoyPort,omitempty"`
	EnvoyAdminPort int `json:"envoyAdminPort,omitempty"`
	S3Port         int `json:"s3Port,omitempty"`
}

type ContainerState string

const (
	ContainerStateActive ContainerState = "active"
	// Paused state indicates that the cluster which owns the container is in grace period before being destroyed
	ContainerStatePaused ContainerState = "paused"
	// Destroying state indicates that the container can be destroyed without any additional steps (such as deactivation)
	ContainerStateDestroying ContainerState = "destroying"
	// Deleting state indicates that the container is being deleted, normally such container should go through deactivation flow
	ContainerStateDeleting ContainerState = "deleting"
)

type ContainerStatus string

const (
	Init           ContainerStatus = "Init"
	PodNotRunning  ContainerStatus = "PodNotRunning"
	PodRunning     ContainerStatus = "PodRunning"
	PodTerminating ContainerStatus = "PodTerminating"
	WaitForDrivers ContainerStatus = "WaitForDrivers"
	Running        ContainerStatus = "Running"
	Stopped        ContainerStatus = "Stopped"
	Starting       ContainerStatus = "Starting"
	Deleting       ContainerStatus = "Deleting"
	Destroying     ContainerStatus = "Destroying"
	Paused         ContainerStatus = "Paused"
	Degraded       ContainerStatus = "Degraded"
	Unhealthy      ContainerStatus = "Unhealthy"
	Error          ContainerStatus = "Error"
	DrivesAdding   ContainerStatus = "DrivesAdding"
	Draining       ContainerStatus = "Draining" // for client containers that are waiting for deletion due to active mounts
	// for drivers-build and adhoc-op-with-container (sign-dives) container
	Completed            ContainerStatus = "Completed"
	Building             ContainerStatus = "Building"
	TimestampStopAttempt ContainerStatus = "StoppingAttempt"
)

type WekaContainerSpecOverrides struct {
	// skips deactivation of container, this is unsafe operation that should be used only when this container will never be back into cluster
	SkipDeactivate bool `json:"skipDeactivate,omitempty"`
	// skips resign of drives, if we did not resign drives on removal of drive container we will not be able to reuse them, and manual operation with force resign will be required
	SkipDrivesForceResign bool `json:"skipDrivesForceResign,omitempty"`
	// skips removal of virtual drives from ssdproxy - unsafe operation that can lead to virtual drives leftovers
	// should not be used unless instructed explicitly by weka personnel
	SkipVirtualDrivesRemoval bool `json:"skipVirtualDrivesRemoval,omitempty"`
	// skips cleanup of persistent directory, if this operation was omit local data of container will remain in persistent location(/opt/k8s-weka on vanilla OS/k8s distributions)
	SkipCleanupPersistentDir bool `json:"skipCleanupPersistentDir,omitempty"`
	// unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel
	UpgradeForceReplace      bool   `json:"upgradeForceReplace,omitempty"`
	UpgradePreventEviction   bool   `json:"upgradePreventEviction,omitempty"`
	PodDeleteForceReplace    bool   `json:"podDeleteForceReplace,omitempty"`
	MachineIdentifierNodeRef string `json:"machineIdentifierNodeRef,omitempty"`
	// script to be executed post initial persistency(if needed) configuration, before running actual workload
	PreRunScript string `json:"preRunScript,omitempty"`
	// unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner
	ForceDrain bool `json:"forceDrain,omitempty"`
	// option to skip active mounts check before deleting client containers
	SkipActiveMountsCheck bool `json:"skipActiveMountsCheck,omitempty"`
	// unsafe operation, runs nsenter in root namespace to umount all wekafs mounts visible on host
	UmountOnHost bool `json:"umountOnHost,omitempty"`
	// DebugSleepOnTerminate specifies the number of seconds to sleep on container abnormal exit for debugging purposes
	DebugSleepOnTerminate int `json:"debugSleepOnTerminate,omitempty"`
	// MigrateOutFromPvc specifies that the container should be migrated out from PVC into local storage, this will be done prior to starting pod
	MigrateOutFromPvc bool `json:"migrateOutFromPvc,omitempty"`
}

type Instructions struct {
	Type    string `json:"type"`
	Payload string `json:"payload,omitempty"`
}

type DataServicesConfig struct {
	DataServicesFeCores int `json:"dataServicesFeCores,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity == 0 || !has(self.driveTypesRatio)",message="driveCapacity and driveTypesRatio are mutually exclusive; use driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types"
// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity == 0 || !has(self.numDrives) || self.numDrives == 0 || self.numDrives >= self.numCores",message="numDrives must be >= numCores when using driveCapacity (TLC-only mode); each core requires at least one virtual drive"
// +kubebuilder:validation:XValidation:rule="!has(self.numDrives) || self.numDrives == 0 || !has(self.containerCapacity) || self.containerCapacity == 0",message="numDrives and containerCapacity are mutually exclusive; use numDrives with driveCapacity for TLC-only mode, or containerCapacity with driveTypesRatio for mixed drive types"
// +kubebuilder:validation:XValidation:rule="!has(self.containerCapacity) || self.containerCapacity > 0",message="containerCapacity must be greater than 0 when specified"
// +kubebuilder:validation:XValidation:rule="!has(self.driveTypesRatio) || self.driveTypesRatio.tlc > 0 || self.driveTypesRatio.qlc > 0",message="at least one of driveTypesRatio.tlc or driveTypesRatio.qlc must be greater than 0"
// +kubebuilder:validation:XValidation:rule="!has(self.driveTypesRatio) || self.driveTypesRatio.tlc > 0",message="driveTypesRatio.tlc must be greater than 0 when driveTypesRatio is specified; TLC-only and mixed TLC/QLC configurations are supported, but QLC-only is not allowed"
// +kubebuilder:validation:XValidation:rule="!has(self.driveCapacity) || self.driveCapacity > 0",message="driveCapacity must be greater than 0 when specified"
type WekaContainerSpec struct {
	// name of the node where the container should run on
	NodeAffinity NodeName `json:"nodeAffinity,omitempty"`
	// failure domain configuration
	FailureDomain *FailureDomain `json:"failureDomain,omitempty"`
	// controls the distribution of weka containers across the failure domains
	TopologySpreadConstraints []v1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// advanced scheduling constraints
	Affinity     *v1.Affinity      `json:"affinity,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	Port         int               `json:"port,omitempty"`
	// deprecated, use ExposedPorts instead
	ExposePorts []int `json:"exposePorts,omitempty"`
	// ports to be exposed on the container, proxied to pod
	ExposedPorts []v1.ContainerPort `json:"exposedPorts,omitempty"`
	AgentPort    int                `json:"agentPort,omitempty"`
	PortRange    *PortRange         `json:"portRange,omitempty"`
	Image        string             `json:"image"`
	// a hash that represents the config state of a WekaContainer, will recreate the pod if stale
	PodConfigHash     string `json:"podConfigHash,omitempty"`
	ImagePullSecret   string `json:"imagePullSecret,omitempty"`
	WekaContainerName string `json:"name"`
	// +kubebuilder:validation:Enum=drive;compute;client;dist;drivers-dist;drivers-loader;drivers-builder;discovery;s3;adhoc-op-with-container;adhoc-op;envoy;nfs;smbw;telemetry;ssdproxy;data-services;data-services-fe
	Mode       string `json:"mode"`
	NumCores   int    `json:"numCores"`             //numCores is weka-specific cores
	ExtraCores int    `json:"extraCores,omitempty"` //extraCores is temporary solution for S3 containers, cores allocation on top of weka cores
	CoreIds    []int  `json:"coreIds,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	// +kubebuilder:default=auto
	CpuPolicy             CpuPolicy             `json:"cpuPolicy,omitempty"`
	Network               Network               `json:"network,omitempty"`
	Hugepages             int                   `json:"hugepages,omitempty"`
	HugepagesOffset       int                   `json:"hugepagesOffset,omitempty"`
	HugepagesSize         string                `json:"hugepagesSize,omitempty"`
	NumDrives             int                   `json:"numDrives,omitempty"`
	DriversDistService    string                `json:"driversDistService,omitempty"`
	DriversLoaderImage    string                `json:"driversLoaderImage,omitempty"`
	DriversBuildId        *string               `json:"driversBuildId,omitempty"`
	Builder               *WekaContainerBuilder `json:"builder,omitempty"`
	WekaSecretRef         v1.EnvVarSource       `json:"wekaSecretRef,omitempty"`
	JoinIps               []string              `json:"joinIpPorts,omitempty"`
	TracesConfiguration   *TracesConfiguration  `json:"tracesConfiguration,omitempty"`
	Tolerations           []v1.Toleration       `json:"tolerations,omitempty"`
	NodeInfoConfigMap     string                `json:"nodeInfoConfigMap,omitempty"`
	Ipv6                  bool                  `json:"ipv6,omitempty"`
	AdditionalMemory      int                   `json:"additionalMemory,omitempty"`
	Group                 string                `json:"group,omitempty"`
	ServiceAccountName    string                `json:"serviceAccountName,omitempty"`
	AdditionalSecrets     map[string]string     `json:"additionalSecrets,omitempty"`
	Instructions          *Instructions         `json:"instructions,omitempty"`
	NoAffinityConstraints bool                  `json:"dropAffinityConstraints,omitempty"`
	UploadResultsTo       string                `json:"uploadResultsTo,omitempty"`
	// +kubebuilder:validation:Enum=manual;all-at-once;rolling;all-at-once-force
	// +kubebuilder:default=manual
	UpgradePolicyType UpgradePolicyType `json:"upgradePolicyType,omitempty"`
	// +kubebuilder:validation:Enum=active;paused;destroying;deleting
	// +kubebuilder:default=active
	State           ContainerState `json:"state,omitempty"`
	AllowHotUpgrade bool           `json:"allowHotUpgrade,omitempty"`
	// DriveCapacity specifies the capacity (in GiB) per virtual drive, indicates this container uses shared drives via SSD proxy.
	// When enabled, the container will:
	// - Use virtual UUIDs instead of device paths for drives
	// - Allocate capacity from shared drives rather than exclusive drives
	// - Require an SSD proxy container to be running on the same node
	// This value is copied from the cluster's DriveSharing.DriveCapacity configuration.
	// Used to calculate total capacity request: NumDrives * DriveCapacity
	DriveCapacity int `json:"driveCapacity,omitempty"`
	// ContainerCapacity specifies the total capacity (in GiB) requested by this container when using shared drives via SSD proxy.
	// This value takes precedence over DriveCapacity when both are set. It allows more flexible capacity allocation.
	ContainerCapacity int `json:"containerCapacity,omitempty"`
	// DriveTypesRatio specifies the desired ratio of drive types (TLC vs QLC) when allocating drives for the container.
	DriveTypesRatio *DriveTypesRatio `json:"driveTypesRatio,omitempty"`
	// sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +kubebuilder:default="0s"
	AutoRemoveTimeout metav1.Duration             `json:"autoRemoveTimeout,omitempty"`
	Overrides         *WekaContainerSpecOverrides `json:"overrides,omitempty"`
	HostPID           bool                        `json:"hostPID,omitempty"`
	// resources to be proxied as-is to the pod spec
	Resources          *PodResourcesSpec   `json:"resources,omitempty"`
	PVC                *PVCConfig          `json:"pvc,omitempty"`
	DpdkBaseMemoryMb   int                 `json:"dpdkBaseMemoryMb,omitempty"`
	DataServicesConfig *DataServicesConfig `json:"dataServicesConfig,omitempty"`
}

type AWSNetwork struct {
	// should provide list of additional nics indexes starting from 1, index 0 is reserved for kernel networking
	DeviceSlots []int `json:"deviceSlots,omitempty"`
}

// VirtualDrive represents a virtual drive allocation in drive sharing mode
type VirtualDrive struct {
	// VirtualUUID is the virtual drive identifier
	VirtualUUID string `json:"virtualUUID"`
	// PhysicalUUID is the physical drive UUID obtained from proxy signing
	PhysicalUUID string `json:"physicalUUID"`
	// CapacityGiB is the allocated capacity in GiB
	CapacityGiB int `json:"capacityGiB"`
	// Serial is the serial number of the physical drive
	Serial string `json:"serial"`
	// Type is the type of the drive (e.g., TLC, QLC)
	Type string `json:"type,omitempty"`
}

type ContainerAllocations struct {
	Drives    []string `json:"drives,omitempty"`
	WekaPort  int      `json:"wekaPort,omitempty"`
	AgentPort int      `json:"agentPort,omitempty"`
	// value of the failure domain label of the node where the container is running
	FailureDomain     *string  `json:"failureDomain,omitempty"`
	MachineIdentifier string   `json:"machineIdentifier,omitempty"`
	NetDevices        []string `json:"netDevices,omitempty"`
	// VirtualDrives contains virtual drive allocations for drive sharing mode.
	// Each VirtualDrive maps a virtual UUID to a physical drive UUID with allocated capacity.
	VirtualDrives []VirtualDrive `json:"virtualDrives,omitempty"`
}

func (c *ContainerAllocations) Equals(other *ContainerAllocations) bool {
	if c == nil && other == nil {
		return true
	}
	if c == nil || other == nil {
		return false
	}
	if c.WekaPort != other.WekaPort || c.AgentPort != other.AgentPort {
		return false
	}
	if !slices.Equal(c.Drives, other.Drives) || !slices.Equal(c.NetDevices, other.NetDevices) {
		return false
	}
	if (c.FailureDomain == nil && other.FailureDomain != nil) || (c.FailureDomain != nil && other.FailureDomain == nil) {
		return false
	}
	if c.FailureDomain != nil && *c.FailureDomain != *other.FailureDomain {
		return false
	}
	if c.MachineIdentifier != other.MachineIdentifier {
		return false
	}
	// Compare VirtualDrives slices
	if len(c.VirtualDrives) != len(other.VirtualDrives) {
		return false
	}
	for i := range c.VirtualDrives {
		if c.VirtualDrives[i].VirtualUUID != other.VirtualDrives[i].VirtualUUID ||
			c.VirtualDrives[i].PhysicalUUID != other.VirtualDrives[i].PhysicalUUID ||
			c.VirtualDrives[i].CapacityGiB != other.VirtualDrives[i].CapacityGiB ||
			c.VirtualDrives[i].Serial != other.VirtualDrives[i].Serial {
			return false
		}
	}
	return true
}

func (c *ContainerAllocations) GetVirtualDrivesUuids() []string {
	uuids := make([]string, 0, len(c.VirtualDrives))
	for _, d := range c.VirtualDrives {
		uuids = append(uuids, d.VirtualUUID)
	}
	return uuids
}

func (c *ContainerAllocations) GetVirtualDrivesPhysicalUuids() []string {
	uuids := make([]string, 0, len(c.VirtualDrives))
	for _, d := range c.VirtualDrives {
		uuids = append(uuids, d.PhysicalUUID)
	}
	return uuids
}

// GetTotalAllocatedCapacity returns the total capacity allocated across all virtual drives (in GiB).
func (c *ContainerAllocations) GetAllocatedVirtualDrivesCapacity() int {
	total := 0
	for _, d := range c.VirtualDrives {
		total += d.CapacityGiB
	}
	return total
}

type Drive struct {
	Uuid         string `json:"uuid"`
	AddedTime    string `json:"added_time"`
	DevicePath   string `json:"device_path"`
	SerialNumber string `json:"serial_number"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Status       string `json:"status"`
}

type WekaContainerMetrics struct {
	Processes    EntityStatefulNum `json:"processes,omitempty"`
	CpuUsage     FloatMetric       `json:"cpuUtilization,omitempty"`
	Drives       DriveMetrics      `json:"drives,omitempty"`
	ActiveMounts IntMetric         `json:"activeMounts,omitempty"`
	LastUpdate   metav1.Time       `json:"lastUpdate,omitempty"`
}

type ContainerPrinterColumns struct {
	Processes    StringMetric `json:"processes,omitempty"`
	Drives       StringMetric `json:"drives,omitempty"`
	ActiveMounts StringMetric `json:"activeMounts,omitempty"`
	// pretty-printed management IPs
	ManagementIPs string `json:"managementIPs,omitempty"`
	// node name where the container is running
	NodeAffinity string `json:"nodeAffinity,omitempty"`
}

func (c *ContainerPrinterColumns) SetManagementIps(ips []string) {
	if len(ips) == 0 {
		return
	}
	total := len(ips)
	if total > 1 {
		c.ManagementIPs = fmt.Sprintf("%s (+%d)", ips[0], total-1)
	} else {
		c.ManagementIPs = ips[0]
	}
}

type WekaContainerStatus struct {
	// +kubebuilder:default="Init"
	Status                   ContainerStatus          `json:"status"`
	InternalStatus           string                   `json:"internalStatus,omitempty"` // weka local container internal status
	ManagementIP             string                   `json:"managementIP,omitempty"`
	ManagementIPs            []string                 `json:"managementIPs,omitempty"`
	ClusterContainerID       *int                     `json:"containerID,omitempty"`
	ClusterID                string                   `json:"clusterID,omitempty"`
	Conditions               []metav1.Condition       `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	LastAppliedImage         string                   `json:"lastAppliedImage,omitempty"`         // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	LastAppliedSpec          string                   `json:"lastAppliedSpec,omitempty"`          // set by weka cluster or client or other higher level controller, to track if higher level spec was propagated
	LastAppliedPodConfigHash string                   `json:"lastAppliedPodConfigHash,omitempty"` // to signal to a higher controller WekaContainer's pod status
	NodeAffinity             NodeName                 `json:"nodeAffinity,omitempty"`             // active nodeAffinity, copied from spec and populated if nodeSelector was used instead of direct nodeAffinity
	ExecutionResult          *string                  `json:"result,omitempty"`
	Allocations              *ContainerAllocations    `json:"allocations,omitempty"`
	AddedDrives              []Drive                  `json:"addedDrives,omitempty"` // drives that were added to the weka cluster
	Stats                    *WekaContainerMetrics    `json:"stats,omitempty"`
	PrinterColumns           *ContainerPrinterColumns `json:"printer,omitempty"`
	Timestamps               map[string]metav1.Time   `json:"timestamps,omitempty"`
	NotToleratedOnReschedule bool                     `json:"notToleratedOnReschedule,omitempty"`
}

func (s *WekaContainerStatus) GetAddedDrivesSerials() []string {
	serials := make([]string, 0, len(s.AddedDrives))
	for _, d := range s.AddedDrives {
		if d.SerialNumber == "" {
			continue
		}
		serials = append(serials, d.SerialNumber)
	}
	return serials
}

func (s *WekaContainerStatus) GetAddedDrivesUuids() []string {
	uuids := make([]string, 0, len(s.AddedDrives))
	for _, d := range s.AddedDrives {
		uuids = append(uuids, d.Uuid)
	}
	return uuids
}

func (s *WekaContainerStatus) GetManagementIps() []string {
	if s.ManagementIPs != nil {
		return s.ManagementIPs
	}
	if s.ManagementIP != "" {
		return []string{s.ManagementIP}
	}
	return nil
}

func (s *WekaContainerStatus) GetPrinterColumns() *ContainerPrinterColumns {
	if s.PrinterColumns == nil {
		return &ContainerPrinterColumns{}
	}
	return s.PrinterColumns
}

func (s *WekaContainerStatus) GetStats() *WekaContainerMetrics {
	if s.Stats == nil {
		return &WekaContainerMetrics{}
	}
	return s.Stats
}

// TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes
type TracesConfiguration struct {
	// +kubebuilder:default=10
	MaxCapacityPerIoNode int `json:"maxCapacityPerIoNode,omitempty"`
	// +kubebuilder:default=20
	EnsureFreeSpace int `json:"ensureFreeSpace,omitempty"`
	// +kubebuilder:default=auto
	// +kubebuilder:validation:Enum=override;partial-override;auto;cluster
	DumperConfigMode DumperConfigMode `json:"dumperConfigMode,omitempty"`
}

type DumperConfigMode string

const (
	DumperConfigModeAuto            DumperConfigMode = "auto"
	DumperConfigModeOverride        DumperConfigMode = "override"
	DumperConfigOverrideModePartial DumperConfigMode = "partial-override"
	DumperConfigOverrideModeCluster DumperConfigMode = "cluster"
)

func GetDefaultTracesConfiguration() *TracesConfiguration {
	return &TracesConfiguration{
		MaxCapacityPerIoNode: 10,
		EnsureFreeSpace:      20,
		DumperConfigMode:     DumperConfigModeAuto,
	}
}

// +kubebuilder:object:root=true
type WekaContainerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaContainer `json:"items"`
}

type CpuPolicy string

const (
	CpuPolicyAuto        CpuPolicy = "auto"
	CpuPolicyShared      CpuPolicy = "shared"
	CpuPolicyDedicated   CpuPolicy = "dedicated"
	CpuPolicyDedicatedHT CpuPolicy = "dedicated_ht"
	CpuPolicyManual      CpuPolicy = "manual"
)

func (c CpuPolicy) IsValid() bool {
	switch c {
	case CpuPolicyAuto, CpuPolicyShared, CpuPolicyDedicated, CpuPolicyDedicatedHT, CpuPolicyManual:
		return true
	}
	return false
}

func init() {
	SchemeBuilder.Register(&WekaContainer{}, &WekaContainerList{})
}

func (w *WekaContainer) DriversReady() bool {
	return meta.IsStatusConditionTrue(w.Status.Conditions, condition.CondEnsureDrivers)
}

func (w *WekaContainer) IsDistMode() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversDist}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversLoaderMode() bool {
	// Check for legacy drivers-loader mode
	if w.Spec.Mode == WekaContainerModeDriversLoader {
		return true
	}
	// Check for new adhoc-op-with-container mode with load-drivers instructions
	return w.Spec.Mode == WekaContainerModeAdhocOpWC &&
		w.Spec.Instructions != nil &&
		w.Spec.Instructions.Type == InstructionTypeLoadDrivers
}

func (w *WekaContainer) RequiresDrivers() bool {
	return w.IsWekaContainer() && !w.IsDriversContainer() && !w.IsEnvoy() && !w.IsTelemetry()
}

func (w *WekaContainer) IsServiceContainer() bool {
	return slices.Contains([]string{
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeDriversLoader,
		WekaContainerModeDiscovery,
		WekaContainerModeDriversBuilder,
		WekaContainerModeEnvoy,
		WekaContainerModeAdhocOpWC,
		WekaContainerModeAdhocOp,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostNetwork() bool {
	return w.IsWekaContainer() && !w.IsDriversContainer() && !w.IsSSDProxyContainer() && !w.IsTelemetry()
}

func (w *WekaContainer) ShouldJoinCluster() bool {
	return w.IsWekaContainer() && !w.IsDriversContainer() && !w.IsEnvoy() && !w.IsSSDProxyContainer() && !w.IsTelemetry()
}

func (w *WekaContainer) IsDriversContainer() bool {
	return slices.Contains([]string{
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeDriversLoader,
		WekaContainerModeDriversBuilder,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversBuilder() bool {
	return slices.Contains([]string{WekaContainerModeDriversBuilder, WekaContainerModeDist}, w.Spec.Mode)
}

func (w *WekaContainer) IsBackend() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3, WekaContainerModeNfs, WekaContainerModeSmbw, WekaContainerModeDataServices}, w.Spec.Mode)
}

func (w *WekaContainer) IsDiscoveryContainer() bool {
	return w.Spec.Mode == WekaContainerModeDiscovery
}

func (w *WekaContainer) IsAdhocOpContainer() bool {
	return slices.Contains([]string{WekaContainerModeAdhocOpWC, WekaContainerModeAdhocOp, WekaContainerModeDriversLoader}, w.Spec.Mode)
}

func (w *WekaContainer) HasPersistentStorage() bool {
	return slices.Contains([]string{
		WekaContainerModeDrive,
		WekaContainerModeCompute,
		WekaContainerModeS3,
		WekaContainerModeEnvoy,
		WekaContainerModeClient,
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeNfs,
		WekaContainerModeSmbw,
		WekaContainerModeSSDProxy,
		WekaContainerModeTelemetry,
		WekaContainerModeDataServices,
	}, w.Spec.Mode)
}

func (w *WekaContainer) HasFrontend() bool {
	return slices.Contains([]string{WekaContainerModeS3, WekaContainerModeClient, WekaContainerModeNfs, WekaContainerModeSmbw}, w.Spec.Mode)
}

func (w *WekaContainer) IsS3Container() bool {
	return w.Spec.Mode == WekaContainerModeS3
}

func (w *WekaContainer) IsNfsContainer() bool {
	return w.Spec.Mode == WekaContainerModeNfs
}

func (w *WekaContainer) IsDataServicesContainer() bool {
	return w.Spec.Mode == WekaContainerModeDataServices
}

func (w *WekaContainer) HasJoinIps() bool {
	return len(w.Spec.JoinIps) > 0
}

func (w *WekaContainer) IsDriveContainer() bool {
	return w.Spec.Mode == WekaContainerModeDrive
}

func (w *WekaContainer) IsComputeContainer() bool {
	return w.Spec.Mode == WekaContainerModeCompute
}

func (w *WekaContainer) IsWekaContainer() bool {
	return slices.Contains([]string{
		WekaContainerModeDrive,
		WekaContainerModeCompute,
		WekaContainerModeS3,
		WekaContainerModeClient,
		WekaContainerModeEnvoy,
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeDriversBuilder,
		WekaContainerModeNfs,
		WekaContainerModeSmbw,
		WekaContainerModeSSDProxy,
		WekaContainerModeTelemetry,
		WekaContainerModeDataServices,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsAllocatable() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeEnvoy, WekaContainerModeS3, WekaContainerModeNfs, WekaContainerModeSmbw, WekaContainerModeTelemetry, WekaContainerModeDataServices}, w.Spec.Mode)
}

func (w *WekaContainer) MustHaveNodeAffinity() bool {
	return w.IsAllocatable() && w.IsBackend() || w.IsEnvoy() || w.IsTelemetry()
}

func (w *WekaContainer) HasAgent() bool {
	return slices.Contains([]string{
		WekaContainerModeDrive,
		WekaContainerModeCompute,
		WekaContainerModeS3,
		WekaContainerModeEnvoy,
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeDriversBuilder,
		WekaContainerModeAdhocOpWC,
		WekaContainerModeNfs,
		WekaContainerModeSmbw,
		WekaContainerModeSSDProxy,
		WekaContainerModeTelemetry,
		WekaContainerModeDataServices,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostWideSingleton() bool {
	return slices.Contains([]string{WekaContainerModeEnvoy, WekaContainerModeS3, WekaContainerModeNfs, WekaContainerModeSmbw, WekaContainerModeTelemetry}, w.Spec.Mode)
}

func (w *WekaContainer) GetNodeAffinity() NodeName {
	if w.Spec.NodeAffinity != "" {
		return w.Spec.NodeAffinity
	}
	if w.Status.NodeAffinity != "" {
		return w.Status.NodeAffinity
	}
	return ""
}

func (w *WekaContainer) ToOwnerDetails() *WekaOwnerDetails {
	return &WekaOwnerDetails{
		Image:              w.Spec.Image,
		ImagePullSecret:    w.Spec.ImagePullSecret,
		Tolerations:        w.Spec.Tolerations,
		Labels:             w.ObjectMeta.GetLabels(),
		Affinity:           w.Spec.Affinity,
		ServiceAccountName: w.Spec.ServiceAccountName,
	}
}

func (w *WekaContainer) IsOneOff() bool {
	return slices.Contains([]string{WekaContainerModeAdhocOpWC, WekaContainerModeDiscovery, WekaContainerModeAdhocOp, WekaContainerModeDriversBuilder, WekaContainerModeDriversLoader}, w.Spec.Mode)

}

func (w *WekaContainer) IsClientContainer() bool {
	return w.Spec.Mode == WekaContainerModeClient
}

func (w *WekaContainer) IsProtocolContainer() bool {
	return slices.Contains([]string{WekaContainerModeNfs, WekaContainerModeS3, WekaContainerModeSmbw, WekaContainerModeDataServices}, w.Spec.Mode)
}

func (w *WekaContainer) IsSmbwContainer() bool {
	return w.Spec.Mode == WekaContainerModeSmbw
}

func (w *WekaContainer) IsSSDProxyContainer() bool {
	return w.Spec.Mode == WekaContainerModeSSDProxy
}

func (w *WekaContainer) UsesDriveSharing() bool {
	return w.Spec.DriveCapacity > 0 || w.Spec.ContainerCapacity > 0
}

func (w *WekaContainer) HasContainerCapacity() bool {
	return w.Spec.ContainerCapacity > 0
}

func (w *WekaContainer) TlcToQlcRatioEnabled() bool {
	return w.Spec.DriveTypesRatio != nil && (w.Spec.DriveTypesRatio.Tlc > 0 || w.Spec.DriveTypesRatio.Qlc > 0)
}

func (w *WekaContainer) GetParentClusterId() string {
	// get parent via controller reference
	for _, ref := range w.GetOwnerReferences() {
		if ref.Kind == "WekaCluster" {
			return string(ref.UID)
		}
	}
	return ""
}

func (w *WekaContainer) IsEnvoy() bool {
	return w.Spec.Mode == WekaContainerModeEnvoy
}

func (w *WekaContainer) IsTelemetry() bool {
	return w.Spec.Mode == WekaContainerModeTelemetry
}

func (w *WekaContainer) GetPort() int {
	if w.Status.Allocations == nil {
		return w.Spec.Port
	}
	if w.Status.Allocations.WekaPort == 0 {
		return w.Spec.Port
	}
	return w.Status.Allocations.WekaPort
}

func (w *WekaContainer) GetAgentPort() int {
	if w.Status.Allocations == nil {
		return w.Spec.AgentPort
	}
	if w.Status.Allocations.AgentPort == 0 {
		return w.Spec.AgentPort
	}
	return w.Status.Allocations.AgentPort
}

func (c *WekaContainer) IsMarkedForDeletion() bool {
	return !c.GetDeletionTimestamp().IsZero()
}

func (c *WekaContainer) IsActive() bool {
	return c.Spec.State == ContainerStateActive
}

func (c *WekaContainer) IsPaused() bool {
	return c.Spec.State == ContainerStatePaused
}

func (c *WekaContainer) IsDestroyingState() bool {
	return c.Spec.State == ContainerStateDestroying
}

func (c *WekaContainer) IsDeletingState() bool {
	return c.Spec.State == ContainerStateDeleting
}

func (c *WekaContainer) IsDeactivated() bool {
	return meta.IsStatusConditionTrue(c.Status.Conditions, condition.CondContainerDeactivated)
}

func (c *WekaContainer) IsRemoved() bool {
	return meta.IsStatusConditionTrue(c.Status.Conditions, condition.CondContainerRemoved)
}

func (c *WekaContainer) DrivesRemoved() bool {
	return meta.IsStatusConditionTrue(c.Status.Conditions, condition.CondContainerDrivesRemoved)
}

type WekaOwnerDetails struct {
	Image              string            `json:"image"`
	ImagePullSecret    string            `json:"imagePullSecrets"`
	Tolerations        []v1.Toleration   `json:"tolerations,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	Affinity           *v1.Affinity      `json:"affinity,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

func (c *WekaContainerSpec) GetOverrides() *WekaContainerSpecOverrides {
	if c.Overrides == nil {
		return &WekaContainerSpecOverrides{}
	} else {
		return c.Overrides
	}
}
