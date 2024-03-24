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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type NetworkSelector struct {
	EthDevice string `json:"ethDevice,omitempty"`
	UdpMode   bool   `json:"udpMode,omitempty"`
}

// DummyClusterSpec defines the desired state of DummyCluster
type DummyClusterSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of DummyCluster. Edit dummycluster_types.go to remove/update
	Size                    int    `json:"size"`
	Template                string `json:"template"`
	Topology                string `json:"topology"`
	Image                   string `json:"image"`
	ImagePullSecret         string `json:"imagePullSecret,omitempty"`
	WekaContainerNamePrefix string `json:"wekaContainerNamePrefix"`
}

// DummyClusterStatus defines the observed state of DummyCluster
type DummyClusterStatus struct {
	Status     string             `json:"status"`
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	Throughput string             `json:"throughput"`
	ClusterID  string             `json:"clusterID"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type DummyCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DummyClusterSpec   `json:"spec,omitempty"`
	Status DummyClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DummyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DummyCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DummyCluster{}, &DummyClusterList{})
}
