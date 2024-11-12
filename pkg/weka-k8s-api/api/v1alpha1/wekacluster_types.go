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
	"time"

	"github.com/weka/weka-k8s-api/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WekaClusterStatusEnum string

const (
	WekaClusterStatusInit        WekaClusterStatusEnum = "Init"
	WekaClusterStatusReady       WekaClusterStatusEnum = "Ready"
	WekaClusterStatusGracePeriod WekaClusterStatusEnum = "GracePeriod"
)

type NetworkSelector struct {
	EthSlots  []string `json:"ethSlots,omitempty"`
	EthDevice string   `json:"ethDevice,omitempty"`
	UdpMode   bool     `json:"udpMode,omitempty"`
}

type AdditionalMemory struct {
	Compute    int `json:"compute,omitempty"`
	Drive      int `json:"drive,omitempty"`
	S3         int `json:"s3,omitempty"`
	NfsGateway int `json:"nfsGateway,omitempty"`
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
	case WekaContainerModeNfsGateway:
		additionalMemory = a.NfsGateway
	}
	return additionalMemory
}

type WekaConfig struct {
	ComputeContainers                 *int `json:"computeContainers,omitempty"`
	DriveContainers                   *int `json:"driveContainers,omitempty"`
	S3Containers                      int  `json:"s3Containers,omitempty"`
	ComputeCores                      int  `json:"computeCores,omitempty"`
	DriveCores                        int  `json:"driveCores,omitempty"`
	S3Cores                           int  `json:"s3Cores,omitempty"`
	NumDrives                         int  `json:"numDrives,omitempty"`
	S3ExtraCores                      int  `json:"s3ExtraCores,omitempty"`
	DriveHugepages                    int  `json:"driveHugepages,omitempty"`
	DriveHugepagesOffset              int  `json:"driveHugepagesOffset,omitempty"`
	ComputeHugepages                  int  `json:"computeHugepages,omitempty"`
	ComputeHugepagesOffset            int  `json:"computeHugepagesOffset,omitempty"`
	S3FrontendHugepages               int  `json:"s3FrontendHugepages,omitempty"`
	S3FrontendHugepagesOffset         int  `json:"s3FrontendHugepagesOffset,omitempty"`
	EnvoyCores                        int  `json:"envoyCores,omitempty"`
	NfsGatewayContainers              int  `json:"nfsGatewayContainers,omitempty"`
	NfsGatewayCores                   int  `json:"nfsGatewayCores,omitempty"`
	NfsGatewayExtraCores              int  `json:"nfsGatewayExtraCores,omitempty"`
	NfsGatewayFrontendHugepages       int  `json:"nfsGatewayFrontendHugepages,omitempty"`
	NfsGatewayFrontendHugepagesOffset int  `json:"nfsGatewayFrontendHugepagesOffset,omitempty"`
}

