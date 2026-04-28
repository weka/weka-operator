package domain

import weka "github.com/weka/weka-k8s-api/api/v1alpha1"

// SignedDrivesExtendedPayload extends SignDrivesPayload with operator-side fields
// that are added per-node before the pod instruction is created.
type SignedDrivesExtendedPayload struct {
	weka.SignDrivesPayload
	ExcludedSerialIds     []string `json:"excludedSerialIds,omitempty"`
	SsdProxyContainerUuid string   `json:"ssd_proxy_container_uuid,omitempty"`
}

// DriveRawInfo is a raw disk found on the node (all block devices, not just Weka-formatted ones).
type DriveRawInfo struct {
	SerialId    string `json:"serial_id"`
	Path        string `json:"path"`
	IsMounted   bool   `json:"is_mounted"`
	CapacityGiB int    `json:"capacity_gib"`
}

// DriveNodeResults is written by the pod runtime and read by the operator after discover-drives
// or sign-drives adhoc operations. Err uses *string (not error) for clean JSON null/string encoding.
type DriveNodeResults struct {
	Err         *string          `json:"err"`
	Drives      []DriveInfo      `json:"drives"`
	RawDrives   []DriveRawInfo   `json:"raw_drives"`
	ProxyDrives []SharedDriveInfo `json:"proxy_drives,omitempty"`
}

// ResignDrivesResult is written by force-resign-drives adhoc operation.
type ResignDrivesResult struct {
	Err    string   `json:"err,omitempty"`
	Drives []string `json:"drives"`
}

// BuiltDriversResult is written by the drivers-builder mode.
type BuiltDriversResult struct {
	WekaVersion          string `json:"weka_version"`
	KernelSignature      string `json:"kernel_signature"`
	WekaPackNotSupported bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool  `json:"no_weka_drivers_handling"`
	Err                  string `json:"err"`
}

// FeatureFlagsResult is written by feature-flags-update adhoc-op-with-container operation.
type FeatureFlagsResult struct {
	FeatureFlags *FeatureFlags `json:"feature_flags"`
}
