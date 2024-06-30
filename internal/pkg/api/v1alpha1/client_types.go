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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type DriverSpec struct{}

type ClientContainerSpec struct {
	Debug bool `json:"debug,omitempty"`
}

type AgentContainerSpec struct {
	Debug bool `json:"debug,omitempty"`
}

type ObjectReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// WekaClientSpec defines the desired state of WekaClient
type WekaClientSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Used in new format
	Image              string            `json:"image"`
	ImagePullSecret    string            `json:"imagePullSecret,omitempty"`
	Port               int               `json:"port,omitempty"`
	AgentPort          int               `json:"agentPort,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	WekaSecretRef      string            `json:"wekaSecretRef,omitempty"`
	NetworkSelector    NetworkSelector   `json:"network,omitempty"`
	DriversDistService string            `json:"driversDistService,omitempty"`
	JoinIps            []string          `json:"joinIpPorts,omitempty"`
	TargetCluster      ObjectReference   `json:"targetCluster,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy           CpuPolicy            `json:"cpuPolicy,omitempty"`
	CoresNumber         int                  `json:"coresNum,omitempty"`
	CoreIds             []int                `json:"coreIds,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations         []string             `json:"tolerations,omitempty"`
	RawTolerations      []v1.Toleration      `json:"rawTolerations,omitempty"`
	AdditionalMemory    int                  `json:"additionalMemory,omitempty"`
}

// WekaClientStatus defines the observed state of WekaClient
type WekaClientStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func (s *WekaClientStatus) SetCondition(condition metav1.Condition) {
	if s.Conditions == nil {
		s.Conditions = []metav1.Condition{}
	}
	meta.SetStatusCondition(&s.Conditions, condition)
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// WekaClient is the Schema for the clients API
type WekaClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaClientSpec   `json:"spec,omitempty"`
	Status WekaClientStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WekaClientList contains a list of WekaClient
type WekaClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaClient `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WekaClient{}, &WekaClientList{})
}
