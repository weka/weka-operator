package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaPolicyType enumerates the supported WekaPolicy types.
type WekaPolicyType string

const (
	WekaPolicyTypeSignDrives                     WekaPolicyType = opSignDrives
	WekaPolicyTypeDiscoverDrives                 WekaPolicyType = opDiscoverDrives
	WekaPolicyTypeEnsureNICs                     WekaPolicyType = opEnsureNICs
	WekaPolicyTypeEnableLocalDriversDistribution WekaPolicyType = opEnableLocalDriversDist
	WekaPolicyTypeRemoteTracesSession            WekaPolicyType = opRemoteTracesSession
	WekaPolicyTypeCleanStaleVirtualDrives        WekaPolicyType = opCleanStaleVirtualDrives
)

// WekaPolicySpec defines the desired state of WekaPolicy
type WekaPolicySpec struct {
	// +kubebuilder:validation:Enum=sign-drives;discover-drives;ensure-nics;enable-local-drivers-distribution;remote-traces-session;clean-stale-virtual-drives
	Type               WekaPolicyType  `json:"type"`
	Payload            PolicyPayload   `json:"payload"`
	Image              *string         `json:"image,omitempty"`
	ImagePullSecret    *string         `json:"imagePullSecret,omitempty"`
	Tolerations        []v1.Toleration `json:"tolerations,omitempty"`
	ServiceAccountName string          `json:"serviceAccountName,omitempty"`
}

// WekaPolicyStatus defines the observed state of WekaPolicy
type WekaPolicyStatus struct {
	Status      string             `json:"status"`
	LastResult  string             `json:"result"`
	LastRunTime metav1.Time        `json:"lastRunTime"`
	Progress    string             `json:"progress,omitempty"`
	TypedStatus *TypedPolicyStatus `json:"typedStatus,omitempty"`
}

// TypedPolicyStatus holds status fields specific to a policy type
type TypedPolicyStatus struct {
	DistService *DistServiceStatus `json:"distService,omitempty"`
	// Add other policy-specific statuses here in the future
}

// DistServiceStatus holds the status for the enable-local-drivers-distribution policy
type DistServiceStatus struct {
	ServiceUrl string `json:"serviceUrl,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type",description="Type",priority=0
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status",priority=0
// +kubebuilder:printcolumn:name="Progress",type="string",JSONPath=".status.progress",description="Policy-specific progress",priority=0
// +kubebuilder:printcolumn:name="Result",type="string",JSONPath=".status.result",description="Last result",priority=1
// +kubebuilder:printcolumn:name="Interval",type="string",JSONPath=".spec.payload.interval",description="Interval",priority=1
// +kubebuilder:printcolumn:name="Last Run Time",type="date",JSONPath=".status.lastRunTime",description="Last run time",priority=1
// WekaPolicy is the Schema for the wekapolicies API
type WekaPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaPolicySpec   `json:"spec,omitempty"`
	Status WekaPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WekaPolicyList contains a list of WekaPolicy
type WekaPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaPolicy `json:"items"`
}

type PolicyPayload struct {
	SignDrives              *SignDrivesPayload              `json:"signDrivesPayload,omitempty"`
	SchedulingConfig        *SchedulingConfigPayload        `json:"schedulingConfigPayload,omitempty"`
	DiscoverDrives          *DiscoverDrivesPayload          `json:"discoverDrivesPayload,omitempty"`
	EnsureNICs              *EnsureNICsPayload              `json:"ensureNICsPayload,omitempty"`
	DriverDistPayload       *DriverDistPayload              `json:"driverDistPayload,omitempty"`
	RemoteTracesSession     *RemoteTracesSessionConfig      `json:"remoteTracesSessionPayload,omitempty"`
	CleanStaleVirtualDrives *CleanStaleVirtualDrivesPayload `json:"cleanStaleVirtualDrivesPayload,omitempty"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(s|m|h))+)$"
	// +kubebuilder:default="5m"
	Interval        metav1.Duration `json:"interval,omitempty"`
	WaitForPolicies []string        `json:"waitForPolicies,omitempty"`
}

type SchedulingConfigPayload struct {
}

// DriverDistPayload defines the parameters for the enable-local-drivers-distribution policy
type DriverDistPayload struct {
	// EnsureImages is a list of Weka images for which drivers should be proactively built.
	EnsureImages []string `json:"ensureImages,omitempty"`
	// NodeSelectors is a list of node selectors. Nodes matching any of these selectors will be considered for driver building.
	// If empty, all nodes in the cluster are considered.
	NodeSelectors []map[string]string `json:"nodeSelectors,omitempty"`
	// KernelLabelKey is the custom label key to use for storing the node's kernel version.
	// If not specified, "weka.io/kernel" will be used.
	KernelLabelKey *string `json:"kernelLabelKey,omitempty"`
	// ArchitectureLabelKey is the custom label key to use for storing the node's architecture.
	// If not specified, "weka.io/architecture" will be used.
	ArchitectureLabelKey *string `json:"architectureLabelKey,omitempty"`
	// OsLabelKey is the custom label key to use for storing the node's os.
	// If not specified, "weka.io/os" will be used.
	OsLabelKey *string `json:"osLabelKey,omitempty"`
	// BuilderImageOverride is an optional image that you can specify for the builder
	BuilderImageOverride string `json:"builderImageOverride,omitempty"`
	// BuilderPreRunScript is an optional script to run on builder containers after kernel validation.
	BuilderPreRunScript *string `json:"builderPreRunScript,omitempty"`
	// DistNodeSelector is the node selector for the drivers distribution (dist) container.
	// If not specified, the dist container will be scheduled on any available node.
	DistNodeSelector map[string]string `json:"distNodeSelector,omitempty"`
	// DistResources overrides the pod resources of the drivers distribution (dist) container.
	// Fields left empty keep the built-in sizing.
	DistResources *PodResourcesSpec `json:"distResources,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WekaPolicy{}, &WekaPolicyList{})
}
