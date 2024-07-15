package v1alpha1

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"slices"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Weka container status",priority=1
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode",description="Weka container mode",priority=2
// +kubebuilder:printcolumn:name="Hugepages Count",type="integer",JSONPath=".spec.hugepages",description="Hugepages count",priority=3
// +kubebuilder:printcolumn:name="Hugepages Size",type="integer",JSONPath=".spec.hugepagesSize",description="Hugepages size",priority=4
// +kubebuilder:printcolumn:name="Drives Count",type="integer",JSONPath=".spec.numDrives",description="Number of drives attached to container",priority=5
// +kubebuilder:printcolumn:name="Weka Container Name",type="string",JSONPath=".spec.wekaContainerName",description="Weka container name",priority=6

type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

type WekaContainerMode string

const (
	WekaContainerModeDist             = "dist"
	WekaContainerModeDriversLoader    = "drivers-loader"
	WekaContainerModeCompute          = "compute"
	WekaContainerModeDrive            = "drive"
	WekaContainerModeClient           = "client"
	WekaContainerModeDiscovery        = "discovery"
	WekaContainerModeS3               = "s3"
	WekaContainerModeEnvoy            = "envoy"
	WekaContainerModeBuild            = "build"
	PersistentContainersLocation      = "/opt/k8s-weka/containers"
	PersistentContainersLocationCos   = "/mnt/stateful_partition/k8s-weka/containers"
	PersistentContainersLocationRhCos = "/root/k8s-weka/containers"
	OsNameOpenshift                   = "rhcos"
	OsNameCos                         = "cos"
)

type S3Params struct {
	EnvoyPort      int `json:"envoyPort,omitempty"`
	EnvoyAdminPort int `json:"envoyAdminPort,omitempty"`
	S3Port         int `json:"s3Port,omitempty"`
}

type CoreOSBuildSpec struct {
	DriverToolkitImagePullSecret string `json:"driverToolkitImagePullSecret,omitempty"`
	DriverToolkitImage           string `json:"driverToolkitImage,omitempty"`
}

type COSBuildSpec struct {
	OsBuildId               string `json:"osBuildId,omitempty"`
	GcloudCredentialsSecret string `json:"gcloudCredentialsSecret,omitempty"`
}

type WekaContainerSpec struct {
	NodeAffinity      string            `json:"nodeAffinity,omitempty"`
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	Port              int               `json:"port,omitempty"`
	AgentPort         int               `json:"agentPort,omitempty"`
	Image             string            `json:"image"`
	ImagePullSecret   string            `json:"imagePullSecret,omitempty"`
	WekaContainerName string            `json:"name"`
	CoreOSBuildSpec   *CoreOSBuildSpec  `json:"coreOSBuildSpec,omitempty"`
	COSBuildSpec      *COSBuildSpec     `json:"cosBuildSpec,omitempty"`
	// +kubebuilder:validation:Enum="cos";rhcos;"ubuntu";""
	OsDistro string `json:"osDistro,omitempty"`
	// +kubebuilder:validation:Enum=drive;compute;client;dist;drivers-loader;discovery;s3
	Mode       string `json:"mode"`
	NumCores   int    `json:"numCores"`             //numCores is weka-specific cores
	ExtraCores int    `json:"extraCores,omitempty"` //extraCores is temporary solution for S3 containers, cores allocation on top of weka cores
	CoreIds    []int  `json:"coreIds,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	// +kubebuilder:default=auto
	CpuPolicy           CpuPolicy            `json:"cpuPolicy,omitempty"`
	Network             Network              `json:"network,omitempty"`
	Hugepages           int                  `json:"hugepages,omitempty"`
	HugepagesSize       string               `json:"hugepagesSize,omitempty"`
	HugepagesOverride   string               `json:"hugepagesSizeOverride,omitempty"`
	PotentialDrives     []string             `json:"driveOptions,omitempty"` // Whole reason of this struct is not having persistent handler for drives
	NumDrives           int                  `json:"numDrives,omitempty"`
	DriversDistService  string               `json:"driversDistService,omitempty"`
	WekaSecretRef       v1.EnvVarSource      `json:"wekaSecretRef,omitempty"`
	JoinIps             []string             `json:"joinIpPorts,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations         []v1.Toleration      `json:"tolerations,omitempty"`
	NodeInfoConfigMap   string               `json:"nodeInfoConfigMap,omitempty"`
	Ipv6                bool                 `json:"ipv6,omitempty"`
	AdditionalMemory    int                  `json:"additionalMemory,omitempty"`
	ForceAllowDriveSign bool                 `json:"forceAllowDriveSign,omitempty"`
	Group               string               `json:"group,omitempty"`
	ServiceAccountName  string               `json:"serviceAccountName,omitempty"`
	AdditionalSecrets   map[string]string    `json:"additionalSecrets,omitempty"`
}

type Network struct {
	EthDevices []string `json:"ethDevices,omitempty"`
	EthDevice  string   `json:"ethDevice,omitempty"`
	UdpMode    bool     `json:"udpMode,omitempty"`
}

type WekaContainerStatus struct {
	Status             string             `json:"status"`
	ManagementIP       string             `json:"managementIP,omitempty"`
	ClusterContainerID *int               `json:"containerID,omitempty"`
	ClusterID          string             `json:"clusterID,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	LastAppliedImage   string             `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
}

// TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes
type TracesConfiguration struct {
	// +kubebuilder:default=10
	MaxCapacityPerIoNode int `json:"maxCapacityPerIoNode,omitempty"`
	// +kubebuilder:default=20
	EnsureFreeSpace int `json:"ensureFreeSpace,omitempty"`
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
	return !w.IsDistMode() && !w.IsDriversLoaderMode()
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
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeDiscovery, WekaContainerModeBuild, WekaContainerModeEnvoy}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostNetwork() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeDiscovery, WekaContainerModeBuild}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversContainer() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeBuild}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversBuilder() bool {
	return w.Spec.Mode == WekaContainerModeDist
}

func (w *WekaContainer) IsBackend() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) IsDiscoveryContainer() bool {
	return w.Spec.Mode == WekaContainerModeDiscovery
}

func (w *WekaContainer) GetOsDistro() string {
	return w.Spec.OsDistro
}

func (w *WekaContainer) IsOpenshift() bool {
	return w.Spec.OsDistro == OsNameOpenshift
}

func (w *WekaContainer) IsCos() bool {
	return w.Spec.OsDistro == OsNameCos
}

func (w *WekaContainer) IsUnspecifiedOs() bool {
	return w.Spec.OsDistro == ""
}

func (w *WekaContainer) GetPersistentLocation() string {
	if w.Spec.OsDistro == OsNameOpenshift {
		return PersistentContainersLocationRhCos //TODO: check persistence for openshift
	} else if w.Spec.OsDistro == OsNameCos {
		return PersistentContainersLocationCos
	}
	return PersistentContainersLocation
}

func (w *WekaContainer) IsWekaContainer() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) IsAllocatable() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeEnvoy, WekaContainerModeS3}, w.Spec.Mode)
}

func (w *WekaContainer) HasAgent() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3, WekaContainerModeEnvoy, WekaContainerModeDist}, w.Spec.Mode)
}

func (w *WekaContainer) IsHostWideSingleton() bool {
	return slices.Contains([]string{WekaContainerModeEnvoy, WekaContainerModeS3}, w.Spec.Mode)
}
