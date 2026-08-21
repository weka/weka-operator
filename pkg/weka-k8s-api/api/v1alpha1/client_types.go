/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type WekaClientStatusEnum string

const (
	WekaClientStatusInit       WekaClientStatusEnum = "Init"
	WekaClientStatusRunning    WekaClientStatusEnum = "Running"
	WekaClientStatusUpgrading  WekaClientStatusEnum = "Upgrading"
	WekaClientStatusDestroying WekaClientStatusEnum = "Destroying"
)

type DriverSpec struct{}

type ClientContainerSpec struct {
	Debug bool `json:"debug,omitempty"`
}

type ObjectReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type WekahomeClientConfig struct {
	CacertSecret string `json:"cacertSecret,omitempty"`
}

type UpgradePolicyType string

const (
	UpgradePolicyTypeManual         UpgradePolicyType = "manual"
	UpgradePolicyTypeRolling        UpgradePolicyType = "rolling"
	UpgradePolicyTypeAllAtOnce      UpgradePolicyType = "all-at-once"
	UpgradePolicyTypeAllAtOnceForce UpgradePolicyType = "all-at-once-force"
)

type WekaClientSpecOverrides struct {
	// can be used to specify a build_id for a driver in the distributor service, keep empty for auto detection default
	DriversBuildId     *string `json:"driversBuildId,omitempty"`
	DriversLoaderImage string  `json:"driversLoaderImage,omitempty"`
	// used to override machine identifier node reference for client containers
	MachineIdentifierNodeRef string `json:"machineIdentifierNodeRef,omitempty"`
	// unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner
	ForceDrain bool `json:"forceDrain,omitempty"`
	// option to skip active mounts check before deleting client containers
	SkipActiveMountsCheck bool `json:"skipActiveMountsCheck,omitempty"`
	// unsafe operation, runs nsenter in root namespace to umount all wekafs mounts visible on host
	UmountOnHost bool `json:"umountOnHost,omitempty"`
	// unsafe parameter, disables anti-affinities on client pods, allowing to schedule more than one client pod per node.
	// Running multiple clients for multiple clusters on the same node is not fully supported yet, and this flag should not be used in production.
	DropAffinityConstraints bool `json:"dropAffinityConstraints,omitempty"`
	// override name used in weka local setup for the container
	// this can be used for integration with external client on the host
	WekaContainerName string `json:"wekaContainerName,omitempty"`
	DpdkBaseMemoryMb  int    `json:"dpdkBaseMemoryMb,omitempty"`
	// how long to wait, once IO processes are reported up, before considering the container's applied
	// image settled. nil/0 (default): don't wait.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +optional
	WaitSinceIoProcessesUpTimeout *metav1.Duration `json:"waitSinceIoProcessesUpTimeout,omitempty"`
}

type UpgradePolicy struct {
	// +kubebuilder:validation:Enum=manual;all-at-once;rolling;all-at-once-force
	// +kubebuilder:default=all-at-once
	Type UpgradePolicyType `json:"type,omitempty"`
}

type PortRange struct {
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=45000
	BasePort int `json:"basePort"`
	// +kubebuilder:default=0
	// number of ports to check for availability
	// if 0 - will check all ports from basePort to 65535
	PortRange int `json:"portRange,omitempty"`
}

type PodResources struct {
	Cpu    resource.Quantity `json:"cpu,omitempty"`
	Memory resource.Quantity `json:"memory,omitempty"`
	// Hugepages2Mi is requested verbatim as the pod's hugepages-2Mi resource. 1Gi pages are
	// not settable through this field.
	Hugepages2Mi resource.Quantity `json:"hugepages-2Mi,omitempty"`
}

type PodResourcesSpec struct {
	Requests PodResources `json:"requests,omitempty"`
	Limits   PodResources `json:"limits,omitempty"`
}

type ClientCsiConfig struct {
	CsiGroup                  string             `json:"csiGroup,omitempty"`
	DisableControllerCreation bool               `json:"disableControllerCreation,omitempty"`
	Advanced                  *AdvancedCsiConfig `json:"advanced,omitempty"`
}

type AdvancedCsiConfig struct {
	EnforceTrustedHttps   bool              `json:"enforceTrustedHttps,omitempty"`
	NodeLabels            map[string]string `json:"nodeLabels,omitempty"`
	NodeTolerations       []v1.Toleration   `json:"nodeTolerations,omitempty"`
	ControllerLabels      map[string]string `json:"controllerLabels,omitempty"`
	ControllerTolerations []v1.Toleration   `json:"controllerTolerations,omitempty"`
	SkipGarbageCollection bool              `json:"skipGarbageCollection,omitempty"`
}

