package domain

import (
	"testing"
)

func TestReadDriveAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		fullAnnotation  string
		expectedEntries []DriveEntry
		expectError     bool
		errorContains   string
	}{
		{
			name:            "empty annotation returns nil",
			fullAnnotation:  "",
			expectedEntries: nil,
			expectError:     false,
		},
		{
			name:           "full annotation with capacity",
			fullAnnotation: `[{"serial":"SN001","capacity_gib":1024},{"serial":"SN002","capacity_gib":2048}]`,
			expectedEntries: []DriveEntry{
				{Serial: "SN001", CapacityGiB: 1024},
				{Serial: "SN002", CapacityGiB: 2048},
			},
			expectError: false,
		},
		{
			name:            "full annotation empty array",
			fullAnnotation:  `[]`,
			expectedEntries: []DriveEntry{},
			expectError:     false,
		},
		{
			name:           "zero capacity entries are filtered out",
			fullAnnotation: `[{"serial":"ZERO-CAP","capacity_gib":0},{"serial":"GOOD","capacity_gib":512}]`,
			expectedEntries: []DriveEntry{
				{Serial: "GOOD", CapacityGiB: 512},
			},
			expectError: false,
		},
		{
			name:            "all zero capacity returns empty slice",
			fullAnnotation:  `[{"serial":"ZERO-CAP","capacity_gib":0}]`,
			expectedEntries: []DriveEntry{},
			expectError:     false,
		},
		{
			name:            "invalid json returns error",
			fullAnnotation:  `{invalid json}`,
			expectedEntries: nil,
			expectError:     true,
			errorContains:   "failed to parse weka-full-drives",
		},
		{
			name:            "unclosed bracket returns error",
			fullAnnotation:  `[{"serial":"test"`,
			expectedEntries: nil,
			expectError:     true,
			errorContains:   "failed to parse weka-full-drives",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := ReadDriveAnnotations(tt.fullAnnotation)

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

func TestReadAnnotatedDriveSerials(t *testing.T) {
	tests := []struct {
		name             string
		fullAnnotation   string
		legacyAnnotation string
		expectedSerials  []string
		expectError      bool
		errorContains    string
	}{
		{
			name:             "both empty",
			fullAnnotation:   "",
			legacyAnnotation: "",
			expectedSerials:  []string{},
			expectError:      false,
		},
		{
			name:             "full annotation only",
			fullAnnotation:   `[{"serial":"SN001","capacity_gib":1024},{"serial":"SN002","capacity_gib":2048}]`,
			legacyAnnotation: "",
			expectedSerials:  []string{"SN001", "SN002"},
			expectError:      false,
		},
		{
			name:             "legacy annotation only",
			fullAnnotation:   "",
			legacyAnnotation: `["SN001","SN002"]`,
			expectedSerials:  []string{"SN001", "SN002"},
			expectError:      false,
		},
		{
			name:             "both present - union deduplicated",
			fullAnnotation:   `[{"serial":"FULL-SN001","capacity_gib":1024}]`,
			legacyAnnotation: `["FULL-SN001","LEGACY-SN002"]`,
			expectedSerials:  []string{"FULL-SN001", "LEGACY-SN002"},
			expectError:      false,
		},
		{
			name:             "legacy with empty string entries skipped",
			fullAnnotation:   "",
			legacyAnnotation: `["SN001","","SN002",""]`,
			expectedSerials:  []string{"SN001", "SN002"},
			expectError:      false,
		},
		{
			name:             "legacy empty array",
			fullAnnotation:   "",
			legacyAnnotation: `[]`,
			expectedSerials:  []string{},
			expectError:      false,
		},
		{
			name:             "full annotation invalid json",
			fullAnnotation:   `{invalid json}`,
			legacyAnnotation: "",
			expectError:      true,
			errorContains:    "failed to parse weka-full-drives",
		},
		{
			name:             "legacy annotation invalid json",
			fullAnnotation:   "",
			legacyAnnotation: `["unclosed`,
			expectError:      true,
			errorContains:    "failed to parse weka-drives",
		},
		{
			name:             "full annotation with zero capacity serial still included",
			fullAnnotation:   `[{"serial":"ZERO-CAP","capacity_gib":0}]`,
			legacyAnnotation: "",
			expectedSerials:  []string{"ZERO-CAP"},
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serials, err := ReadAnnotatedDriveSerials(tt.fullAnnotation, tt.legacyAnnotation)

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

			if !deepEqualStrings(serials, tt.expectedSerials) {
				t.Errorf("serials mismatch\nexpected: %#v\ngot:      %#v", tt.expectedSerials, serials)
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
