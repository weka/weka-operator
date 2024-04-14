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

import (
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type NetworkSelector struct {
	EthDevice string `json:"ethDevice,omitempty"`
	UdpMode   bool   `json:"udpMode,omitempty"`
}

// WekaClusterSpec defines the desired state of WekaCluster
type WekaClusterSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of WekaCluster. Edit dummycluster_types.go to remove/update
	Size               int               `json:"size"`
	Template           string            `json:"template"`
	Topology           string            `json:"topology"`
	Image              string            `json:"image"`
	ImagePullSecret    string            `json:"imagePullSecret,omitempty"`
	DriversDistService string            `json:"driversDistService,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	// +kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy                 CpuPolicy `json:"cpuPolicy,omitempty"`
	DriveAppendSetupCommand   string    `json:"driveAppendSetupCommand,omitempty"`
	ComputeAppendSetupCommand string    `json:"computeAppendSetupCommand,omitempty"`
}

// WekaClusterStatus defines the observed state of WekaCluster
type WekaClusterStatus struct {
	Status     string             `json:"status"`
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	Throughput string             `json:"throughput"`
	ClusterID  string             `json:"clusterID,omitempty"`
	TraceId    string             `json:"traceId,omitempty"`
	SpanID     string             `json:"spanId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type WekaCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaClusterSpec   `json:"spec,omitempty"`
	Status WekaClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WekaClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaCluster `json:"items"`
}

func (status *WekaClusterStatus) InitStatus() {
	status.Conditions = []metav1.Condition{}

	status.Status = "Init"
	// Set Predefined conditions to explicit False for visibility
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondPodsCreated,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "The pods for the custom resource are not created yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondPodsReady,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "The pods for the custom resource are not ready yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondClusterSecretsCreated,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Secrets are not created yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondClusterSecretsApplied,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Secrets are not applied yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondClusterCreated,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Secrets are not applied yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondDrivesAdded,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Drives are not added yet",
	})

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondIoStarted,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Weka Cluster IO is not started",
	})
}

func init() {
	SchemeBuilder.Register(&WekaCluster{}, &WekaClusterList{})
}
