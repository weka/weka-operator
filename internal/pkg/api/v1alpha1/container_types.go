package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type WekaContainer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaContainerSpec   `json:"spec,omitempty"`
	Status WekaContainerStatus `json:"status,omitempty"`
}

type WekaContainerSpec struct {
	NodeAffinity      string  `json:"nodeAffinity,omitempty"`
	Port              int     `json:"port,omitempty"`
	AgentPort         int     `json:"agentPort,omitempty"`
	Image             string  `json:"image"`
	ImagePullSecret   string  `json:"imagePullSecret,omitempty"`
	WekaContainerName string  `json:"name"`
	Mode              string  `json:"mode"` // TODO: How to define as enum?
	NumCores          int     `json:"numCores"`
	CoreIds           []int   `json:"coreIds,omitempty"`
	Network           Network `json:"network,omitempty"`
	Hugepages         string  `json:"hugepages,omitempty"`
}

type Network struct {
	EthDevice string `json:"ethDevice,omitempty"`
	UdpMode   bool   `json:"udpMode,omitempty"`
}

type WekaContainerStatus struct {
	Status             string `json:"status"`
	ManagementIP       string `json:"managementIP,omitempty"`
	ClusterContainerID string `json:"containerID,omitempty"`
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
