package domain

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
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
// This matches the format returned by weka_runtime.py list_weka_proxy_drives_with_sign_tool()
type SharedDriveInfo struct {
	PhysicalUUID string `json:"physical_uuid"`   // Physical UUID from proxy signing
	Serial       string `json:"serial"`          // Drive serial number
	CapacityGiB  int    `json:"capacity_gib"`    // Capacity in GiB
	Type         string `json:"type"`            // Drive type (e.g., QLC, TLC)
	Model        string `json:"model,omitempty"` // Device model, used for drive type overrides
}

// ReadNodeSharedDrives decodes the node's weka.io/weka-shared-drives annotation.
//
// signed is false when the node carries no shared-drive list at all — annotation absent, empty, or
// decoding to an empty slice. Callers must treat that as "not signed yet" and must not write an
// empty annotation back: the cluster_signed_drives admission webhook treats the annotation's mere
// presence as "signed" and would flip the node from bootstrap-skip to enforce.
func ReadNodeSharedDrives(node *corev1.Node) (drives []SharedDriveInfo, signed bool, err error) {
	raw := node.Annotations[consts.AnnotationSharedDrives]
	if raw == "" {
		return nil, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &drives); err != nil {
		return nil, false, fmt.Errorf("failed to parse shared drives annotation: %w", err)
	}
	if len(drives) == 0 {
		return nil, false, nil
	}
	return drives, true, nil
}

// ReadBlockedDriveSerials decodes the node's weka.io/blocked-drives annotation (drives blocked by
// serial). Returns an empty slice when the annotation is absent or empty.
func ReadBlockedDriveSerials(node *corev1.Node) ([]string, error) {
	return readStringSliceAnnotation(node, consts.AnnotationBlockedDrives)
}

// ReadBlockedDrivePhysicalUUIDs decodes the node's weka.io/blocked-drives-physical-uuids
// annotation. Returns an empty slice when the annotation is absent or empty.
func ReadBlockedDrivePhysicalUUIDs(node *corev1.Node) ([]string, error) {
	return readStringSliceAnnotation(node, consts.AnnotationBlockedDrivesPhysicalUuids)
}

// ReadBlockedDriveVirtualUUIDs decodes the node's weka.io/blocked-drives-virtual-uuids annotation
// (individual virtual drives blocked on a drive-sharing node). Returns an empty slice when the
// annotation is absent or empty.
func ReadBlockedDriveVirtualUUIDs(node *corev1.Node) ([]string, error) {
	return readStringSliceAnnotation(node, consts.AnnotationBlockedDrivesVirtualUuids)
}

func readStringSliceAnnotation(node *corev1.Node, annotation string) ([]string, error) {
	values := []string{}
	raw := node.Annotations[annotation]
	if raw == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation: %w", annotation, err)
	}
	return values, nil
}
