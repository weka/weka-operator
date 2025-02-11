package v1alpha1

import (
	"fmt"
	"slices"

	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type NodeName types.NodeName

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Weka container status",priority=0
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode",description="Weka container mode",priority=0
// +kubebuilder:printcolumn:name="Management IPs",type="string",JSONPath=".status.printer.managementIPs",description="Management IPs",priority=0
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeAffinity",description="Node affinity of container",priority=0
// +kubebuilder:printcolumn:name="Processes",type="string",JSONPath=".status.printer.processes",description="Number of processes per state",priority=1
// +kubebuilder:printcolumn:name="Drives",type="string",JSONPath=".status.printer.drives",description="Number of drives per state",priority=1
// +kubebuilder:printcolumn:name="Mounts",type="string",JSONPath=".status.printer.activeMounts",description="Number of active mounts",priority=1
// +kubebuilder:printcolumn:name="CPU",type="string",JSONPath=".status.stats.cpuUtilization",description="CPU Utilization",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation",priority=0
// +kubebuilder:printcolumn:name="Weka cID",type="string",JSONPath=".status.containerID",description="Weka container ID",priority=1
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.message",description="Weka container message",priority=1
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

func (c *WekaContainer) GetHostIps() []string {
	mngmtIps := c.Status.GetManagementIps()
	hostIps := make([]string, 0, len(mngmtIps))
	port := c.GetPort()
	for _, ip := range mngmtIps {
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
	WekaContainerModeEnvoy          = "envoy"
	WekaContainerModeAdhocOpWC      = "adhoc-op-with-container"
	WekaContainerModeAdhocOp        = "adhoc-op"
	PersistencePathBase             = "/opt/k8s-weka"
	PersistencePathBaseCos          = "/mnt/stateful_partition/k8s-weka"
	PersistencePathBaseRhCos        = "/root/k8s-weka"
	OsNameOpenshift                 = "rhcos"
	OsNameCos                       = "cos"
	// Statis is fine, since we will not relay on host network here
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

type WekaContainerSpecOverrides struct {
	// skips deactivation of container, this is unsafe operation that should be used only when this container will never be back into cluster
	SkipDeactivate bool `json:"skipDeactivate,omitempty"`
	// skips resign of drives, if we did not resign drives on removal of drive container we will not be able to reuse them, and manual operation with force resign will be required
	SkipDrivesForceResign bool `json:"skipDrivesForceResign,omitempty"`
	// skips cleanup of persistent directory, if this operation was omit local data of container will remain in persistent location(/opt/k8s-weka on vanilla OS/k8s distributions)
	SkipCleanupPersistentDir bool   `json:"skipCleanupPersistentDir,omitempty"`
	UpgradeForceReplace      bool   `json:"upgradeForceReplace,omitempty"`
	MachineIdentifierNodeRef string `json:"machineIdentifierNodeRef,omitempty"`
	// script to be executed post initial persistency(if needed) configuration, before running actual workload
	PreRunScript string `json:"preRunScript,omitempty"`
}

type Instructions struct {
	Type    string `json:"type"`
	Payload string `json:"payload,omitempty"`
}

type WekaContainerSpec struct {
	// name of the node where the container should run on
	NodeAffinity NodeName `json:"nodeAffinity,omitempty"`
	// failure domain configuration
	FailureDomain *FailureDomain `json:"failureDomain,omitempty"`
	// controls the distribution of weka containers across the failure domains
	TopologySpreadConstraints []v1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// advanced scheduling constraints
	Affinity          *v1.Affinity      `json:"affinity,omitempty"`
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	Port              int               `json:"port,omitempty"`
	AgentPort         int               `json:"agentPort,omitempty"`
	PortRange         *PortRange        `json:"portRange,omitempty"`
	Image             string            `json:"image"`
	ImagePullSecret   string            `json:"imagePullSecret,omitempty"`
	WekaContainerName string            `json:"name"`
	// +kubebuilder:validation:Enum=drive;compute;client;dist;drivers-dist;drivers-loader;drivers-builder;discovery;s3;adhoc-op-with-container;adhoc-op;envoy;nfs
	Mode       string `json:"mode"`
	NumCores   int    `json:"numCores"`             //numCores is weka-specific cores
	ExtraCores int    `json:"extraCores,omitempty"` //extraCores is temporary solution for S3 containers, cores allocation on top of weka cores
	CoreIds    []int  `json:"coreIds,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	// +kubebuilder:default=auto
	CpuPolicy             CpuPolicy            `json:"cpuPolicy,omitempty"`
	Network               Network              `json:"network,omitempty"`
	Hugepages             int                  `json:"hugepages,omitempty"`
	HugepagesOffset       int                  `json:"hugepagesOffset,omitempty"`
	HugepagesSize         string               `json:"hugepagesSize,omitempty"`
	HugepagesOverride     string               `json:"hugepagesSizeOverride,omitempty"`
	NumDrives             int                  `json:"numDrives,omitempty"`
	DriversDistService    string               `json:"driversDistService,omitempty"`
	DriversLoaderImage    string               `json:"driversLoaderImage,omitempty"`
	WekaSecretRef         v1.EnvVarSource      `json:"wekaSecretRef,omitempty"`
	JoinIps               []string             `json:"joinIpPorts,omitempty"`
	TracesConfiguration   *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations           []v1.Toleration      `json:"tolerations,omitempty"`
	NodeInfoConfigMap     string               `json:"nodeInfoConfigMap,omitempty"`
	Ipv6                  bool                 `json:"ipv6,omitempty"`
	AdditionalMemory      int                  `json:"additionalMemory,omitempty"`
	Group                 string               `json:"group,omitempty"`
	ServiceAccountName    string               `json:"serviceAccountName,omitempty"`
	AdditionalSecrets     map[string]string    `json:"additionalSecrets,omitempty"`
	Instructions          *Instructions        `json:"instructions,omitempty"`
	NoAffinityConstraints bool                 `json:"dropAffinityConstraints,omitempty"`
	UploadResultsTo       string               `json:"uploadResultsTo,omitempty"`
	// +kubebuilder:validation:Enum=manual;all-at-once;rolling
	// +kubebuilder:default=manual
	UpgradePolicyType UpgradePolicyType `json:"upgradePolicyType,omitempty"`
	// +kubebuilder:validation:Enum=active;paused;destroying;deleting
	// +kubebuilder:default=active
	State           ContainerState              `json:"state,omitempty"`
	AllowHotUpgrade bool                        `json:"allowHotUpgrade,omitempty"`
	Overrides       *WekaContainerSpecOverrides `json:"overrides,omitempty"`
}

type AWSNetwork struct {
	// should provide list of additional nics indexes starting from 1, index 0 is reserved for kernel networking
	DeviceSlots []int `json:"deviceSlots,omitempty"`
}

type Network struct {
	EthDevices []string   `json:"ethDevices,omitempty"`
	EthDevice  string     `json:"ethDevice,omitempty"`
	UdpMode    bool       `json:"udpMode,omitempty"`
	AWS        AWSNetwork `json:"aws,omitempty"`
	Gateway    string     `json:"gateway,omitempty"`
	// subnet that is used for devices auto-discovery
	// +kubebuilder:validation:items:Pattern="^([0-9]{1,3}\\.){3}[0-9]{1,3}\\/[0-9]{1,2}$"
	DeviceSubnets []string `json:"deviceSubnets,omitempty"`
}

type ContainerAllocations struct {
	Drives    []string `json:"drives,omitempty"`
	EthSlots  []string `json:"ethSlots,omitempty"`
	LbPort    int      `json:"lbPort,omitempty"`
	WekaPort  int      `json:"wekaPort,omitempty"`
	AgentPort int      `json:"agentPort,omitempty"`
	// value of the failure domain label of the node where the container is running
	FailureDomain     *string  `json:"failureDomain,omitempty"`
	MachineIdentifier string   `json:"machineIdentifier,omitempty"`
	NetDevices        []string `json:"netDevices,omitempty"`
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
	Status             string                   `json:"status"`
	ManagementIP       string                   `json:"managementIP,omitempty"`
	ManagementIPs      []string                 `json:"managementIPs,omitempty"`
	ClusterContainerID *int                     `json:"containerID,omitempty"`
	ClusterID          string                   `json:"clusterID,omitempty"`
	Conditions         []metav1.Condition       `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	LastAppliedImage   string                   `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	NodeAffinity       NodeName                 `json:"nodeAffinity,omitempty"`     // active nodeAffinity, copied from spec and populated if nodeSelector was used instead of direct nodeAffinity
	ExecutionResult    *string                  `json:"result,omitempty"`
	Allocations        *ContainerAllocations    `json:"allocations,omitempty"`
	Stats              *WekaContainerMetrics    `json:"stats,omitempty"`
	PrinterColumns     *ContainerPrinterColumns `json:"printer,omitempty"`
	Timestamps         map[string]metav1.Time   `json:"timestamps,omitempty"`
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

// TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes
type TracesConfiguration struct {
	// +kubebuilder:default=10
	MaxCapacityPerIoNode int `json:"maxCapacityPerIoNode,omitempty"`
	// +kubebuilder:default=20
	EnsureFreeSpace int `json:"ensureFreeSpace,omitempty"`
}

func GetDefaultTracesConfiguration() *TracesConfiguration {
	return &TracesConfiguration{
		MaxCapacityPerIoNode: 10,
		EnsureFreeSpace:      20,
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
	return w.Spec.Mode == WekaContainerModeDriversLoader
}

func (w *WekaContainer) RequiresDrivers() bool {
	return w.IsWekaContainer() && !w.IsDistMode() && !w.IsEnvoy()
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
	return w.IsWekaContainer() && !w.IsDistMode()
}

func (w *WekaContainer) ShouldJoinCluster() bool {
	return w.IsWekaContainer() && !w.IsDistMode() && !w.IsEnvoy()
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
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3, WekaContainerModeNfs}, w.Spec.Mode)
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
	}, w.Spec.Mode)
}

func (w *WekaContainer) HasFrontend() bool {
	return slices.Contains([]string{WekaContainerModeS3, WekaContainerModeClient, WekaContainerModeNfs}, w.Spec.Mode)
}

func (w *WekaContainer) IsS3Container() bool {
	return slices.Contains([]string{WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) IsNfsContainer() bool {
	return slices.Contains([]string{WekaContainerModeNfs}, w.Spec.Mode)
}

func (w *WekaContainer) HasJoinIps() bool {
	return w.Spec.JoinIps != nil && len(w.Spec.JoinIps) > 0
}

func (w *WekaContainer) IsDriveContainer() bool {
	return slices.Contains([]string{WekaContainerModeDrive}, w.Spec.Mode)
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
		WekaContainerModeNfs,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsAllocatable() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeEnvoy, WekaContainerModeS3, WekaContainerModeNfs}, w.Spec.Mode)
}

func (w *WekaContainer) MustHaveNodeAffinity() bool {
	return w.IsAllocatable() && w.IsBackend() || w.IsEnvoy()
}

func (w *WekaContainer) HasAgent() bool {
	return slices.Contains([]string{
		WekaContainerModeDrive,
		WekaContainerModeCompute,
		WekaContainerModeS3,
		WekaContainerModeEnvoy,
		WekaContainerModeDist,
		WekaContainerModeDriversDist,
		WekaContainerModeAdhocOpWC,
		WekaContainerModeNfs,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostWideSingleton() bool {
	return slices.Contains([]string{WekaContainerModeEnvoy, WekaContainerModeS3, WekaContainerModeNfs}, w.Spec.Mode)
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

func (w *WekaContainer) ToContainerDetails() *WekaContainerDetails {
	return &WekaContainerDetails{
		Image:           w.Spec.Image,
		ImagePullSecret: w.Spec.ImagePullSecret,
		Tolerations:     w.Spec.Tolerations,
		Labels:          w.ObjectMeta.GetLabels(),
		Affinity:        w.Spec.Affinity,
	}
}

func (w *WekaContainer) IsOneOff() bool {
	return slices.Contains([]string{WekaContainerModeAdhocOpWC, WekaContainerModeDiscovery, WekaContainerModeAdhocOp, WekaContainerModeDriversBuilder, WekaContainerModeDriversLoader}, w.Spec.Mode)

}

func (w *WekaContainer) IsClientContainer() bool {
	return slices.Contains([]string{WekaContainerModeClient}, w.Spec.Mode)
}

func (w *WekaContainer) IsProtocolContainer() bool {
	return slices.Contains([]string{WekaContainerModeNfs, WekaContainerModeS3}, w.Spec.Mode)
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
	return slices.Contains([]string{WekaContainerModeEnvoy}, w.Spec.Mode)
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

type WekaContainerDetails struct {
	Image           string            `json:"image"`
	ImagePullSecret string            `json:"imagePullSecrets"`
	Tolerations     []v1.Toleration   `json:"tolerations,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Affinity        *v1.Affinity      `json:"affinity,omitempty"`
}

func (c *WekaContainerSpec) GetOverrides() *WekaContainerSpecOverrides {
	if c.Overrides == nil {
		return &WekaContainerSpecOverrides{}
	} else {
		return c.Overrides
	}
}
