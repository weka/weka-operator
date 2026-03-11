package domain

import (
	"testing"
)

func TestReadDriveAnnotations(t *testing.T) {
	tests := []struct {
		name             string
		fullAnnotation   string
		legacyAnnotation string
		expectedEntries  []DriveEntry
		expectError      bool
		errorContains    string
	}{
		// Case 1: Both annotations empty → nil, nil
		{
			name:             "both empty",
			fullAnnotation:   "",
			legacyAnnotation: "",
			expectedEntries:  nil,
			expectError:      false,
		},

		// Case 2: Full annotation only → parses []DriveEntry with serial + capacity
		{
			name:             "full annotation only",
			fullAnnotation:   `[{"serial":"SN001","capacity_gib":1024},{"serial":"SN002","capacity_gib":2048}]`,
			legacyAnnotation: "",
			expectedEntries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 1024},
				{Serial: "SN002", CapacityGiB: 2048},
			},
			expectError: false,
		},

		// Case 3: Legacy annotation only → parses []string, returns []DriveEntry with CapacityGiB=0
		{
			name:             "legacy annotation only",
			fullAnnotation:   "",
			legacyAnnotation: `["SN001","SN002"]`,
			expectedEntries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 0},
				{Serial: "SN002", CapacityGiB: 0},
			},
			expectError: false,
		},

		// Case 4: Both present → full annotation wins (legacy ignored)
		{
			name:             "both present - full wins",
			fullAnnotation:   `[{"serial":"FULL-SN001","capacity_gib":1024}]`,
			legacyAnnotation: `["LEGACY-SN001"]`,
			expectedEntries: []DriveEntry{
				{Serial: "FULL-SN001", CapacityGiB: 1024},
			},
			expectError: false,
		},

		// Case 5: Full annotation invalid JSON → error
		{
			name:             "full annotation invalid json",
			fullAnnotation:   `{invalid json}`,
			legacyAnnotation: "",
			expectedEntries:  nil,
			expectError:      true,
			errorContains:    "failed to parse weka-full-drives",
		},

		// Case 6: Legacy annotation invalid JSON → error
		{
			name:             "legacy annotation invalid json",
			fullAnnotation:   "",
			legacyAnnotation: `[not valid]`,
			expectedEntries:  nil,
			expectError:      true,
			errorContains:    "failed to parse weka-drives",
		},

		// Case 7: Legacy annotation with empty string entries → empty strings are skipped
		{
			name:             "legacy with empty strings",
			fullAnnotation:   "",
			legacyAnnotation: `["SN001","","SN002",""]`,
			expectedEntries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 0},
				{Serial: "SN002", CapacityGiB: 0},
			},
			expectError: false,
		},

		// Case 8: Full annotation with empty array [] → returns empty slice, no error
		{
			name:             "full annotation empty array",
			fullAnnotation:   `[]`,
			legacyAnnotation: "",
			expectedEntries:  []DriveEntry{},
			expectError:      false,
		},

		// Additional: Full annotation with single entry
		{
			name:             "full annotation single entry",
			fullAnnotation:   `[{"serial":"SINGLE","capacity_gib":4096}]`,
			legacyAnnotation: "",
			expectedEntries: []DriveEntry{
				{Serial: "SINGLE", CapacityGiB: 4096},
			},
			expectError: false,
		},

		// Additional: Full annotation with zero capacity
		{
			name:             "full annotation zero capacity",
			fullAnnotation:   `[{"serial":"ZERO-CAP","capacity_gib":0}]`,
			legacyAnnotation: "",
			expectedEntries: []DriveEntry{
				{Serial: "ZERO-CAP", CapacityGiB: 0},
			},
			expectError: false,
		},

		// Additional: Legacy annotation empty array
		{
			name:             "legacy annotation empty array",
			fullAnnotation:   "",
			legacyAnnotation: `[]`,
			expectedEntries:  []DriveEntry{},
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := ReadDriveAnnotations(tt.fullAnnotation, tt.legacyAnnotation)

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
					t.Errorf("error message %q does not contain %q", err.Error(), tt.errorContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if !deepEqualDriveEntries(entries, tt.expectedEntries) {
				t.Errorf("entries mismatch\nexpected: %#v\ngot:      %#v", tt.expectedEntries, entries)
			}
		})
	}
}

// deepEqualDriveEntries compares two slices of DriveEntry.
// Handles nil vs empty slice distinction (nil != []).
func deepEqualDriveEntries(a, b []DriveEntry) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Serial != b[i].Serial || a[i].CapacityGiB != b[i].CapacityGiB {
			return false
		}
	}
	return true
}

// contains checks if substring is in string.
func contains(s, substring string) bool {
	for i := 0; i <= len(s)-len(substring); i++ {
		if s[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

func TestDriveEntrySerials(t *testing.T) {
	tests := []struct {
		name     string
		entries  []DriveEntry
		expected []string
	}{
		{
			name:     "empty slice",
			entries:  []DriveEntry{},
			expected: []string{},
		},
		{
			name: "single entry",
			entries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 1024},
			},
			expected: []string{"SN001"},
		},
		{
			name: "multiple entries",
			entries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 1024},
				{Serial: "SN002", CapacityGiB: 2048},
				{Serial: "SN003", CapacityGiB: 512},
			},
			expected: []string{"SN001", "SN002", "SN003"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DriveEntrySerials(tt.entries)

			if !deepEqualStrings(result, tt.expected) {
				t.Errorf("serials mismatch\nexpected: %#v\ngot:      %#v", tt.expected, result)
			}
		})
	}
}

// deepEqualStrings compares two slices of strings.
func deepEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReadDriveAnnotationsJSONUnmarshal specifically tests JSON unmarshaling edge cases.
func TestReadDriveAnnotationsJSONUnmarshal(t *testing.T) {
	tests := []struct {
		name             string
		fullAnnotation   string
		legacyAnnotation string
		expectError      bool
	}{
		{
			name:             "full: malformed json object",
			fullAnnotation:   `{"serial":"test"}`,
			legacyAnnotation: "",
			expectError:      true,
		},
		{
			name:             "full: unclosed bracket",
			fullAnnotation:   `[{"serial":"test"`,
			legacyAnnotation: "",
			expectError:      true,
		},
		{
			name:             "legacy: malformed json",
			fullAnnotation:   "",
			legacyAnnotation: `["unclosed`,
			expectError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadDriveAnnotations(tt.fullAnnotation, tt.legacyAnnotation)
			if !tt.expectError && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
