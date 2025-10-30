/*
Copyright 2024.

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
	"slices"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-k8s-api/util"
)

type WekaClusterStatusEnum string

const (
	WekaClusterStatusInit         WekaClusterStatusEnum = "Init"
	WekaClusterStatusReady        WekaClusterStatusEnum = "Ready"
	WekaClusterStatusWaitDrives   WekaClusterStatusEnum = "WaitForDrives"
	WekaClusterStatusStartingIO   WekaClusterStatusEnum = "StartingIO"
	WekaClusterStatusGracePeriod  WekaClusterStatusEnum = "GracePeriod"
	WekaClusterStatusDestroying   WekaClusterStatusEnum = "Destroying"
	WekaClusterStatusDeallocating WekaClusterStatusEnum = "Deallocating"
)

type NetworkSelector struct {
	Subnet      string   `json:"subnet,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         int      `json:"max,omitempty"`
	DeviceNames []string `json:"deviceNames,omitempty"`
}

func (n *NetworkSelector) Equal(o *NetworkSelector) bool {
	if n == nil && o == nil {
		return true
	}
	if n == nil || o == nil {
		return false
	}

	return n.Subnet == o.Subnet &&
		n.Min == o.Min &&
		n.Max == o.Max &&
		slices.Equal(n.DeviceNames, o.DeviceNames)
}

type Network struct {
	EthDevice  string   `json:"ethDevice,omitempty"`
	EthDevices []string `json:"ethDevices,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	UdpMode    bool     `json:"udpMode,omitempty"`
	// subnet that is used for devices auto-discovery
	// +kubebuilder:validation:items:Pattern="^([0-9]{1,3}\\.){3}[0-9]{1,3}\\/[0-9]{1,2}$"
	DeviceSubnets          []string          `json:"deviceSubnets,omitempty"`
	Selectors              []NetworkSelector `json:"selectors,omitempty"`
	ManagementIPsSelectors []NetworkSelector `json:"managementIpsSelectors,omitempty"`
	// BindManagementAll controls whether Weka containers bind to all network interfaces or only to specific management interfaces.
	// When set to false (default), containers will only listen on the management ips interfaces (restrict_listen mode).
	// When set to true, containers will listen on all ips (0.0.0.0) instead of specific IP addresses.
	BindManagementAll bool `json:"bindManagementAll,omitempty"`
}

func (n *Network) Equal(o *Network) bool {
	if n == nil && o == nil {
		return true
	}
	if n == nil || o == nil {
		return false
	}

	// Compare simple fields
	if n.EthDevice != o.EthDevice ||
		n.Gateway != o.Gateway ||
		n.UdpMode != o.UdpMode ||
		n.BindManagementAll != o.BindManagementAll {
		return false
	}

	// Compare string slices
	if !slices.Equal(n.EthDevices, o.EthDevices) {
		return false
	}

	if !slices.Equal(n.DeviceSubnets, o.DeviceSubnets) {
		return false
	}

	// Compare NetworkSelector slices
	if len(n.Selectors) != len(o.Selectors) {
		return false
	}
	for i, v := range n.Selectors {
		if !v.Equal(&o.Selectors[i]) {
			return false
		}
	}

	if len(n.ManagementIPsSelectors) != len(o.ManagementIPsSelectors) {
		return false
	}
	for i, v := range n.ManagementIPsSelectors {
		if !v.Equal(&o.ManagementIPsSelectors[i]) {
			return false
		}
	}

	return true
}

type AdditionalMemory struct {
	Compute int `json:"compute,omitempty"`
	Drive   int `json:"drive,omitempty"`
	S3      int `json:"s3,omitempty"`
	Nfs     int `json:"nfs,omitempty"`
	Envoy   int `json:"envoy,omitempty"`
}

func (a *AdditionalMemory) GetForMode(mode string) int {
	additionalMemory := 0
	switch mode {
	case WekaContainerModeDrive:
		additionalMemory = a.Drive
	case WekaContainerModeCompute:
		additionalMemory = a.Compute
	case WekaContainerModeS3:
		additionalMemory = a.S3
	case WekaContainerModeNfs:
		additionalMemory = a.Nfs
	case WekaContainerModeEnvoy:
		additionalMemory = a.Envoy
	}
	return additionalMemory
}

type WekaConfig struct {
	ComputeContainers         *int `json:"computeContainers,omitempty"`
	DriveContainers           *int `json:"driveContainers,omitempty"`
	S3Containers              int  `json:"s3Containers,omitempty"`
	ComputeCores              int  `json:"computeCores,omitempty"`
	DriveCores                int  `json:"driveCores,omitempty"`
	S3Cores                   int  `json:"s3Cores,omitempty"`
	NumDrives                 int  `json:"numDrives,omitempty"`
	ComputeExtraCores         int  `json:"computeExtraCores,omitempty"`
	DriveExtraCores           int  `json:"driveExtraCores,omitempty"`
	S3ExtraCores              int  `json:"s3ExtraCores,omitempty"`
	DriveHugepages            int  `json:"driveHugepages,omitempty"`
	DriveHugepagesOffset      int  `json:"driveHugepagesOffset,omitempty"`
	ComputeHugepages          int  `json:"computeHugepages,omitempty"`
	ComputeHugepagesOffset    int  `json:"computeHugepagesOffset,omitempty"`
	S3FrontendHugepages       int  `json:"s3FrontendHugepages,omitempty"`
	S3FrontendHugepagesOffset int  `json:"s3FrontendHugepagesOffset,omitempty"`
	EnvoyCores                int  `json:"envoyCores,omitempty"`
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS containers
	NfsContainers int `json:"nfsContainers,omitempty"`
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS cores per container
	NfsCores int `json:"nfsCores,omitempty"`
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS extra cores per container
	NfsExtraCores int `json:"nfsExtraCores,omitempty"`
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage allocation for NFS frontend
	NfsFrontendHugepages int `json:"nfsFrontendHugepages,omitempty"`
	// EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for NFS frontend
	NfsFrontendHugepagesOffset int `json:"nfsFrontendHugepagesOffset,omitempty"`
}

type WekaHomeConfig struct {
	Endpoint      string `json:"endpoint,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	CacertSecret  string `json:"cacertSecret,omitempty"`
	EnableStats   *bool  `json:"enableStats,omitempty"`
}

type RoleNodeSelector struct {
	// nodeSelector for compute weka containers
	Compute *map[string]string `json:"compute,omitempty"`
	// nodeSelector for drive weka containers
	Drive *map[string]string `json:"drive,omitempty"`
	// nodeSelector for s3 weka containers
	S3 *map[string]string `json:"s3,omitempty"`
	// nodeSelector for nfs weka containers
	Nfs *map[string]string `json:"nfs,omitempty"`
}

type RoleAnnotations struct {
	// annotations for compute weka containers
	Compute *map[string]string `json:"compute,omitempty"`
	// annotations for drive weka containers
	Drive *map[string]string `json:"drive,omitempty"`
	// annotations for s3 weka containers
	S3 *map[string]string `json:"s3,omitempty"`
	// annotations for nfs weka containers
	Nfs *map[string]string `json:"nfs,omitempty"`
}

type RoleNetworkSelector struct {
	// network selector for compute weka containers
	Compute *Network `json:"compute,omitempty"`
	// network selector for drive weka containers
	Drive *Network `json:"drive,omitempty"`
	// network selector for s3 weka containers
	S3 *Network `json:"s3,omitempty"`
	// network selector for nfs weka containers
	Nfs *Network `json:"nfs,omitempty"`
}

// RoleCoreIds defines CPU core id lists per container role for Manual CPU policy.
// Each slice contains the core IDs (as visible to the node OS) that should be
// pinned to every container of the corresponding role. If a slice is empty or
// omitted, no explicit core pinning will be applied for that role.
// +kubebuilder:validation:Type=object
// +kubebuilder:validation:Optional
type RoleCoreIds struct {
	// +kubebuilder:validation:Optional
	Compute []int `json:"compute,omitempty"`
	// +kubebuilder:validation:Optional
	Drive []int `json:"drive,omitempty"`
	// +kubebuilder:validation:Optional
	S3 []int `json:"s3,omitempty"`
	// +kubebuilder:validation:Optional
	Nfs []int `json:"nfs,omitempty"`
}

type RoleTopologySpreadConstraints struct {
	Compute []v1.TopologySpreadConstraint `json:"compute,omitempty"`
	Drive   []v1.TopologySpreadConstraint `json:"drive,omitempty"`
	S3      []v1.TopologySpreadConstraint `json:"s3,omitempty"`
	Nfs     []v1.TopologySpreadConstraint `json:"nfs,omitempty"`
}

func (c *RoleTopologySpreadConstraints) ForRole(role string) []v1.TopologySpreadConstraint {
	switch role {
	case "compute":
		return c.Compute
	case "drive":
		return c.Drive
	case "s3":
		return c.S3
	case "nfs":
		return c.Nfs
	default:
		return nil
	}
}

type RoleAffinity struct {
	Compute *v1.Affinity `json:"compute,omitempty"`
	Drive   *v1.Affinity `json:"drive,omitempty"`
	S3      *v1.Affinity `json:"s3,omitempty"`
	Nfs     *v1.Affinity `json:"nfs,omitempty"`
}

type PodConfiguration struct {
	// controls the distribution of weka containers across the failure domainsqq
	TopologySpreadConstraints []v1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// takes precedence over the `topologySpreadConstraints`
	RoleTopologySpreadConstraints *RoleTopologySpreadConstraints `json:"roleTopologySpreadConstraints,omitempty"`
	// advanced scheduling constraints
	Affinity *v1.Affinity `json:"affinity,omitempty"`
	// affinity per container role
	// takes precedence over the `affinity` field
	RoleAffinity *RoleAffinity `json:"roleAffinity,omitempty"`
}

type StartIoConditions struct {
	// minumum number of drives that should be added to the cluster before starting IO
	MinNumDrives int `json:"minNumDrives,omitempty"`
}

type FailureDomain struct {
	// label used for spreading the weka containers across different failure domains (if set)
	// nodes that have the same value for the label will be considered as a single failure domain
	Label *string `json:"label,omitempty"`
	// skew for the failure domain, if set, the weka containers will be spread with the skew in mind
	// (only applicable if `label` is set)
	Skew *int `json:"skew,omitempty"`
	// If multiple labels are specified, the failure domain will be the combination of the labels.
	// If `compositeLabels` is set, `label` and `skew` will be ignored.
	// When using compositeLabels, weka containers will be spread considering all labels
	// with best effort, but even distribution is not guaranteed
	CompositeLabels []string `json:"compositeLabels,omitempty"`
}

type CsiConfig struct {
	EndpointsSubnets []string           `json:"endpointsSubnets,omitempty"`
	CsiGroup         string             `json:"csiGroup,omitempty"`
	Advanced         *AdvancedCsiConfig `json:"advanced,omitempty"`
}

type VaultConfig struct {
	// Vault address, e.g. "https://vault.example.com:8200".
	Address string `json:"address"`

	// Role to authenticate as in Vault.
	Role string `json:"role"`

	// +kubebuilder:default=kubernetes
	// Path under auth/ that the weka uses for login. defaults to "kubernetes"
	AuthPath string `json:"authPath,omitempty"`

	// +kubebuilder:default=transit
	// Transit engine mount path, defaults "transit".
	TransitPath string `json:"transitPath,omitempty"`

	// +kubebuilder:validation:Enum=kubernetes
	// +kubebuilder:default=kubernetes
	// Vault Auth method (only “kubernetes” is supported  on operator side.)
	Method string `json:"method,omitempty"`

	// +kubebuilder:default=weka-key
	// Name of the transit key. defaults to "weka-key"
	KeyName string `json:"keyName,omitempty"`
}

type EncryptionConfig struct {
	VaultConfig *VaultConfig `json:"vault,omitempty"`
}

// WekaClusterSpec defines the desired state of WekaCluster
type WekaClusterSpec struct {
	// A template/strategy of how to build a cluster, right now only "dynamic" supported, explicitly specifying config of a cluster
	// +kubebuilder:default=dynamic
	Template string `json:"template,omitempty"`
	// full container image name in format of quay.io/weka.io/weka-in-container:VERSION
	// +kubebuilder:validation:Pattern=`^.+:\d+\.\d+\.\d+.*$`
	Image string `json:"image"`
	// image pull secret to use for pulling the image
	ImagePullSecret string `json:"imagePullSecret,omitempty"`
	// endpoint for distribution service, global https://drivers.weka.io or in-k8s-cluster "https://weka-drivers-dist.namespace.svc.cluster.local:60001"
	DriversDistService string `json:"driversDistService,omitempty"`
	// node selector for the weka containers
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// node selector for the weka containers per role, overrides global nodeSelector
	RoleNodeSelector RoleNodeSelector `json:"roleNodeSelector,omitempty"`
	// annotations for the weka containers per role
	RoleAnnotations RoleAnnotations `json:"roleAnnotations,omitempty"`
	// network selector for the weka containers per role, overrides global network
	RoleNetworkSelector RoleNetworkSelector `json:"roleNetworkSelector,omitempty"`
	// failure domain configuration for weka containers
	FailureDomain *FailureDomain `json:"failureDomain,omitempty"`
	// advanced pod affinities configuration
	PodConfig *PodConfiguration `json:"podConfig,omitempty"`
	// cpu policy to use for scheduling cores for weka, unless instructed by weka team, keep default of auto
	// manual and shared are same, with shared being deprecated
	// when manual is used - no exclusive cores will be allocaated on k8s/cgroup level, assuming good alignment of cores usage across different applications, like weka and slurm
	// there is no need to specify siblings in this list, but on the side of other applications like slurm, both weka core and its siblings should be excluded from used cpu set
	//+kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy CpuPolicy `json:"cpuPolicy,omitempty"`
	// traces capacities configuration for weka containers
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	// simplified tolerations, checked only by key existence, expanding to NoExecute|NoSchedule tolerations
	Tolerations []string `json:"tolerations,omitempty"`
	// tolerations in standard k8s format
	RawTolerations []v1.Toleration `json:"rawTolerations,omitempty"`
	// weka home configuration
	WekaHome *WekaHomeConfig `json:"wekaHome,omitempty"`
	// use ipv6 for weka cluster networking configuration
	Ipv6 bool `json:"ipv6,omitempty"`
	// additional memory to allocate for weka containers
	AdditionalMemory AdditionalMemory `json:"additionalMemory,omitempty"`
	// port allocation for weka containers, if not set, free range will be auto selected. Currently allocated ports can be seen in wekacluster.status.ports
	Ports ClusterPorts `json:"ports,omitempty"`
	// reference to the secret containing the weka system credentials used by operator, used in flow of migration
	OperatorSecretRef string `json:"operatorSecretRef,omitempty"`
	// endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration
	ExpandEndpoints []string `json:"expandEndpoints,omitempty"`
	// weka cluster topology configuration
	Dynamic *WekaConfig `json:"dynamicTemplate,omitempty"`
	// weka cluster network configuration
	Network Network `json:"network,omitempty"`
	// A hot spare is reserved capacity designed to handle data rebuilds while maintaining the system's net capacity, even in the event of failure domains being lost
	// See: https://docs.weka.io/weka-system-overview/ssd-capacity-management#hot-spare
	// +kubebuilder:default=0
	HotSpare int `json:"hotSpare,omitempty"`
	// storage capacity dedicated to system protection (2/4). https://docs.weka.io/weka-system-overview/ssd-capacity-management#protection-level
	RedundancyLevel int `json:"redundancyLevel,omitempty"`
	// stripe width is the number of blocks within a common protection set, ranging from 3 to 16 https://docs.weka.io/weka-system-overview/ssd-capacity-management#stripe-width
	StripeWidth int `json:"stripeWidth,omitempty"`
	// size of raft for leadership, defaults to 5, 5/9 are supported
	LeadershipSize *int `json:"leadershipRaftSize,omitempty"`
	// size of raft for buckets, defaults to 5, 5/9 are supported
	BucketRaftSize *int `json:"bucketRaftSize,omitempty"`
	// conditions that must be met before starting IO
	StartIoConditions *StartIoConditions `json:"startIoConditions,omitempty"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +kubebuilder:default="24h"
	// During this period the cluster will not be destroyed (protection from accidental deletion)
	// Note: due to discrepancies in validation vs parsing, we use a Pattern instead of `Format=duration`. See
	// https://bugzilla.redhat.com/show_bug.cgi?id=2050332
	// https://github.com/kubernetes/apimachinery/issues/131
	// https://github.com/kubernetes/apiextensions-apiserver/issues/56
	GracefulDestroyDuration metav1.Duration           `json:"gracefulDestroyDuration,omitempty"`
	Overrides               *WekaClusterSpecOverrides `json:"overrides,omitempty"`
	CsiConfig               CsiConfig                 `json:"csiConfig,omitempty"`
	GlobalPVC               *PVCConfig                `json:"globalPVC,omitempty"`
	ServiceAccountName      string                    `json:"serviceAccountName,omitempty"`
	// RoleCoreIds defines a list of CPU core IDs (as seen by the host) that should
	// be assigned to containers of the specific role when CpuPolicy is set to
	// "manual". If the slice for the given role is empty, core ids will not be
	// set for that role, and the manual policy will fail validation on pod start.
	//
	// NOTE: The semantics are the same as for NodeSelector/Annotations structures –
	// a single list per role which will be copied to every container of that role.
	// Users are responsible to provide a set that makes sense for their topology.
	// +kubebuilder:validation:Type=object
	// Example:
	//   roleCoreIds:
	//     compute: [0,1,2,3]
	//     drive:   [4,5,6,7]
	//
	// will result in every compute container getting coreIds [0,1,2,3] and every
	// drive container getting [4,5,6,7].
	RoleCoreIds RoleCoreIds       `json:"roleCoreIds,omitempty"`
	Encryption  *EncryptionConfig `json:"encryption,omitempty"`
}

func (c *WekaClusterSpec) GetOverrides() *WekaClusterSpecOverrides {
	if c.Overrides == nil {
		return &WekaClusterSpecOverrides{}
	} else {
		return c.Overrides
	}
}

func (c *WekaClusterSpec) GetStartIoConditions() *StartIoConditions {
	if c.StartIoConditions == nil {
		return &StartIoConditions{}
	} else {
		return c.StartIoConditions
	}
}

type PVCConfig struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type WekaClusterSpecOverrides struct {
	AllowS3ClusterDestroy bool `json:"allowS3ClusterDestroy,omitempty"`
	// disregard redundancy constraints, useful for testing, should not be used in production as misaligns failure domains
	DisregardRedundancy bool `json:"disregardRedundancy,omitempty"`
	// image to be used for loading drivers, do not use unless explicitly instructed by Weka team
	DriversLoaderImage string `json:"driversLoaderImage,omitempty"`
	// force weka to use drives in aio mode and not direct nvme (impacts performance, but might serve as a fallback in case of incompatible device)
	ForceAio bool `json:"forceAio,omitempty"`
	// script to run post cluster create (i.e before starting io)
	PostFormClusterScript string `json:"postFormClusterScript,omitempty"`
	// unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel
	UpgradeForceReplace bool `json:"upgradeForceReplace,omitempty"`
	// unsafe operation, skips graceful stop of drive weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel
	UpgradeForceReplaceDrives bool `json:"upgradeForceReplaceDrives,omitempty"`
	// unsafe operation, should not be used unless instructed explicitly by weka personnel
	UpgradeAllAtOnce bool `json:"upgradeAllAtOnce,omitempty"`
	// Pause upgrade
	UpgradePaused bool `json:"upgradePaused,omitempty"`
	// Prevent from moving into compute phase
	UpgradePausePreCompute bool `json:"upgradePausePreCompute,omitempty"`
	// Timeout duration for deactivating pods that are terminating longer than this duration.
	// When nil (default), the default timeout of 5 minutes is used.
	// When set to 0, deactivation of terminating pods is disabled.
	// Otherwise, the specified duration is used.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +optional
	PodTerminationDeactivationTimeout *metav1.Duration `json:"podTerminationDeactivationTimeout,omitempty"`
}

func (c *WekaClusterSpec) GetAdditionalMemory(mode string) int {
	return c.AdditionalMemory.GetForMode(mode)
}

type ClusterPorts struct {
	// We should not be updating Spec, as it's a user interface and we should not break ability to update spec file
	// Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here
	BasePort    int `json:"basePort,omitempty"`
	PortRange   int `json:"portRange,omitempty"`
	LbPort      int `json:"lbPort,omitempty"`
	LbAdminPort int `json:"lbAdminPort,omitempty"`
	S3Port      int `json:"s3Port,omitempty"`
}

// WekaClusterStatus defines the observed state of WekaCluster
type WekaClusterStatus struct {
	Status           WekaClusterStatusEnum  `json:"status"`
	Conditions       []metav1.Condition     `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	ClusterID        string                 `json:"clusterID,omitempty"`
	TraceId          string                 `json:"traceId,omitempty"`
	SpanID           string                 `json:"spanId,omitempty"`
	LastAppliedImage string                 `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	LastAppliedSpec  string                 `json:"lastAppliedSpec,omitempty"`
	Ports            ClusterPorts           `json:"ports,omitempty"`
	Stats            *ClusterMetrics        `json:"stats,omitempty"`
	PrinterColumns   ClusterPrinterColumns  `json:"printer,omitempty"`
	Timestamps       map[string]metav1.Time `json:"timestamps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status of the cluster",priority=0
// +kubebuilder:printcolumn:name="Cluster ID",type="string",JSONPath=".status.clusterID",description="Weka cluster GUID",priority=0
// +kubebuilder:printcolumn:name="CCT(A/C/D)",type="string",JSONPath=".status.printer.computeContainers",description="Number of compute containers: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="DCT(A/C/D)",type="string",JSONPath=".status.printer.driveContainers",description="Number of drive containers: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="DRVS(A/C/D)",type="string",JSONPath=".status.printer.drives",description="Number of Drives: Active/Created/Desired",priority=0
// +kubebuilder:printcolumn:name="IOPS(R/W/M)",type="string",JSONPath=".status.printer.iops",description="IOPS Read/Write/Metadata",priority=1
// +kubebuilder:printcolumn:name="THRPT(R/W)",type="string",JSONPath=".status.printer.throughput",description="Throughput Read/Write",priority=1
// +kubebuilder:printcolumn:name="FS(Capacity)",type="string",JSONPath=".status.printer.filesystemCapacity",description="Filesystem Capacity",priority=1

type WekaCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaClusterSpec   `json:"spec,omitempty"`
	Status WekaClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WekaClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaCluster `json:"items"`
}

func (c *WekaCluster) GetOperatorSecretName() string {
	if c.Spec.OperatorSecretRef != "" {
		return c.Spec.OperatorSecretRef
	}
	return string("weka-operator-" + c.GetUID())
}

func (c *WekaCluster) GetLastGuidPart() string {
	return util.GetLastGuidPart(c.GetUID())
}

func (c *WekaCluster) GetUserClusterUsername() string {
	return "weka" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetClusterClientUsername() string {
	return "wekaclient" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetOperatorClusterUsername() string {
	return "weka-operator-" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetInitialOperatorUsername() string {
	return "admin"
}

func (c *WekaCluster) GetUserSecretName() string {
	name := c.Name
	return "weka-cluster-" + name
}

func (c *WekaCluster) GetClientSecretName() string {
	name := c.Name
	return "weka-client-" + name
}

func GetClientSecretName(clusterName string) string {
	return "weka-client-" + clusterName
}

func GetCsiSecretName(clusterName string) string {
	return "weka-csi-" + clusterName
}

func (c *WekaCluster) GetCsiSecretName() string {
	return GetCsiSecretName(c.Name)
}

func (status *WekaClusterStatus) InitStatus() {
	status.Conditions = []metav1.Condition{}
	status.Status = WekaClusterStatusInit
}

func (w *WekaCluster) ToOwnerObject() *WekaOwnerDetails {
	return &WekaOwnerDetails{
		Image:           w.Spec.Image,
		ImagePullSecret: w.Spec.ImagePullSecret,
		Tolerations:     util.ExpandTolerations([]v1.Toleration{}, w.Spec.Tolerations, w.Spec.RawTolerations),
		Labels:          w.ObjectMeta.GetLabels(),
	}
}

func (c *WekaCluster) GetClusterCsiUsername() string {
	return "wekacsi" + c.GetLastGuidPart()
}

func (c *WekaCluster) IsMarkedForDeletion() bool {
	return !c.GetDeletionTimestamp().IsZero()
}

func (c *WekaCluster) IsTerminating() bool {
	return c.Status.Status == WekaClusterStatusDeallocating || c.Status.Status == WekaClusterStatusDestroying
}

func (c *WekaCluster) IsReady() bool {
	return c.Status.Status == WekaClusterStatusReady
}

func (c *WekaCluster) IsExpand() bool {
	return len(c.Spec.ExpandEndpoints) != 0
}

func (c *WekaCluster) GetGracefulDestroyDuration() time.Duration {
	return c.Spec.GracefulDestroyDuration.Duration
}

// Use role-specific node selector if set, otherwise use cluster node selector
func (c *WekaCluster) GetNodeSelectorForRole(role string) map[string]string {
	var roleNodeSelector *map[string]string

	switch role {
	case "compute":
		roleNodeSelector = c.Spec.RoleNodeSelector.Compute
	case "drive":
		roleNodeSelector = c.Spec.RoleNodeSelector.Drive
	case "s3":
		roleNodeSelector = c.Spec.RoleNodeSelector.S3
	case "nfs":
		roleNodeSelector = c.Spec.RoleNodeSelector.Nfs
	}

	if roleNodeSelector != nil {
		return *roleNodeSelector
	} else {
		return c.Spec.NodeSelector
	}
}

// Use role-specific annotations if set, otherwise use cluster annotations
func (c *WekaCluster) GetAnnotationsForRole(role string) map[string]string {
	var roleAnnotations *map[string]string

	switch role {
	case "compute":
		roleAnnotations = c.Spec.RoleAnnotations.Compute
	case "drive":
		roleAnnotations = c.Spec.RoleAnnotations.Drive
	case "s3":
		roleAnnotations = c.Spec.RoleAnnotations.S3
	case "nfs":
		roleAnnotations = c.Spec.RoleAnnotations.Nfs
	}

	if roleAnnotations != nil {
		return *roleAnnotations
	} else {
		return c.ObjectMeta.GetAnnotations()
	}
}

// Use role-specific network selector if set, otherwise use cluster network selector
func (c *WekaCluster) GetNetworkForRole(role string) Network {
	var roleNetworkSelector *Network

	switch role {
	case "compute":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Compute
	case "drive":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Drive
	case "s3":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.S3
	case "nfs":
		roleNetworkSelector = c.Spec.RoleNetworkSelector.Nfs
	}

	if roleNetworkSelector != nil {
		return *roleNetworkSelector
	} else {
		return c.Spec.Network
	}
}

// Return the CPU core IDs for the specified role.
func (c *WekaCluster) GetCoreIdsForRole(role string) []int {
	switch role {
	case "compute":
		return c.Spec.RoleCoreIds.Compute
	case "drive":
		return c.Spec.RoleCoreIds.Drive
	case "s3":
		return c.Spec.RoleCoreIds.S3
	case "nfs":
		return c.Spec.RoleCoreIds.Nfs
	default:
		return nil
	}
}

// Use role-specific affinity if set, otherwise use cluster affinity from PodConfig.
func (c *WekaCluster) GetAffinityForRole(role string) *v1.Affinity {
	if c.Spec.PodConfig == nil {
		return nil
	}

	if c.Spec.PodConfig.RoleAffinity == nil {
		return c.Spec.PodConfig.Affinity
	}

	var affinity *v1.Affinity

	switch role {
	case "compute":
		affinity = c.Spec.PodConfig.RoleAffinity.Compute
	case "drive":
		affinity = c.Spec.PodConfig.RoleAffinity.Drive
	case "s3":
		affinity = c.Spec.PodConfig.RoleAffinity.S3
	case "nfs":
		affinity = c.Spec.PodConfig.RoleAffinity.Nfs
	}

	if affinity != nil {
		return affinity
	} else {
		return c.Spec.PodConfig.Affinity
	}
}

// Use role-specific topology spread constraints if set, otherwise use cluster topology spread constraints from PodConfig.
func (c *WekaCluster) GetTopologySpreadConstraintsForRole(role string) []v1.TopologySpreadConstraint {
	if c.Spec.PodConfig == nil {
		return nil
	}

	if c.Spec.PodConfig.RoleTopologySpreadConstraints == nil {
		return c.Spec.PodConfig.TopologySpreadConstraints
	}

	topologySpreadConstraints := c.Spec.PodConfig.RoleTopologySpreadConstraints.ForRole(role)

	if topologySpreadConstraints != nil {
		return topologySpreadConstraints
	} else {
		return c.Spec.PodConfig.TopologySpreadConstraints
	}
}

func init() {
	SchemeBuilder.Register(&WekaCluster{}, &WekaClusterList{})
}
