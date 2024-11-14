package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaManualOperationSpec defines the desired state of WekaManualOperation
type WekaManualOperationSpec struct {
	// +kubebuilder:validation:Enum=sign-drives;discover-drives;force-resign-drives;block-drives;ensure-nics
	Action          string                `json:"action"`
	Payload         ManualOperatorPayload `json:"payload"`
	Image           string                `json:"image"`
	ImagePullSecret string                `json:"imagePullSecret"`
	Tolerations     []v1.Toleration       `json:"tolerations,omitempty"`
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
	SignDrives        *SignDrivesPayload        `json:"signDrivesPayload,omitempty"`
	BlockDrives       *BlockDrivesPayload       `json:"blockDrivesPayload,omitempty"`
	DiscoverDrives    *DiscoverDrivesPayload    `json:"discoverDrivesPayload,omitempty"`
	EnsureNICs        *EnsureNICsPayload        `json:"ensureNICsPayload,omitempty"`
	ForceResignDrives *ForceResignDrivesPayload `json:"forceResignDrivesPayload,omitempty"`
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
	PCIDevices *PCIDevices `json:"pciDevices,omitempty"`
}

type ForceResignDrivesPayload struct {
	NodeName      NodeName `json:"node_name"`
	DeviceSerials []string `json:"device_serials,omitempty"`
	DevicePaths   []string `json:"device_paths,omitempty"`
}

type BlockDrivesPayload struct {
	SerialIDs []string `json:"serialIDs"`
	Node      string   `json:"node"`
}

type DiscoverDrivesPayload struct {
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WekaManualOperation{}, &WekaManualOperationList{})
}
