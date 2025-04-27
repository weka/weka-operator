package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaPolicySpec defines the desired state of WekaPolicy
type WekaPolicySpec struct {
	Type            string          `json:"type"`
	Payload         PolicyPayload   `json:"payload"`
	Image           *string         `json:"image,omitempty"`
	ImagePullSecret *string         `json:"imagePullSecret,omitempty"`
	Tolerations     []v1.Toleration `json:"tolerations,omitempty"`
}

// WekaPolicyStatus defines the observed state of WekaPolicy
type WekaPolicyStatus struct {
	Status      string      `json:"status"`
	LastResult  string      `json:"result"`
	LastRunTime metav1.Time `json:"lastRunTime"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type",description="Type",priority=0
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status",priority=0
// +kubebuilder:printcolumn:name="Result",type="string",JSONPath=".status.lastResult",description="Last result",priority=1
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
	SignDrives       *SignDrivesPayload       `json:"signDrivesPayload,omitempty"`
	SchedulingConfig *SchedulingConfigPayload `json:"schedulingConfigPayload,omitempty"`
	DiscoverDrives   *DiscoverDrivesPayload   `json:"discoverDrivesPayload,omitempty"`
	EnsureNICs       *EnsureNICsPayload       `json:"ensureNICsPayload,omitempty"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(s|m|h))+)$"
	// +kubebuilder:default="5m"
	Interval        metav1.Duration `json:"interval,omitempty"`
	WaitForPolicies []string        `json:"waitForPolicies,omitempty"`
}

type SchedulingConfigPayload struct {
}

func init() {
	SchemeBuilder.Register(&WekaPolicy{}, &WekaPolicyList{})
}
