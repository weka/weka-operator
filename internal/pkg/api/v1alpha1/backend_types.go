package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Backend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackendSpec   `json:"spec,omitempty"`
	Status BackendStatus `json:"status,omitempty"`
}

type BackendSpec struct {
	NodeName string `json:"nodeName"`
}

type BackendStatus struct {
	Node             v1.Node                                `json:"node,omitempty"`
	DriveAssignments map[DriveName]*v1.LocalObjectReference `json:"assignments,omitempty"`
	CoreAssignments  map[CoreId]*v1.LocalObjectReference    `json:"coreAssignments,omitempty"`
}

type (
	DriveName string
	CoreId    string
)

// +kubebuilder:object:root=true

type BackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backend{}, &BackendList{})
}
