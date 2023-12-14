/*
Copyright 2023.

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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type BackendSpec struct {
	// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}$`
	IP string `json:"ip,omitempty"`

	NetInterface string `json:"netInterface,omitempty"`
}

type DriverSpec struct{}

type ClientContainerSpec struct {
	Debug bool `json:"debug,omitempty"`
}

type AgentContainerSpec struct {
	Debug bool `json:"debug,omitempty"`
}

// ClientSpec defines the desired state of Client
type ClientSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Example: 4.2.6.3212-61e9145d99a867bf6aab053cd75ea77f
	// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){2}[0-9]{1,3}(\..*)?$`
	Version string `json:"version,omitempty"`

	// +kubebuilder:default:="quay.io/weka.io/weka-in-container"
	// +optional
	Image string `json:"image,omitempty"`

	Backend       BackendSpec `json:"backend,omitempty"`
	IONodeCount   int32       `json:"ioNodeCount,omitempty"`
	ManagementIPs string      `json:"managementIPs,omitempty"`

	ImagePullSecretName string `json:"imagePullSecretName,omitempty"`

	Client ClientContainerSpec `json:"client,omitempty"`
	Agent  AgentContainerSpec  `json:"agent,omitempty"`
}

// ClientStatus defines the observed state of Client
type ClientStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Client is the Schema for the clients API
type Client struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClientSpec   `json:"spec,omitempty"`
	Status ClientStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClientList contains a list of Client
type ClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Client `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Client{}, &ClientList{})
}