// WekaClientSpec defines the desired state of WekaClient
type WekaClientSpec struct {
	// full container image in format of quay.io/weka.io/weka-in-container:VERSION
	// +kubebuilder:validation:Pattern=`^.+:\d+\.\d+\.\d+.*$`
	Image           string `json:"image"`
	ImagePullSecret string `json:"imagePullSecret,omitempty"`
	// if not set (0), weka will find a free port from the portRange
	Port int `json:"port,omitempty"`
	// if not set (0), weka will find a free port from the portRange
	AgentPort int `json:"agentPort,omitempty"`
	// used for dynamic port allocation
	PortRange          *PortRange        `json:"portRange,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	WekaSecretRef      string            `json:"wekaSecretRef,omitempty"`
	Network            Network           `json:"network,omitempty"`
	DriversDistService string            `json:"driversDistService,omitempty"`
	JoinIps            []string          `json:"joinIpPorts,omitempty"`
	TargetCluster      ObjectReference   `json:"targetCluster,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy   CpuPolicy `json:"cpuPolicy,omitempty"`
	CpuRequest  string    `json:"cpuRequest,omitempty"`
	CoresNumber int       `json:"coresNum,omitempty"`
	// extraCores reserves additional CPUs for the pod on top of the weka FE cores.
	// +kubebuilder:validation:Minimum=0
	ExtraCores          int                  `json:"extraCores,omitempty"`
	CoreIds             []int                `json:"coreIds,omitempty"`
	NonDatapathCoreIds  []int                `json:"nonDatapathCoreIds,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations         []string             `json:"tolerations,omitempty"`
	RawTolerations      []v1.Toleration      `json:"rawTolerations,omitempty"`
	ServiceAccountName  string               `json:"serviceAccountName,omitempty"`
	// memory to add/decrease from "auto-calculated" memory
	AdditionalMemory int `json:"additionalMemory,omitempty"`
	// experimental: pod resources to be proxied as-is to the pod spec
	Resources *PodResourcesSpec `json:"resources,omitempty"`
	// hugepages, value in megabytes
	HugePages int `json:"hugepages,omitempty"`
	// value in megabytes to offset
	HugePagesOffset *int `json:"hugepagesOffset,omitempty"`
	//DEPRECATED, kept for compatibility with old API clients, not taking any action, to be removed on new API version
	WekaHomeConfig  WekahomeClientConfig     `json:"wekaHomeConfig,omitempty"`
	WekaHome        *WekahomeClientConfig    `json:"wekaHome,omitempty"`
	UpgradePolicy   UpgradePolicy            `json:"upgradePolicy,omitempty"`
	AllowHotUpgrade bool                     `json:"allowHotUpgrade,omitempty"`
	Overrides       *WekaClientSpecOverrides `json:"overrides,omitempty"`

	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +kubebuilder:default="24h"
	// sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config
	AutoRemoveTimeout metav1.Duration `json:"autoRemoveTimeout,omitempty"`
	GlobalPVC         *PVCConfig      `json:"globalPVC,omitempty"`

	// +kubebuilder:validation:Type=object
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: if set, allows to reuse the same csi resources for multiple clients
	CsiConfig *ClientCsiConfig `json:"csiConfig,omitempty"`

	// Numa configures NUMA confinement for this client container
	Numa *WekaNuma `json:"numa,omitempty"`
}

func (c *WekaClientSpec) GetCsiConfig() ClientCsiConfig {
	if c.CsiConfig == nil {
		return ClientCsiConfig{}
	}
	return *c.CsiConfig
}

// WekaClientStatus defines the observed state of WekaClient
type WekaClientStatus struct {
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
	LastAppliedSpec string             `json:"lastAppliedSpec,omitempty"`
	// +kubebuilder:validation:Enum=Init;Running;Upgrading;Destroying
	// +kubebuilder:default=Init
	Status         WekaClientStatusEnum `json:"status,omitempty"`
	Stats          *ClientMetrics       `json:"stats,omitempty"`
	PrinterColumns ClientPrinterColumns `json:"printer,omitempty"`
}

type ClientPrinterColumns struct {
	Containers StringMetric `json:"containers,omitempty"`
}

type ClientMetrics struct {
	Containers EntityStatefulNum `json:"containers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Resource status",priority=0
// +kubebuilder:printcolumn:name="Target Cluster",type="string",JSONPath=".spec.targetCluster.name",description="Name of the target cluster if exists",priority=0
// +kubebuilder:printcolumn:name="Cores",type="integer",JSONPath=".spec.coresNum",description="Number of cores",priority=0
// +kubebuilder:printcolumn:name="Containers(A/C/D)",type="string",JSONPath=".status.printer.containers",description="Number of client containers: Active/Created/Desired",priority=0

// WekaClient is the Schema for the clients API
type WekaClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaClientSpec   `json:"spec,omitempty"`
	Status WekaClientStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WekaClientList contains a list of WekaClient
type WekaClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaClient `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WekaClient{}, &WekaClientList{})
}

func (c *WekaClientSpec) GetOverrides() *WekaClientSpecOverrides {
	if c.Overrides == nil {
		return &WekaClientSpecOverrides{}
	} else {
		return c.Overrides
	}
}

func (c *WekaClient) IsMarkedForDeletion() bool {
	return !c.GetDeletionTimestamp().IsZero()
}
