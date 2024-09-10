package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaPolicySpec defines the desired state of WekaPolicy
type WekaPolicySpec struct {
	Type            string        `json:"type"`
	Payload         PolicyPayload `json:"payload"`
	Image           string        `json:"image"`
	ImagePullSecret string        `json:"imagePullSecret"`
}

// WekaPolicyStatus defines the observed state of WekaPolicy
type WekaPolicyStatus struct {
	Status      string      `json:"status"`
	LastResult  string      `json:"result"`
	LastRunTime metav1.Time `json:"lastRunTime"`

	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

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
	Interval         string                   `json:"interval"`
}

type SchedulingConfigPayload struct {
	AllowNoFds bool `json:"allowNoFds"`
}

func init() {
	SchemeBuilder.Register(&WekaPolicy{}, &WekaPolicyList{})
}
