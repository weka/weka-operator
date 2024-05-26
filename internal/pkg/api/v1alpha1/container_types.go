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
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

type WekaContainerMode string

const (
	WekaContainerModeDist          = "dist"
	WekaContainerModeDriversLoader = "drivers-loader"
	WekaContainerModeCompute       = "compute"
	WekaContainerModeDrive         = "drive"
	WekaContainerModeClient        = "client"
	WekaContainerModeDiscovery     = "discovery"
	WekaContainerModeS3            = "s3"
)

type S3Params struct {
	EnvoyPort      int `json:"envoyPort,omitempty"`
	EnvoyAdminPort int `json:"envoyAdminPort,omitempty"`
	S3Port         int `json:"s3Port,omitempty"`
}

type WekaContainerSpec struct {
	NodeAffinity            string            `json:"nodeAffinity,omitempty"`
	NodeSelector            map[string]string `json:"nodeSelector,omitempty"`
	Port                    int               `json:"port,omitempty"`
	AgentPort               int               `json:"agentPort,omitempty"`
	Image                   string            `json:"image"`
	ImagePullSecret         string            `json:"imagePullSecret,omitempty"`
	BuildkitImagePullSecret string            `json:"buildkitImagePullSecret,omitempty"`
	WekaContainerName       string            `json:"name"`
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
	PotentialDrives     []string             `json:"driveOptions,omitempty"` // Whole reason of this struct is not having persistend handler for drives
	NumDrives           int                  `json:"numDrives,omitempty"`
	DriversDistService  string               `json:"driversDistService,omitempty"`
	WekaSecretRef       v1.EnvVarSource      `json:"wekaSecretRef,omitempty"`
	JoinIps             []string             `json:"joinIpPorts,omitempty"`
	AppendSetupCommand  string               `json:"appendSetupCommand,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	S3Params            *S3Params            `json:"s3Params,omitempty"`
	Tolerations         []v1.Toleration      `json:"tolerations,omitempty"`
	NodeInfoConfigMap   string               `json:"nodeInfoConfigMap,omitempty"`
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
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader, WekaContainerModeDiscovery}, w.Spec.Mode)
}

func (w *WekaContainer) IsDriversContainer() bool {
	return slices.Contains([]string{WekaContainerModeDist, WekaContainerModeDriversLoader}, w.Spec.Mode)
}

func (w *WekaContainer) IsBackend() bool {
	return slices.Contains([]string{WekaContainerModeDrive, WekaContainerModeCompute, WekaContainerModeS3}, w.Spec.Mode)
}
