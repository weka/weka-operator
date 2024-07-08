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
	"context"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type NetworkSelector struct {
	EthSlots  []string `json:"ethSlots,omitempty"`
	EthDevice string   `json:"ethDevice,omitempty"`
	UdpMode   bool     `json:"udpMode,omitempty"`
}

type AdditionalMemory struct {
	Compute int `json:"compute,omitempty"`
	Drive   int `json:"drive,omitempty"`
	S3      int `json:"s3,omitempty"`
}

type WekaConfig struct {
	ComputeContainers   *int `json:"computeContainers,omitempty"`
	DriveContainers     *int `json:"driveContainers,omitempty"`
	S3Containers        int  `json:"s3Containers,omitempty"`
	ComputeCores        int  `json:"computeCores,omitempty"`
	DriveCores          int  `json:"driveCores,omitempty"`
	S3Cores             int  `json:"s3Cores,omitempty"`
	NumDrives           int  `json:"numDrives,omitempty"`
	S3ExtraCores        int  `json:"s3ExtraCores,omitempty"`
	DriveHugepages      int  `json:"driveHugepages,omitempty"`
	ComputeHugepages    int  `json:"computeHugepages,omitempty"`
	S3FrontendHugepages int  `json:"s3FrontendHugepages,omitempty"`
	EnvoyCores          int  `json:"envoyCores,omitempty"`
}

type WekaHomeConfig struct {
	Endpoint      string `json:"endpoint,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	CacertSecret  string `json:"cacertSecret,omitempty"`
	EnableStats   *bool  `json:"enableStats"`
}

// WekaClusterSpec defines the desired state of WekaCluster
type WekaClusterSpec struct {
	Size               int               `json:"size,omitempty"`
	Template           string            `json:"template"`
	Topology           string            `json:"topology"`
	Image              string            `json:"image"`
	ImagePullSecret    string            `json:"imagePullSecret,omitempty"`
	OsDistro           string            `json:"osDistro,omitempty"`
	CoreOSBuildSpec    *CoreOSBuildSpec  `json:"coreOSBuildSpec,omitempty"`
	COSBuildSpec       *COSBuildSpec     `json:"cosBuildSpec,omitempty"`
	DriversDistService string            `json:"driversDistService,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	//+kubebuilder:validation:Enum=auto;shared;dedicated;dedicated_ht;manual
	//+kubebuilder:default=auto
	CpuPolicy           CpuPolicy            `json:"cpuPolicy,omitempty"`
	TracesConfiguration *TracesConfiguration `json:"tracesConfiguration,omitempty"`
	Tolerations         []string             `json:"tolerations,omitempty"`
	RawTolerations      []v1.Toleration      `json:"rawTolerations,omitempty"`
	WekaHomeConfig      *WekaHomeConfig      `json:"wekaHomeEndpoint,omitempty"`
	Ipv6                bool                 `json:"ipv6,omitempty"`
	AdditionalMemory    AdditionalMemory     `json:"additionalMemory,omitempty"`
	Ports               ClusterPorts         `json:"ports,omitempty"`
	MaxFdsPerNode       int                  `json:"maxFdsPerNode,omitempty"`
	OperatorSecretRef   string               `json:"operatorSecretRef,omitempty"`
	ExpandEndpoints     []string             `json:"expandEndpoints,omitempty"`
	Dynamic             *WekaConfig          `json:"dynamicTemplate,omitempty"`
}

func (c *WekaClusterSpec) GetAdditionalMemory(mode string) int {
	additionalMemory := 0
	switch mode {
	case WekaContainerModeDrive:
		additionalMemory = c.AdditionalMemory.Drive
	case WekaContainerModeCompute:
		additionalMemory = c.AdditionalMemory.Compute
	case WekaContainerModeS3:
		additionalMemory = c.AdditionalMemory.S3
	}
	return additionalMemory
}

type ClusterPorts struct {
	// We should not be updating Spec, as it's a user interface and we should not break ability to update spec file
	// Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here
	BasePort    int `json:"basePort,omitempty"`
	PortRange   int `json:"portRange,omitempty"`
	LbPort      int `json:"lbPort,omitempty"`
	LbAdminPort int `json:"lbAdminPort,omitempty"`
	S3Port      int `json:"s3Port,omitempty"`
}

