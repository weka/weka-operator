package domain

// SharedDriveInfo represents a signed drive for proxy mode
// This matches the format returned by weka_runtime.py sign_device_path_for_proxy()
type SharedDriveInfo struct {
	PhysicalUUID string `json:"physical_uuid"` // Physical UUID from proxy signing
	Serial       string `json:"serial"`        // Drive serial number
	CapacityGiB  int    `json:"capacity_gib"`  // Capacity in GiB
	Type         string `json:"type"`          // Drive type (e.g., QLC, TLC)
}
