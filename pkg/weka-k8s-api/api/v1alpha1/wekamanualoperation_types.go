package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaManualOperationSpec defines the desired state of WekaManualOperation
type WekaManualOperationSpec struct {
	// +kubebuilder:validation:Enum=sign-drives;discover-drives;force-resign-drives;block-drives;unblock-drives;ensure-nics;remote-traces-session
	Action             string                `json:"action"`
	Payload            ManualOperatorPayload `json:"payload"`
	Image              *string               `json:"image,omitempty"`
	ImagePullSecret    *string               `json:"imagePullSecret,omitempty"`
	Tolerations        []v1.Toleration       `json:"tolerations,omitempty"`
	ServiceAccountName string                `json:"serviceAccountName,omitempty"`
	// DeletionDelay specifies how long to wait after completion before deleting the resource.
	// Defaults to 5m if not specified.
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^(0|([0-9]+(\\.[0-9]+)?(s|m|h))+)$"
	DeletionDelay *metav1.Duration `json:"deletionDelay,omitempty"`
}

// WekaManualOperationStatus defines the observed state of WekaManualOperation
type WekaManualOperationStatus struct {
	Result      string      `json:"result"`
	Status      string      `json:"status"`
	CompletedAt metav1.Time `json:"completedAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Action",type="string",JSONPath=".spec.action",description="Action",priority=0
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="Status",priority=0
// +kubebuilder:printcolumn:name="Result",type="string",JSONPath=".status.result",description="Result",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation",priority=0

// WekaManualOperation is the Schema for the wekamanualoperations API
type WekaManualOperation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WekaManualOperationSpec   `json:"spec,omitempty"`
	Status WekaManualOperationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WekaManualOperationList contains a list of WekaManualOperation
type WekaManualOperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WekaManualOperation `json:"items"`
}

type ManualOperatorPayload struct {
	SignDrives                *SignDrivesPayload         `json:"signDrivesPayload,omitempty"`
	BlockDrives               *BlockDrivesPayload        `json:"blockDrivesPayload,omitempty"`
	DiscoverDrives            *DiscoverDrivesPayload     `json:"discoverDrivesPayload,omitempty"`
	EnsureNICs                *EnsureNICsPayload         `json:"ensureNICsPayload,omitempty"`
	ForceResignDrives         *ForceResignDrivesPayload  `json:"forceResignDrivesPayload,omitempty"`
	RemoteTracesSessionConfig *RemoteTracesSessionConfig `json:"remoteTracesSessionPayload,omitempty"`
}

type PCIDevices struct {
	// VendorId is the 4-digit hexadecimal vendor ID
	// default for AWS: `1d0f` (Amazon.com, Inc.)
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{4}$`
	VendorId string `json:"vendorId"`
	// DeviceId is the 4-digit hexadecimal device ID
	// default for AWS: `cd01` (NVMe SSD)
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{4}$`
	DeviceId string `json:"deviceId"`
}

type NICType string

const (
	AWS NICType = "aws"
)

type EnsureNICsPayload struct {
	Type           NICType           `json:"type"`
	NodeSelector   map[string]string `json:"nodeSelector,omitempty"`
	DataNICsNumber int               `json:"dataNICsNumber,omitempty"`
}

type SignDrivesPayload struct {
	// +kubebuilder:validation:Enum=aws-all;gcp-all;device-identifiers;device-paths;all-not-root
	Type         string            `json:"type"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	DevicePaths  []string          `json:"devicePaths,omitempty"`
	// PCI vendor and device IDs of the drives to sign.
	// To get the values for VendorId and DeviceId:
	// 1. Run the following command to list all PCI devices on your system:
	//    ```bash
	//    lspci -nn
	//    ```
	// 2. Find the relevant PCI device in the output, which will display both the
	//    vendor and device IDs in square brackets in the format [vendorId:deviceId].
	//    For example:
	//    ```
	//    00:1f.0 Non-Volatile memory controller [0108]: Amazon.com, Inc. NVMe SSD Controller [1d0f:cd01]
	//    ```
	PCIDevices  *PCIDevices  `json:"pciDevices,omitempty"`
	SignOptions *SignOptions `json:"options,omitempty"`
	// Shared enables shared drive signing for proxy mode (defaults to false).
	// When enabled:
	// - Drives are signed for proxy using 'weka-sign-drive sign proxy' command
	// - Drives are signed with a proxy system GUID
	// - Results are stored in weka.io/shared-drives annotation (instead of weka.io/weka-drives)
	// - Physical UUIDs, serial IDs, and capacities are captured
	// - Enables multi-tenant drive sharing via SSD proxy
	Shared bool `json:"shared,omitempty"`
}

type SignOptions struct {
	AllowEraseWekaPartitions    bool `json:"allowEraseWekaPartitions,omitempty"`
	AllowEraseNonWekaPartitions bool `json:"allowEraseNonWekaPartitions,omitempty"`
	AllowNonEmptyDevice         bool `json:"allowNonEmptyDevice,omitempty"`
	SkipTrimFormat              bool `json:"skipTrimFormat,omitempty"`
}

type ForceResignDrivesPayload struct {
	NodeName      NodeName `json:"nodeName"`
	DeviceSerials []string `json:"deviceSerials,omitempty"`
	DevicePaths   []string `json:"devicePaths,omitempty"`
}

type BlockDrivesPayload struct {
	SerialIDs     []string `json:"serialIDs,omitempty"`
	PhysicalUUIDs []string `json:"physicalUUIDs,omitempty"`
	Node          string   `json:"node"`
}

type DiscoverDrivesPayload struct {
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type RemoteTracesSessionConfig struct {
	Cluster      ObjectReference   `json:"cluster,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	HostNetwork  bool              `json:"hostNetwork,omitempty"`
	// Duration specifies how long the trace session should run.
	// WekaManualOperation: defaults to 1 week if omitted/0. CR auto-deletes after expiration.
	// WekaPolicy: defaults to continuous if omitted/0. Resources cleaned up after expiration.
	// Examples: "30m", "2h", "7d", "168h"
	Duration                      metav1.Duration `json:"duration,omitempty"`
	WekahomeEndpointOverride      string          `json:"wekahomeEndpointOverride,omitempty"`
	AllowHttpWekahomeEndpoint     bool            `json:"allowHttpWekahomeEndpoint,omitempty"`
	AllowInsecureWekahomeEndpoint bool            `json:"allowInsecureWekahomeEndpoint,omitempty"`
	WekahomeCaSecret              string          `json:"wekahomeCaSecret,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WekaManualOperation{}, &WekaManualOperationList{})
}
