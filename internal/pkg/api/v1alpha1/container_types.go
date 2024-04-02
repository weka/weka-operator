package v1alpha1

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

type WekaContainerSpec struct {
	NodeAffinity      string            `json:"nodeAffinity,omitempty"`
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	Port              int               `json:"port,omitempty"`
	AgentPort         int               `json:"agentPort,omitempty"`
	Image             string            `json:"image"`
	ImagePullSecret   string            `json:"imagePullSecret,omitempty"`
	WekaContainerName string            `json:"name"`
	Mode              string            `json:"mode"` // TODO: How to define as enum?
	NumCores          int               `json:"numCores"`
	CoreIds           []int             `json:"coreIds,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy          CpuPolicy       `json:"cpuPolicy,omitempty"`
	Network            Network         `json:"network,omitempty"`
	Hugepages          int             `json:"hugepages,omitempty"`
	HugepagesSize      string          `json:"hugepagesSize,omitempty"`
	HugepagesOverride  string          `json:"hugepagesSizeOverride,omitempty"`
	PotentialDrives    []string        `json:"driveOptions,omitempty"` // Whole reason of this struct is not having persistend handler for drives
	NumDrives          int             `json:"numDrives,omitempty"`
	DriversDistService string          `json:"driversDistService,omitempty"`
	WekaSecretRef      v1.EnvVarSource `json:"wekaSecretRef,omitempty"`
	JoinIps            []string        `json:"joinIpPorts,omitempty"`
	AppendSetupCommand string          `json:"appendSetupCommand,omitempty"`
}

type Network struct {
	EthDevice string `json:"ethDevice,omitempty"`
	UdpMode   bool   `json:"udpMode,omitempty"`
}

type WekaContainerStatus struct {
	Status             string             `json:"status"`
	ManagementIP       string             `json:"managementIP,omitempty"`
	ClusterContainerID *int               `json:"containerID,omitempty"`
	ClusterID          string             `json:"clusterID,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
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
	return w.Spec.Mode == "dist"
}

func (w *WekaContainer) IsDriversLoaderMode() bool {
	return w.Spec.Mode == "drivers-loader"
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
