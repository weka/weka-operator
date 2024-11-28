package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type TombstoneSpec struct {
	CrType string `json:"cr_type"`
	// +kubebuilder:validation:MinLength=1
	CrId            string          `json:"cr_id"`
	NodeAffinity    NodeName        `json:"node_affinity"`
	PersistencePath string          `json:"persistence_path,omitempty"`
	ContainerName   string          `json:"container_name,omitempty"`
	Tolerations     []v1.Toleration `json:"tolerations,omitempty"`
}

type TombstoneStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Container",type="string",JSONPath=".spec.container_name",description="Container name",priority=0
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".spec.node_affinity",description="Node",priority=0
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Creation time",priority=0
type Tombstone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TombstoneSpec   `json:"spec,omitempty"`
	Status TombstoneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TombstoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tombstone `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tombstone{}, &TombstoneList{})
}