// WekaClusterStatus defines the observed state of WekaCluster
type WekaClusterStatus struct {
	Status           string             `json:"status"`
	Conditions       []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	Throughput       string             `json:"throughput"`
	ClusterID        string             `json:"clusterID,omitempty"`
	TraceId          string             `json:"traceId,omitempty"`
	SpanID           string             `json:"spanId,omitempty"`
	LastAppliedImage string             `json:"lastAppliedImage,omitempty"` // Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later
	LastAppliedSpec  string             `json:"lastAppliedSpec,omitempty"`
	Ports            ClusterPorts       `json:"ports,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:spec
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status of the cluster",priority=0
// +kubebuilder:printcolumn:name="Cluster ID",type="string",JSONPath=".status.ClusterID",description="Weka cluster ID",priority=2
// +kubebuilder:printcolumn:name="Compute Containers",type="integer",JSONPath=".spec.dynamicTemplate.computeContainers",description="Number of compute containers",priority=3
// +kubebuilder:printcolumn:name="Drive Containers",type="integer",JSONPath=".spec.dynamicTemplate.driveContainers",description="Number of drive containers",priority=4
// +kubebuilder:printcolumn:name="S3 Containers",type="integer",JSONPath=".spec.dynamicTemplate.S3Containers",description="Number of S3 containers",priority=5
// +kubebuilder:printcolumn:name="Compute Cores",type="integer",JSONPath=".spec.dynamicTemplate.computeCores",description="Number of compute cores",priority=6
// +kubebuilder:printcolumn:name="Drive Cores",type="integer",JSONPath=".spec.dynamicTemplate.driveCores",description="Number of drive cores",priority=7
// +kubebuilder:printcolumn:name="Drives",type="integer",JSONPath=".spec.dynamicTemplate.NumDrives",description="Number of drives",priority=8
// +kubebuilder:printcolumn:name="S3 Cores",type="integer",JSONPath=".spec.dynamicTemplate.s3Cores",description="Number of S3 cores",priority=9

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

const DefaultOrg = "Root"

func (c *WekaCluster) GetOperatorSecretName() string {
	if c.Spec.OperatorSecretRef != "" {
		return c.Spec.OperatorSecretRef
	}
	return string("weka-operator-" + c.GetUID())
}

func (c *WekaCluster) GetLastGuidPart() string {
	return util.GetLastGuidPart(c.GetUID())
}

func (c *WekaCluster) GetUserClusterUsername() string {
	return "weka" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetClusterClientUsername() string {
	return "wekaclient" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetOperatorClusterUsername() string {
	return "weka-operator-" + c.GetLastGuidPart()
}

func (c *WekaCluster) GetInitialOperatorUsername() string {
	return "admin"
}

func (c *WekaCluster) GetUserSecretName() string {
	name := c.Name
	return "weka-cluster-" + name
}

func (c *WekaCluster) GetClientSecretName() string {
	name := c.Name
	return "weka-client-" + name
}

func (c *WekaCluster) GetCSISecretName() string {
	return "weka-csi-" + c.Name
}

func (c *WekaCluster) NewUserLoginSecret() *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.GetUserSecretName(),
			Namespace: c.Namespace,
		},
		StringData: map[string]string{
			"username": c.GetUserClusterUsername(),
			"password": util.GeneratePassword(32),
			"org":      DefaultOrg,
		},
	}
}

func (c *WekaCluster) NewOperatorLoginSecret() *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.GetOperatorSecretName(),
			Namespace: c.Namespace,
		},
		StringData: map[string]string{
			"username":    c.GetOperatorClusterUsername(),
			"password":    util.GeneratePassword(32),
			"join-secret": util.GeneratePassword(64),
			"org":         DefaultOrg,
		},
	}
}

func (c *WekaCluster) NewClientSecret() *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.GetClientSecretName(),
			Namespace: c.Namespace,
		},
		StringData: map[string]string{
			"username": c.GetClusterClientUsername(),
			"password": util.GeneratePassword(32),
			"org":      DefaultOrg,
		},
	}
}

func (c *WekaCluster) NewCsiSecret(endpoints []string) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.GetCSISecretName(),
			Namespace: c.Namespace,
		},
		StringData: map[string]string{
			"username":     c.GetClusterCSIUsername(),
			"password":     util.GeneratePassword(32),
			"organization": DefaultOrg,
			"endpoints":    strings.Join(endpoints, ","),
			"scheme":       "https",
		},
	}
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
		Message: "Cluster is not formed yet",
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

	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:   condition.CondDefaultFsCreated,
		Status: metav1.ConditionFalse, Reason: "Init",
		Message: "Default fsgroup and filesystem are not created yet",
	})
}

func (r *WekaCluster) SelectActiveContainer(ctx context.Context, containers []*WekaContainer, role string) *WekaContainer {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "selectActiveContainer", "role", role)
	defer end()

	for _, container := range containers {
		if container.Spec.Mode != role {
			continue
		}
		if container.Status.ClusterContainerID == nil {
			continue
		}
		return container
	}

	err := errors.New("No container with role found")
	logger.SetError(err, "No container with role found", "role", role)
	return nil
}

func (c *WekaCluster) GetClusterCSIUsername() string {
	return "wekacsi" + c.GetLastGuidPart()
}

func init() {
	SchemeBuilder.Register(&WekaCluster{}, &WekaClusterList{})
}
