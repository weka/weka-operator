package v1alpha1

import (
	"github.com/weka/weka-operator/internal/controllers/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"slices"
)

type NodeName types.NodeName

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Weka container status",priority=1
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode",description="Weka container mode",priority=2
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation",priority=3
// +kubebuilder:printcolumn:name="Drives Count",type="integer",JSONPath=".spec.numDrives",description="Number of drives attached to container",priority=5
// +kubebuilder:printcolumn:name="Weka cID",type="string",JSONPath=".status.containerID",description="Weka container ID",priority=6
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

type WekaContainerMode string

const (
	WekaContainerModeDist           = "dist"
	WekaContainerModeDriversLoader  = "drivers-loader"
	WekaContainerModeDriversBuilder = "drivers-builder"
	WekaContainerModeCompute        = "compute"
	WekaContainerModeDrive          = "drive"
	WekaContainerModeClient         = "client"
	WekaContainerModeDiscovery      = "discovery"
	WekaContainerModeS3             = "s3"
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

type WekaContainerSpec struct {
	NodeAffinity      NodeName          `json:"nodeAffinity,omitempty"`
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	Port              int               `json:"port,omitempty"`
	AgentPort         int               `json:"agentPort,omitempty"`
	Image             string            `json:"image"`
	ImagePullSecret   string            `json:"imagePullSecret,omitempty"`
	WekaContainerName string            `json:"name"`
	// +kubebuilder:validation:Enum=drive;compute;client;dist;drivers-loader;drivers-builder;discovery;s3;adhoc-op-with-container;adhoc-op
	Mode       string `json:"mode"`
	NumCores   int    `json:"numCores"`             //numCores is weka-specific cores
	ExtraCores int    `json:"extraCores,omitempty"` //extraCores is temporary solution for S3 containers, cores allocation on top of weka cores
	CoreIds    []int  `json:"coreIds,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	// +kubebuilder:default=auto
	CpuPolicy             CpuPolicy            `json:"cpuPolicy,omitempty"`
	Network               Network              `json:"network,omitempty"`
	Hugepages             int                  `json:"hugepages,omitempty"`
	HugepagesSize         string               `json:"hugepagesSize,omitempty"`
	HugepagesOverride     string               `json:"hugepagesSizeOverride,omitempty"`
	RemovePotentialDrives []string             `json:"driveOptions,omitempty"` // Whole reason of this struct is not having persistent handler for drives
	NumDrives             int                  `json:"numDrives,omitempty"`
	DriversDistService    string               `json:"driversDistService,omitempty"`
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
	Instructions          string               `json:"instructions,omitempty"`
	NoAffinityConstraints bool                 `json:"dropAffinityConstraints,omitempty"`
	UploadResultsTo       string               `json:"uploadResultsTo,omitempty"`
}

type Network struct {
	EthDevices []string `json:"ethDevices,omitempty"`
	EthDevice  string   `json:"ethDevice,omitempty"`
	UdpMode    bool     `json:"udpMode,omitempty"`
}

type ContainerAllocations struct {
	Drives    []string `json:"drives,omitempty"`
	EthSlots  []string `json:"ethSlots,omitempty"`
	LbPort    int      `json:"lbPort,omitempty"`
	WekaPort  int      `json:"wekaPort,omitempty"`
	AgentPort int      `json:"agentPort,omitempty"`
}

type WekaContainerStatus struct {
	Status             string                `json:"status"`
	ManagementIP       string                `json:"managementIP,omitempty"`
	ClusterContainerID *int                  `json:"containerID,omitempty"`
	ClusterID          string                `json:"clusterID,omitempty"`
	Conditions         []metav1.Condition    `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	LastAppliedImage   string                `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	NodeAffinity       NodeName              `json:"nodeAffinity,omitempty"`     // active nodeAffinity, copied from spec and populated if ondeSelector was used instead of direct nodeAffinity
	ExecutionResult    *string               `json:"result,omitempty"`
	Allocations        *ContainerAllocations `json:"allocations,omitempty"`
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
	return w.Spec.Mode == WekaContainerModeDist
}

func (w *WekaContainer) IsDriversLoaderMode() bool {
	return w.Spec.Mode == WekaContainerModeDriversLoader
}

func (w *WekaContainer) SupportsEnsureDriversCondition() bool {
	return w.IsWekaContainer()
}

func (w *WekaContainer) InitEnsureDriversCondition() {
	if !w.DriversReady() && w.SupportsEnsureDriversCondition() {
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
			Type:    condition.CondEnsureDrivers,
			Status:  metav1.ConditionFalse,
			Message: "Init",
		})
	}
}

func (w *WekaContainer) IsServiceContainer() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeDiscovery, WekaContainerModeDriversBuilder, WekaContainerModeEnvoy, WekaContainerModeAdhocOpWC, WekaContainerModeAdhocOp}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostNetwork() bool {
	return w.IsWekaContainer()
}

func (w *WekaContainer) IsDriversContainer() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeDriversBuilder}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversBuilder() bool {
	return w.Spec.Mode == WekaContainerModeDriversBuilder
}

func (w *WekaContainer) IsBackend() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) IsDiscoveryContainer() bool {
	return w.Spec.Mode == WekaContainerModeDiscovery
}

func (w *WekaContainer) IsAdhocOpContainer() bool {
	return slices.Contains([]string{WekaContainerModeAdhocOpWC, WekaContainerModeAdhocOp}, w.Spec.Mode)
}

func (w *WekaContainer) HasPersistentStorage() bool {
	return slices.Contains([]string{
		WekaContainerModeDrive,
		WekaContainerModeCompute,
		WekaContainerModeS3,
		WekaContainerModeEnvoy,
		WekaContainerModeClient,
		WekaContainerModeDist,
	}, w.Spec.Mode)
}

func (w *WekaContainer) IsS3Container() bool {
	return slices.Contains([]string{WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) HasJoinIps() bool {
	return w.Spec.JoinIps != nil && len(w.Spec.JoinIps) > 0
}

func (w *WekaContainer) IsDriveContainer() bool {
	return slices.Contains([]string{WekaContainerModeDrive}, w.Spec.Mode)
}

func (w *WekaContainer) HasFrontend() bool {
	return slices.Contains([]string{WekaContainerModeClient, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) IsWekaContainer() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3, WekaContainerModeClient}, w.Spec.Mode)
}

func (w *WekaContainer) IsAllocatable() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeEnvoy, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) HasAgent() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3, WekaContainerModeEnvoy, WekaContainerModeDist, WekaContainerModeAdhocOpWC}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostWideSingleton() bool {
	return slices.Contains([]string{WekaContainerModeEnvoy, WekaContainerModeS3}, w.Spec.Mode)
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

func (w *WekaContainer) ToOwnerObject() *WekaContainerDetails {
	return &WekaContainerDetails{
		Image:           w.Spec.Image,
		ImagePullSecret: w.Spec.ImagePullSecret,
		Tolerations:     w.Spec.Tolerations,
	}
}

func (w *WekaContainer) IsOneOff() bool {
	return slices.Contains([]string{
		WekaContainerModeAdhocOpWC,
		WekaContainerModeDiscovery,
		WekaContainerModeAdhocOp,
		WekaContainerModeDriversLoader,
		WekaContainerModeDriversBuilder,
	}, w.Spec.Mode)

}

func (w *WekaContainer) IsClientContainer() bool {
	return slices.Contains([]string{WekaContainerModeClient}, w.Spec.Mode)
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

type WekaContainerDetails struct {
	Image           string          `json:"image"`
	ImagePullSecret string          `json:"imagePullSecrets"`
	Tolerations     []v1.Toleration `json:"tolerations,omitempty"`
}
