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

// ReadDriveAnnotations reads drive entries from the weka.io/weka-full-drives annotation only.
// Returns nil (not an error) when the annotation is absent.
// Never returns entries with zero capacity — entries without capacity are filtered out.
func ReadDriveAnnotations(fullAnnotation string) ([]DriveEntry, error) {
	if fullAnnotation == "" {
		return nil, nil
	}
	var entries []DriveEntry
	if err := json.Unmarshal([]byte(fullAnnotation), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse weka-full-drives: %w", err)
	}
	// Filter out any zero-capacity entries — they must never be used for allocation or capacity calculation.
	result := make([]DriveEntry, 0, len(entries))
	for _, e := range entries {
		if e.CapacityGiB > 0 {
			result = append(result, e)
		}
	}
	return result, nil
}

// ReadAnnotatedDriveSerials returns all drive serial IDs found in either node annotation
// (weka-full-drives or legacy weka-drives), deduplicated. Used in contexts that need to
// know which drives are annotated on the node (e.g. sign-exclusion, block/unblock)
// without needing capacity info.
func ReadAnnotatedDriveSerials(fullAnnotation, legacyAnnotation string) ([]string, error) {
	seen := make(map[string]struct{})
	serials := make([]string, 0)

	if fullAnnotation != "" {
		var entries []DriveEntry
		if err := json.Unmarshal([]byte(fullAnnotation), &entries); err != nil {
			return nil, fmt.Errorf("failed to parse weka-full-drives: %w", err)
		}
		for _, e := range entries {
			if e.Serial != "" {
				if _, ok := seen[e.Serial]; !ok {
					seen[e.Serial] = struct{}{}
					serials = append(serials, e.Serial)
				}
			}
		}
	}

	if legacyAnnotation != "" {
		var legacySerials []string
		if err := json.Unmarshal([]byte(legacyAnnotation), &legacySerials); err != nil {
			return nil, fmt.Errorf("failed to parse weka-drives: %w", err)
		}
		for _, s := range legacySerials {
			if s != "" {
				if _, ok := seen[s]; !ok {
					seen[s] = struct{}{}
					serials = append(serials, s)
				}
			}
		}
	}

	return serials, nil
}

// SharedDriveInfo represents a signed drive for proxy mode.
// This matches the format returned by weka_runtime.py sign_device_path_for_proxy()
type SharedDriveInfo struct {
	PhysicalUUID string `json:"physical_uuid"` // Physical UUID from proxy signing
	Serial       string `json:"serial"`        // Drive serial number
	CapacityGiB  int    `json:"capacity_gib"`  // Capacity in GiB
	Type         string `json:"type"`          // Drive type (e.g., QLC, TLC)
}
