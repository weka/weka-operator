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

// ClientSpec defines the desired state of WekaClient
type ClientSpec struct {
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
	CoreIds             []int                `json:"coreIds,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
}

// ClientStatus defines the observed state of WekaClient
type ClientStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	ContainerList []Container `json:"containerList,omitempty"`
	ProcessList   []Process   `json:"processList,omitempty"`
}

// Container is a single line from `weka cluster container`
//
//	weka cluster container -J
//
// Example:
//
//	    {
//	       "container_name": "default",
//	       "host_id": "HostId<4>",
//	       "host_ip": "10.108.244.140",
//	       "hostname": "mbp-k8s-oci-backend-4",
//	       "last_failure": "Applying resources on container",
//	       "sw_release_string": "4.2.8.312-6a24a384e98df01fb2ab00075d196b81",
//					...
//	   }
type Container struct {
	Id          string `json:"host_id,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Container   string `json:"container_name,omitempty"`
	Ips         string `json:"host_ip,omitempty"`
	Status      string `json:"status,omitempty"`
	Release     string `json:"sw_release_string,omitempty"`
	FailureText string `json:"last_failure,omitempty"`
	StartTime   string `json:"start_time,omitempty"`
}

// Process is a single weka process running in the agent container
// This is a single line of output from `weka local ps`
// Example:
//
//	{
//			"APIPort": 14000,
//			"containerPid": 66,
//			"internalStatus": {
//					"display_status": "READY",
//					"message": "Ready",
//					"state": "READY"
//			},
//			"isDisabled": false,
//			"isManaged": false,
//			"isMonitoring": true,
//			"isPersistent": true,
//			"isRunning": true,
//			"lastFailure": "Added to cluster",
//			"lastFailureText": "Added to cluster (1 hour ago)",
//			"lastFailureTime": "2023-12-15T14:17:24.576897Z",
//			"name": "client",
//			"runStatus": "Running",
//			"type": "weka",
//			"uptime": 5693.65999999999985,
//			"versionName": "4.2.7.442-4ba059e153b2dce7e3e490bfc43eb5e2"
//	}
type Process struct {
	APIPort         int32          `json:"apiPort,omitempty"`
	ContainerPid    int32          `json:"containerPid,omitempty"`
	InternalStatus  InternalStatus `json:"internalStatus,omitempty"`
	IsDisabled      bool           `json:"isDisabled,omitempty"`
	IsManaged       bool           `json:"isManaged,omitempty"`
	IsMonitoring    bool           `json:"isMonitoring,omitempty"`
	IsPersistent    bool           `json:"isPersistent,omitempty"`
	IsRunning       bool           `json:"isRunning,omitempty"`
	LastFailure     string         `json:"lastFailure,omitempty"`
	LastFailureText string         `json:"lastFailureText,omitempty"`
	LastFailureTime string         `json:"lastFailureTime,omitempty"`
	Name            string         `json:"name,omitempty"`
	RunStatus       string         `json:"runStatus,omitempty"`
	Type            string         `json:"type,omitempty"`
	Uptime          string         `json:"uptime,omitempty"`
	VersionName     string         `json:"versionName,omitempty"`
}

type InternalStatus struct {
	DisplayStatus string `json:"display_status,omitempty"`
	Message       string `json:"message,omitempty"`
	State         string `json:"state,omitempty"`
}

func (s *ClientStatus) SetCondition(condition metav1.Condition) {
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

	Spec   ClientSpec   `json:"spec,omitempty"`
	Status ClientStatus `json:"status,omitempty"`
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
