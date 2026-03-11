package domain

import (
	"encoding/json"
	"fmt"
)

// DriveEntry represents a drive in the weka.io/weka-drives annotation (non-proxy mode).
type DriveEntry struct {
	Serial      string `json:"serial"`
	CapacityGiB int    `json:"capacity_gib"`
}

// DriveEntrySerials extracts serial strings from a slice of DriveEntry.
func DriveEntrySerials(entries []DriveEntry) []string {
	serials := make([]string, 0, len(entries))
	for _, e := range entries {
		serials = append(serials, e.Serial)
	}
	return serials
}

// ReadDriveAnnotations reads drive entries preferring the full annotation over the legacy one.
// Pass the raw annotation string values (empty string if annotation not present).
// fullAnnotation: value of weka.io/weka-full-drives
// legacyAnnotation: value of weka.io/weka-drives
func ReadDriveAnnotations(fullAnnotation, legacyAnnotation string) ([]DriveEntry, error) {
	if fullAnnotation != "" {
		var entries []DriveEntry
		if err := json.Unmarshal([]byte(fullAnnotation), &entries); err != nil {
			return nil, fmt.Errorf("failed to parse weka-full-drives: %w", err)
		}
		return entries, nil
	}
	// Fallback to legacy annotation (weka.io/weka-drives) — always []string format
	if legacyAnnotation == "" {
		return nil, nil
	}
	var serials []string
	if err := json.Unmarshal([]byte(legacyAnnotation), &serials); err != nil {
		return nil, fmt.Errorf("failed to parse weka-drives: %w", err)
	}
	entries := make([]DriveEntry, 0, len(serials))
	for _, s := range serials {
		if s != "" {
			entries = append(entries, DriveEntry{Serial: s, CapacityGiB: 0})
		}
	}
	return entries, nil
}

// SharedDriveInfo represents a signed drive for proxy mode.
// This matches the format returned by weka_runtime.py sign_device_path_for_proxy()
type SharedDriveInfo struct {
	PhysicalUUID string `json:"physical_uuid"` // Physical UUID from proxy signing
	Serial       string `json:"serial"`        // Drive serial number
	CapacityGiB  int    `json:"capacity_gib"`  // Capacity in GiB
	Type         string `json:"type"`          // Drive type (e.g., QLC, TLC)
}
