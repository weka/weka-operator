package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaManualOperationAction enumerates the supported WekaManualOperation actions.
type WekaManualOperationAction string

const (
	WekaManualOperationActionSignDrives              WekaManualOperationAction = opSignDrives
	WekaManualOperationActionDiscoverDrives          WekaManualOperationAction = opDiscoverDrives
	WekaManualOperationActionForceResignDrives       WekaManualOperationAction = opForceResignDrives
	WekaManualOperationActionBlockDrives             WekaManualOperationAction = opBlockDrives
	WekaManualOperationActionUnblockDrives           WekaManualOperationAction = opUnblockDrives
	WekaManualOperationActionEnsureNICs              WekaManualOperationAction = opEnsureNICs
	WekaManualOperationActionRemoteTracesSession     WekaManualOperationAction = opRemoteTracesSession
	WekaManualOperationActionCleanStaleVirtualDrives WekaManualOperationAction = opCleanStaleVirtualDrives
)

// WekaManualOperationSpec defines the desired state of WekaManualOperation
type WekaManualOperationSpec struct {
	// +kubebuilder:validation:Enum=sign-drives;discover-drives;force-resign-drives;block-drives;unblock-drives;ensure-nics;remote-traces-session;clean-stale-virtual-drives
	Action             WekaManualOperationAction `json:"action"`
	Payload            ManualOperatorPayload     `json:"payload"`
	Image              *string                   `json:"image,omitempty"`
	ImagePullSecret    *string                   `json:"imagePullSecret,omitempty"`
	Tolerations        []v1.Toleration           `json:"tolerations,omitempty"`
	ServiceAccountName string                    `json:"serviceAccountName,omitempty"`
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
	SignDrives                *SignDrivesPayload              `json:"signDrivesPayload,omitempty"`
	BlockDrives               *BlockDrivesPayload             `json:"blockDrivesPayload,omitempty"`
	DiscoverDrives            *DiscoverDrivesPayload          `json:"discoverDrivesPayload,omitempty"`
	EnsureNICs                *EnsureNICsPayload              `json:"ensureNICsPayload,omitempty"`
	ForceResignDrives         *ForceResignDrivesPayload       `json:"forceResignDrivesPayload,omitempty"`
	RemoteTracesSessionConfig *RemoteTracesSessionConfig      `json:"remoteTracesSessionPayload,omitempty"`
	CleanStaleVirtualDrives   *CleanStaleVirtualDrivesPayload `json:"cleanStaleVirtualDrivesPayload,omitempty"`
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

// CleanStaleVirtualDrivesPayload is the shared payload for the clean-stale-virtual-drives
// operation, used by both WekaManualOperation (one-shot) and WekaPolicy (periodic).
//
// The operation scans every ssdproxy's virtual drives (VIDs) and diffs them against
// the union of all live WekaContainer allocations to find stale VIDs. Detection always
// runs and reports; deletion is opt-in and double-gated (see DeleteStaleVids).
type CleanStaleVirtualDrivesPayload struct {
	// NodeSelector limits the scan to ssdproxies on nodes matching these labels.
	// Empty = all nodes that have an ssdproxy.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// OnlyNonExistingClusters restricts the stale set to VIDs whose owner cluster GUID has NO
	// WekaCluster CR at all (category dead_cluster) — the safe-by-construction subset (no live
	// cluster could be mid-allocating them). Excludes live_cluster_unclaimed. Recommended ON
	// when pairing with deletion.
	// +kubebuilder:default=false
	OnlyNonExistingClusters bool `json:"onlyNonExistingClusters,omitempty"`
	// DeleteStaleVids enables ACTUAL removal of detected stale VIDs. DANGEROUS — disabled by
	// default. Even when true, a VID is only removed if it was reported stale and UNCHANGED on
	// the previous cycle (fingerprint match) and is still unclaimed at removal time. Acts as the
	// user's confirmation.
	// +kubebuilder:default=false
	DeleteStaleVids bool `json:"deleteStaleVids,omitempty"`
}

// StaleVirtualDriveInfo describes a single stale virtual drive detected on a proxy.
type StaleVirtualDriveInfo struct {
	Node             string `json:"node"`
	PhysicalUUID     string `json:"physicalUUID"`
	Serial           string `json:"serial,omitempty"`
	VirtualUUID      string `json:"virtualUUID"`
	OwnerClusterGUID string `json:"ownerClusterGUID"`
	SizeGB           int    `json:"sizeGB"`
	// Category is "dead_cluster" (no WekaCluster CR has the owner GUID) or
	// "live_cluster_unclaimed" (a WekaCluster CR exists but no container claims the VID).
	Category string `json:"category"`
}

// Stale VID categories.
const (
	StaleVidCategoryDeadCluster          = "dead_cluster"
	StaleVidCategoryLiveClusterUnclaimed = "live_cluster_unclaimed"
)

// StaleVirtualDrivesResult is written to the manual op Status.Result / policy Status.LastResult
// as JSON each cycle. It is inspectable and the Fingerprint drives the two-cycle stability gate.
type StaleVirtualDrivesResult struct {
	ScannedNodes int     `json:"scannedNodes"`
	StaleCount   int     `json:"staleCount"`
	StaleTiB     float64 `json:"staleTiB"`
	// Fingerprint is a stable hash over the sorted (node+virtualUuid) of the stale set.
	Fingerprint string `json:"fingerprint"`
	// DeletionEligible is true when Fingerprint == the previous run's fingerprint (set unchanged).
	DeletionEligible bool                    `json:"deletionEligible"`
	StaleVids        []StaleVirtualDriveInfo `json:"staleVids,omitempty"`
	// Deleted holds the virtualUuids removed this run.
	Deleted []string `json:"deleted,omitempty"`
	// Err carries a scan/operation error message, if any.
	Err string `json:"err,omitempty"`
}

func init() {
	SchemeBuilder.Register(&WekaManualOperation{}, &WekaManualOperationList{})
}
