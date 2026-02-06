package domain

import "encoding/json"

// DriveEntry represents a drive in the weka.io/weka-drives annotation (non-proxy mode).
type DriveEntry struct {
	Serial      string `json:"serial"`
	CapacityGiB int    `json:"capacity_gib"`
}

// ParseDriveEntries parses the weka.io/weka-drives annotation value, handling both
// the new []DriveEntry format and the old []string format for backward compatibility.
// Returns (entries, isOldFormat, error).
func ParseDriveEntries(annotation string) ([]DriveEntry, bool, error) {
	if annotation == "" {
		return nil, false, nil
	}

	// Try new format first: []DriveEntry
	var entries []DriveEntry
	if err := json.Unmarshal([]byte(annotation), &entries); err == nil {
		// Verify it's actually the new format by checking that we didn't get zero-value structs
		// from a plain string array. A plain ["serial"] would fail unmarshal into []DriveEntry,
		// so if we got here it's genuinely the new format.
		return entries, false, nil
	}

	// Fallback: try old format []string
	var serials []string
	if err := json.Unmarshal([]byte(annotation), &serials); err != nil {
		return nil, false, err
	}

	entries = make([]DriveEntry, 0, len(serials))
	for _, s := range serials {
		if s != "" {
			entries = append(entries, DriveEntry{Serial: s, CapacityGiB: 0})
		}
	}
	return entries, true, nil
}

// DriveEntrySerials extracts serial strings from a slice of DriveEntry.
func DriveEntrySerials(entries []DriveEntry) []string {
	serials := make([]string, 0, len(entries))
	for _, e := range entries {
		serials = append(serials, e.Serial)
	}
	return serials
}

// SharedDriveInfo represents a signed drive for proxy mode
// This matches the format returned by weka_runtime.py sign_device_path_for_proxy()
type SharedDriveInfo struct {
	PhysicalUUID string `json:"physical_uuid"` // Physical UUID from proxy signing
	Serial       string `json:"serial"`        // Drive serial number
	CapacityGiB  int    `json:"capacity_gib"`  // Capacity in GiB
	Type         string `json:"type"`          // Drive type (e.g., QLC, TLC)
}
