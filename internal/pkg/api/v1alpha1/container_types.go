package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerSpec   `json:"spec,omitempty"`
	Status ContainerStatus `json:"status,omitempty"`
}

type ContainerSpec struct {
	NodeName            string   `json:"nodeName"`
	Drives              []string `json:"drives"`
	Image               string   `json:"image"`
	ImagePullSecretName string   `json:"imagePullSecretName,omitempty"`
	Name                string   `json:"name"`
	WekaVersion         string   `json:"wekaVersion"`
	BackendIP           string   `json:"backendIP"`
	ManagementPort      int32    `json:"managementPort,omitempty"`
	InterfaceName       string   `json:"interfaceName,omitempty"`

	// WekaUsername corev1.EnvVarSource `json:"wekaUsername,omitempty"`
	// WekaPassword corev1.EnvVarSource `json:"wekaPassword,omitempty"`
}

type ContainerStatus struct {
	AssignedNode v1.Node `json:"assignedNode"`
}

// +kubebuilder:object:root=true
type WekaContainerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaContainer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WekaContainer{}, &WekaContainerList{})
}