type WekaHomeConfig struct {
	Endpoint      string `json:"endpoint,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	CacertSecret  string `json:"cacertSecret,omitempty"`
	EnableStats   *bool  `json:"enableStats,omitempty"`
}

type RoleNodeSelector struct {
	Compute    map[string]string `json:"compute,omitempty"`
	Drive      map[string]string `json:"drive,omitempty"`
	S3         map[string]string `json:"s3,omitempty"`
	NfsGateway map[string]string `json:"nfsGateway,omitempty"`
}

func (s RoleNodeSelector) ForRole(role string) map[string]string {
	switch role {
	case "compute":
		return s.Compute
	case "drive":
		return s.Drive
	case "s3":
		return s.S3
	case "nfs-gateway":
		return s.NfsGateway
	default:
		return nil
	}
}

type RoleTopologySpreadConstraints struct {
	Compute    []v1.TopologySpreadConstraint `json:"compute,omitempty"`
	Drive      []v1.TopologySpreadConstraint `json:"drive,omitempty"`
	S3         []v1.TopologySpreadConstraint `json:"s3,omitempty"`
	NfsGateway []v1.TopologySpreadConstraint `json:"nfsGateway,omitempty"`
}

func (c *RoleTopologySpreadConstraints) ForRole(role string) []v1.TopologySpreadConstraint {
	switch role {
	case "compute":
		return c.Compute
	case "drive":
		return c.Drive
	case "s3":
		return c.S3
	case "nfs-gateway":
		return c.NfsGateway
	default:
		return nil
	}
}

type RoleAffinity struct {
	Compute    *v1.Affinity `json:"compute,omitempty"`
	Drive      *v1.Affinity `json:"drive,omitempty"`
	S3         *v1.Affinity `json:"s3,omitempty"`
	NfsGateway *v1.Affinity `json:"nfsGateway,omitempty"`
}

func (a *RoleAffinity) ForRole(role string) *v1.Affinity {
	switch role {
	case "compute":
		return a.Compute
	case "drive":
		return a.Drive
	case "s3":
		return a.S3
	case "nfs-gateway":
		return a.NfsGateway
	default:
		return nil
	}
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

// WekaClusterSpec defines the desired state of WekaCluster
type WekaClusterSpec struct {
	Template           string            `json:"template"`
	Image              string            `json:"image"`
	ImagePullSecret    string            `json:"imagePullSecret,omitempty"`
	DriversDistService string            `json:"driversDistService,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	RoleNodeSelector   RoleNodeSelector  `json:"roleNodeSelector,omitempty"`
	// label used for spreading the weka containers across different failure domains (if set)
	// nodes that have the same value for this label will be considered as a single failure domain
	FailureDomainLabel *string           `json:"failureDomainLabel,omitempty"`
	PodConfig          *PodConfiguration `json:"podConfig,omitempty"`
	//+kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy           CpuPolicy            `json:"cpuPolicy,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations         []string             `json:"tolerations,omitempty"`
	RawTolerations      []v1.Toleration      `json:"rawTolerations,omitempty"`
	WekaHome            *WekaHomeConfig      `json:"wekaHome,omitempty"`
	Ipv6                bool                 `json:"ipv6,omitempty"`
	AdditionalMemory    AdditionalMemory     `json:"additionalMemory,omitempty"`
	Ports               ClusterPorts         `json:"ports,omitempty"`
	DisregardRedundancy bool                 `json:"disregardRedundancy,omitempty"`
	OperatorSecretRef   string               `json:"operatorSecretRef,omitempty"`
	ExpandEndpoints     []string             `json:"expandEndpoints,omitempty"`
	Dynamic             *WekaConfig          `json:"dynamicTemplate,omitempty"`
	NetworkSelector     NetworkSelector      `json:"network,omitempty"`
	ForceAio            bool                 `json:"forceAio,omitempty"`
	// A hot spare is reserved capacity designed to handle data rebuilds while maintaining the system's net capacity, even in the event of failure domains being lost
	// See: https://docs.weka.io/weka-system-overview/ssd-capacity-management#hot-spare
	// +kubebuilder:default=0
	HotSpare        int `json:"hotSpare,omitempty"`
	RedundancyLevel int `json:"redundancyLevel,omitempty"`
	StripeWidth     int `json:"stripeWidth,omitempty"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// +kubebuilder:default="24h"
	// During this period the cluster will not be destroyed (protection from accidental deletion)
	// Note: due to discrepancies in validation vs parsing, we use a Pattern instead of `Format=duration`. See
	// https://bugzilla.redhat.com/show_bug.cgi?id=2050332
	// https://github.com/kubernetes/apimachinery/issues/131
	// https://github.com/kubernetes/apiextensions-apiserver/issues/56
	GracefulDestroyDuration metav1.Duration `json:"gracefulDestroyDuration,omitempty"`
}

func (c *WekaClusterSpec) GetAdditionalMemory(mode string) int {
	return c.AdditionalMemory.GetForMode(mode)
}

type ClusterPorts struct {
	// We should not be updating Spec, as it's a user interface and we should not break ability to update spec file
	// Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here
	BasePort       int `json:"basePort,omitempty"`
	PortRange      int `json:"portRange,omitempty"`
	LbPort         int `json:"lbPort,omitempty"`
	LbAdminPort    int `json:"lbAdminPort,omitempty"`
	S3Port         int `json:"s3Port,omitempty"`
	NfsGatewayPort int `json:"nfsGatewayPort,omitempty"`
}

type Counter struct {
	NumDrives          int64 `json:"numDrives,omitempty"`
	NumComputeCores    int64 `json:"numComputeCores,omitempty"`
	NumDriveCores      int64 `json:"numDriveCores,omitempty"`
	NumS3Cores         int64 `json:"numS3Cores,omitempty"`
	NumNfsGatewayCores int64 `json:"numNfsGatewayCores,omitempty"`
	NumEnvoyContainers int64 `json:"numEnvoyContainers,omitempty"`
	NumEnvoyCores      int64 `json:"numEnvoyCores,omitempty"`
}

type Counters struct {
	Desired Counter `json:"desired,omitempty"`
	Active  Counter `json:"current,omitempty"`
}

// WekaClusterStatus defines the observed state of WekaCluster
type WekaClusterStatus struct {
	Status           WekaClusterStatusEnum `json:"status"`
	Conditions       []metav1.Condition    `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	Throughput       string                `json:"throughput"`
	ClusterID        string                `json:"clusterID,omitempty"`
	TraceId          string                `json:"traceId,omitempty"`
	SpanID           string                `json:"spanId,omitempty"`
	LastAppliedImage string                `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	LastAppliedSpec  string                `json:"lastAppliedSpec,omitempty"`
	Ports            ClusterPorts          `json:"ports,omitempty"`
	Counters         Counters              `json:"counters,omitempty"`

	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$"
	// has priority over the spec `gracefulDestroyDuration` value (if set)
	OverrideGracefulDestroyDuration *metav1.Duration `json:"overrideGracefulDestroyDuration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status of the cluster",priority=0
// +kubebuilder:printcolumn:name="Cluster ID",type="string",JSONPath=".status.clusterID",description="Weka cluster ID",priority=0
// +kubebuilder:printcolumn:name="Drives",type="integer",JSONPath=".status.counters.current.numDrives",description="Number of drives",priority=0
// +kubebuilder:printcolumn:name="Compute Containers",type="integer",JSONPath=".spec.dynamicTemplate.computeContainers",description="Number of compute containers",priority=0
// +kubebuilder:printcolumn:name="Drive Containers",type="integer",JSONPath=".spec.dynamicTemplate.driveContainers",description="Number of drive containers",priority=0
// +kubebuilder:printcolumn:name="S3 Containers",type="integer",JSONPath=".spec.dynamicTemplate.s3Containers",description="Number of S3 containers",priority=1
// +kubebuilder:printcolumn:name="NFS Containers",type="integer",JSONPath=".spec.dynamicTemplate.nfsGatewayContainers",description="Number of NFS gateway containers",priority=1
// +kubebuilder:printcolumn:name="Envoy Containers",type="integer",JSONPath=".spec.dynamicTemplate.envoyContainers",description="Number of NFS gateway containers",priority=1
// +kubebuilder:printcolumn:name="Compute Cores",type="integer",JSONPath=".status.counters.current.numComputeCores",description="Number of compute cores",priority=1
// +kubebuilder:printcolumn:name="Drive Cores",type="integer",JSONPath=".status.counters.current.numDriveCores",description="Number of drive cores",priority=1
// +kubebuilder:printcolumn:name="S3 Cores",type="integer",JSONPath=".status.counters.current.nums3Cores",description="Number of S3 cores",priority=1
// +kubebuilder:printcolumn:name="NFS Gateway Cores",type="integer",JSONPath=".status.counters.current.numNfsGatewayCores",description="Number of NFS Gateway cores",priority=1
// +kubebuilder:printcolumn:name="Envoy Cores",type="integer",JSONPath=".status.counters.current.numEnvoyCores",description="Number of Envoy cores",priority=1

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

func (c *WekaCluster) GetCSISecretName() string {
	return "weka-csi-" + c.Name
}

func (status *WekaClusterStatus) InitStatus() {
	status.Conditions = []metav1.Condition{}
	status.Status = WekaClusterStatusInit
}

func (w *WekaCluster) ToOwnerObject() *WekaContainerDetails {
	return &WekaContainerDetails{
		Image:           w.Spec.Image,
		ImagePullSecret: w.Spec.ImagePullSecret,
		Tolerations:     util.ExpandTolerations([]v1.Toleration{}, w.Spec.Tolerations, w.Spec.RawTolerations),
		Labels:          w.ObjectMeta.GetLabels(),
	}
}

func (c *WekaCluster) GetClusterCSIUsername() string {
	return "wekacsi" + c.GetLastGuidPart()
}

func (c *WekaCluster) IsMarkedForDeletion() bool {
	return !c.GetDeletionTimestamp().IsZero()
}

func (c *WekaCluster) IsExpand() bool {
	return len(c.Spec.ExpandEndpoints) != 0
}

func (c *WekaCluster) GetGracefulDestroyDuration() time.Duration {
	gracefulDestroyDuration := c.Spec.GracefulDestroyDuration.Duration

	if c.Status.OverrideGracefulDestroyDuration != nil {
		overrideDuration := c.Status.OverrideGracefulDestroyDuration.Duration
		return overrideDuration
	}
	return gracefulDestroyDuration
}

func init() {
	SchemeBuilder.Register(&WekaCluster{}, &WekaClusterList{})
}
