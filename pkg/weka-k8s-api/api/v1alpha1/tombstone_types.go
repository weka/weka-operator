package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type TombstoneSpec struct {
	CrType          string   `json:"cr_type"`
	CrId            string   `json:"cr_id"`
	NodeAffinity    NodeName `json:"node_affinity"`
	PersistencePath string   `json:"persistence_path,omitempty"`
	ContainerName   string   `json:"container_name,omitempty"`
}

type TombstoneStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
