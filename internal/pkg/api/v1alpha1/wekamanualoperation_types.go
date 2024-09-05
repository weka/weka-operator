package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaManualOperationSpec defines the desired state of WekaManualOperation
type WekaManualOperationSpec struct {
	Action          string                `json:"action"`
	Payload         ManualOperatorPayload `json:"payload"`
	Image           string                `json:"image"`
	ImagePullSecret string                `json:"imagePullSecret"`
	Tolerations     []v1.Toleration       `json:"tolerations,omitempty"`
}

// WekaManualOperationStatus defines the observed state of WekaManualOperation
type WekaManualOperationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Result      string      `json:"result"`
	Status      string      `json:"status"`
	CompletedAt metav1.Time `json:"completedAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation",priority=2
// +kubebuilder:printcolumn:name="Result",type="string",JSONPath=".status.result",description="Result",priority=3

// WekaManualOperation is the Schema for the wekamanualoperations API
type WekaManualOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaManualOperationSpec   `json:"spec,omitempty"`
	Status WekaManualOperationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WekaManualOperationList contains a list of WekaManualOperation
type WekaManualOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaManualOperation `json:"items"`
}

type ManualOperatorPayload struct {
	SignDrives     *SignDrivesPayload     `json:"signDrivesPayload,omitempty"`
	BlockDrives    *BlockDrivesPayload    `json:"blockDrivesPayload,omitempty"`
	DiscoverDrives *DiscoverDrivesPayload `json:"discoverDrivesPayload,omitempty"`
}

type NamespacedOwnerWekaObject struct {
	Image           string          `json:"image"`
	ImagePullSecret string          `json:"imagePullSecrets"`
	Tolerations     []v1.Toleration `json:"tolerations,omitempty"`
	Namespace       string          `json:"namespace"`
}

type SignDrivesPayload struct {
	Type         string            `json:"type"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	DevicePaths  []string          `json:"devicePaths,omitempty"`
}

type BlockDrivesPayload struct {
	SerialIDs []string `json:"serialIDs"`
	Node      string   `json:"node"`
}

type DiscoverDrivesPayload struct {
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WekaManualOperation{}, &WekaManualOperationList{})
}
