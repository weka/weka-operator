package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/consts"
)

func TestApplyDriveTypeOverrides_ModelMatching(t *testing.T) {
	tests := []struct {
		name        string
		drives      []SharedDriveInfo
		rules       []v1alpha1.DriveTypeOverrideRule
		wantTypes   []string
		wantChanged int
	}{
		{
			name: "exact model match changes type",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
		},
		{
			name: "model match is case-insensitive",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "SAMSUNG PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "samsung pm1733", Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
		},
		{
			name: "model match trims surrounding whitespace",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "  Samsung PM1733  ", Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
		},
		{
			name: "model mismatch leaves type unchanged",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Intel P5800X", Type: "QLC"},
			},
			wantTypes:   []string{"TLC"},
			wantChanged: 0,
		},
		{
			// Back-compat: every drive annotated before SharedDriveInfo gained Model has an
			// empty Model. A model rule must never match those, or a single override would
			// reclassify a node's entire legacy drive set.
			name: "model rule does not match a drive with no recorded model",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
			},
			wantTypes:   []string{"TLC"},
			wantChanged: 0,
		},
		{
			// Regression: a whitespace-only Model trims to "" and used to compare equal to a
			// drive with no recorded model, matching every legacy drive. It passes CEL
			// validation (size("   ") > 0), so it is reachable through the API.
			name: "whitespace-only model rule matches nothing, not every empty-model drive",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "", Type: "TLC"},
				{Serial: "SN2", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "   ", Type: "QLC"},
			},
			wantTypes:   []string{"TLC", "TLC"},
			wantChanged: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, _ := ApplyDriveTypeOverrides(tt.drives, tt.rules)
			assertDriveTypes(t, out, tt.wantTypes)
			if changed != tt.wantChanged {
				t.Errorf("changed = %d, want %d", changed, tt.wantChanged)
			}
		})
	}
}

func TestApplyDriveTypeOverrides_CapacityMatching(t *testing.T) {
	tests := []struct {
		name        string
		drives      []SharedDriveInfo
		rules       []v1alpha1.DriveTypeOverrideRule
		wantTypes   []string
		wantChanged int
	}{
		{
			name: "exact capacity match changes type",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 7000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{CapacityGiB: 7000, Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
		},
		{
			name: "capacity mismatch leaves type unchanged",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 7000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{CapacityGiB: 4000, Type: "QLC"},
			},
			wantTypes:   []string{"TLC"},
			wantChanged: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, _ := ApplyDriveTypeOverrides(tt.drives, tt.rules)
			assertDriveTypes(t, out, tt.wantTypes)
			if changed != tt.wantChanged {
				t.Errorf("changed = %d, want %d", changed, tt.wantChanged)
			}
		})
	}
}

func TestApplyDriveTypeOverrides_CombinedAndEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		drives        []SharedDriveInfo
		rules         []v1alpha1.DriveTypeOverrideRule
		wantTypes     []string
		wantChanged   int
		wantUnmatched []int
	}{
		{
			name: "rule with both model and capacity requires both to match",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", CapacityGiB: 4000, Type: "QLC"},
			},
			wantTypes:     []string{"TLC"},
			wantChanged:   0,
			wantUnmatched: []int{0},
		},
		{
			name: "rule with both model and capacity matches when both match",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", CapacityGiB: 7000, Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
		},
		{
			name: "first matching rule wins, shadowed rule still counts as matched",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
				{Model: "Samsung PM1733", Type: "TLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 1,
			// Rule 1 also matches SN1 (it is shadowed by rule 0, which wins the Type), so it
			// must not be reported as unmatched: doing so would falsely tell an admin the rule
			// is dead when it is merely shadowed.
			wantUnmatched: nil,
		},
		{
			name: "rule with invalid type is ignored and reported unmatched",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "SLC"},
			},
			wantTypes:     []string{"TLC"},
			wantChanged:   0,
			wantUnmatched: []int{0},
		},
		{
			name: "rule with neither model nor capacity never matches",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Type: "QLC"},
			},
			wantTypes:     []string{"TLC"},
			wantChanged:   0,
			wantUnmatched: []int{0},
		},
		{
			name: "matched rule that already equals drive type does not count as changed",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "QLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
			},
			wantTypes:   []string{"QLC"},
			wantChanged: 0,
		},
		{
			name: "unmatched rule indexes reported correctly among mixed rules",
			drives: []SharedDriveInfo{
				{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
				{Serial: "SN2", CapacityGiB: 4000, Type: "TLC"},
			},
			rules: []v1alpha1.DriveTypeOverrideRule{
				{Model: "Samsung PM1733", Type: "QLC"},
				{Model: "Nonexistent Model", Type: "QLC"},
				{CapacityGiB: 4000, Type: "QLC"},
				{CapacityGiB: 99999, Type: "QLC"},
			},
			wantTypes:     []string{"QLC", "QLC"},
			wantChanged:   2,
			wantUnmatched: []int{1, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, unmatched := ApplyDriveTypeOverrides(tt.drives, tt.rules)
			assertDriveTypes(t, out, tt.wantTypes)
			if changed != tt.wantChanged {
				t.Errorf("changed = %d, want %d", changed, tt.wantChanged)
			}
			if !slices.Equal(unmatched, tt.wantUnmatched) {
				t.Errorf("unmatchedRules = %#v, want %#v", unmatched, tt.wantUnmatched)
			}
		})
	}
}

// TestApplyDriveTypeOverrides_DoesNotMutateInput proves that the returned slice is an
// independent copy: mutating out[i].Type must never be observed through the caller's
// original drives slice.
func TestApplyDriveTypeOverrides_DoesNotMutateInput(t *testing.T) {
	drives := []SharedDriveInfo{
		{Serial: "SN1", Model: "Samsung PM1733", Type: "TLC"},
	}
	rules := []v1alpha1.DriveTypeOverrideRule{
		{Model: "Samsung PM1733", Type: "QLC"},
	}

	out, changed, _ := ApplyDriveTypeOverrides(drives, rules)

	if changed != 1 {
		t.Fatalf("expected 1 change, got %d", changed)
	}
	if out[0].Type != "QLC" {
		t.Fatalf("expected returned slice to have Type QLC, got %q", out[0].Type)
	}
	if drives[0].Type != "TLC" {
		t.Errorf("input slice was mutated: drives[0].Type = %q, want unchanged %q", drives[0].Type, "TLC")
	}
}

// TestApplyDriveTypeOverrides_NilOrEmptyRules proves that both a nil and an empty (non-nil)
// rules slice are a complete no-op: drives come back unchanged, changed is 0, and no
// unmatched rules are reported (there are none to report).
func TestApplyDriveTypeOverrides_NilOrEmptyRules(t *testing.T) {
	drives := []SharedDriveInfo{
		{Serial: "SN1", Model: "Samsung PM1733", CapacityGiB: 7000, Type: "TLC"},
		{Serial: "SN2", Model: "", CapacityGiB: 4000, Type: "QLC"},
	}

	t.Run("nil rules", func(t *testing.T) {
		out, changed, unmatched := ApplyDriveTypeOverrides(drives, nil)
		assertDriveTypes(t, out, []string{"TLC", "QLC"})
		if changed != 0 {
			t.Errorf("changed = %d, want 0", changed)
		}
		if len(unmatched) != 0 {
			t.Errorf("unmatchedRules = %#v, want empty", unmatched)
		}
	})

	t.Run("empty non-nil rules", func(t *testing.T) {
		out, changed, unmatched := ApplyDriveTypeOverrides(drives, []v1alpha1.DriveTypeOverrideRule{})
		assertDriveTypes(t, out, []string{"TLC", "QLC"})
		if changed != 0 {
			t.Errorf("changed = %d, want 0", changed)
		}
		if len(unmatched) != 0 {
			t.Errorf("unmatchedRules = %#v, want empty", unmatched)
		}
	})
}

func TestReadWriteDriveTypeOverrides(t *testing.T) {
	t.Run("read returns nil when annotation absent", func(t *testing.T) {
		node := &corev1.Node{}
		rules, err := ReadDriveTypeOverrides(node)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if rules != nil {
			t.Errorf("expected nil rules, got %#v", rules)
		}
	})

	t.Run("read returns nil when annotation is empty string", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					consts.AnnotationDriveTypeOverrides: "",
				},
			},
		}
		rules, err := ReadDriveTypeOverrides(node)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if rules != nil {
			t.Errorf("expected nil rules, got %#v", rules)
		}
	})

	t.Run("read returns error on malformed json", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					consts.AnnotationDriveTypeOverrides: `{not valid json`,
				},
			},
		}
		rules, err := ReadDriveTypeOverrides(node)
		if err == nil {
			t.Fatalf("expected error, got nil (rules: %#v)", rules)
		}
		if !contains(err.Error(), "failed to parse drive-type-overrides") {
			t.Errorf("error message %q does not contain expected substring", err.Error())
		}
	})

	t.Run("write then read round-trips rules", func(t *testing.T) {
		node := &corev1.Node{}
		want := []v1alpha1.DriveTypeOverrideRule{
			{Model: "Samsung PM1733", Type: "QLC"},
			{CapacityGiB: 4000, Type: "TLC"},
		}

		if err := WriteDriveTypeOverrides(node, want); err != nil {
			t.Fatalf("write failed: %v", err)
		}

		got, err := ReadDriveTypeOverrides(node)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("round-tripped rules mismatch\nwant: %#v\ngot:  %#v", want, got)
		}
	})

	t.Run("write initializes nil Annotations map without panic", func(t *testing.T) {
		node := &corev1.Node{}
		rules := []v1alpha1.DriveTypeOverrideRule{
			{Model: "Samsung PM1733", Type: "QLC"},
		}

		if err := WriteDriveTypeOverrides(node, rules); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if node.Annotations == nil {
			t.Fatalf("expected Annotations map to be initialized")
		}
		if _, ok := node.Annotations[consts.AnnotationDriveTypeOverrides]; !ok {
			t.Errorf("expected annotation to be set")
		}
	})

	t.Run("write with empty rules deletes annotation", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					consts.AnnotationDriveTypeOverrides: `[{"model":"Samsung PM1733","type":"QLC"}]`,
				},
			},
		}

		if err := WriteDriveTypeOverrides(node, []v1alpha1.DriveTypeOverrideRule{}); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if _, ok := node.Annotations[consts.AnnotationDriveTypeOverrides]; ok {
			t.Errorf("expected annotation to be deleted")
		}
	})

	t.Run("write with nil rules deletes annotation", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					consts.AnnotationDriveTypeOverrides: `[{"model":"Samsung PM1733","type":"QLC"}]`,
				},
			},
		}

		if err := WriteDriveTypeOverrides(node, nil); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if _, ok := node.Annotations[consts.AnnotationDriveTypeOverrides]; ok {
			t.Errorf("expected annotation to be deleted")
		}
	})
}

func TestMergeSharedDriveInfo(t *testing.T) {
	existing := SharedDriveInfo{
		PhysicalUUID: "existing-uuid",
		Serial:       "existing-serial",
		CapacityGiB:  1000,
		Type:         "QLC",
		Model:        "Existing Model",
	}

	tests := []struct {
		name     string
		existing SharedDriveInfo
		incoming SharedDriveInfo
		want     SharedDriveInfo
	}{
		{
			name:     "incoming full data overrides existing entirely",
			existing: existing,
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "New Model",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "New Model",
			},
		},
		{
			name:     "empty incoming PhysicalUUID preserves existing",
			existing: existing,
			incoming: SharedDriveInfo{
				Serial:      "new-serial",
				CapacityGiB: 2000,
				Type:        "TLC",
				Model:       "New Model",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "existing-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "New Model",
			},
		},
		{
			name:     "empty incoming Serial preserves existing",
			existing: existing,
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "New Model",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "existing-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "New Model",
			},
		},
		{
			name:     "zero incoming CapacityGiB preserves existing",
			existing: existing,
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				Type:         "TLC",
				Model:        "New Model",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  1000,
				Type:         "TLC",
				Model:        "New Model",
			},
		},
		{
			name:     "empty incoming Type preserves existing",
			existing: existing,
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Model:        "New Model",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "QLC",
				Model:        "New Model",
			},
		},
		{
			name:     "empty incoming Model preserves existing (does not disarm model-based override rules)",
			existing: existing,
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
				Model:        "Existing Model",
			},
		},
		{
			name:     "incoming with only Serial set preserves all other existing fields",
			existing: existing,
			incoming: SharedDriveInfo{
				Serial: "new-serial",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "existing-uuid",
				Serial:       "new-serial",
				CapacityGiB:  1000,
				Type:         "QLC",
				Model:        "Existing Model",
			},
		},
		{
			name: "both existing and incoming empty Model stays empty",
			existing: SharedDriveInfo{
				PhysicalUUID: "existing-uuid",
				Serial:       "existing-serial",
				CapacityGiB:  1000,
				Type:         "QLC",
			},
			incoming: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
			},
			want: SharedDriveInfo{
				PhysicalUUID: "new-uuid",
				Serial:       "new-serial",
				CapacityGiB:  2000,
				Type:         "TLC",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeSharedDriveInfo(tt.existing, tt.incoming)
			if got != tt.want {
				t.Errorf("MergeSharedDriveInfo() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSumSharedDriveCapacityByType(t *testing.T) {
	tests := []struct {
		name             string
		drives           []SharedDriveInfo
		blockedUUIDs     []string
		blockedSerials   []string
		wantTLC, wantQLC int64
	}{
		{
			name:    "empty drives list sums to zero",
			drives:  nil,
			wantTLC: 0,
			wantQLC: 0,
		},
		{
			name: "mixed TLC and QLC drives sum correctly",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
				{Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
				{Serial: "SN3", CapacityGiB: 500, Type: "TLC"},
			},
			wantTLC: 1500,
			wantQLC: 2000,
		},
		{
			name: "empty type falls back to TLC",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 1000, Type: ""},
			},
			wantTLC: 1000,
			wantQLC: 0,
		},
		{
			name: "unknown type falls back to TLC",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 1000, Type: "SLC"},
			},
			wantTLC: 1000,
			wantQLC: 0,
		},
		{
			name: "drive blocked by physical uuid excluded",
			drives: []SharedDriveInfo{
				{PhysicalUUID: "uuid-1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
				{PhysicalUUID: "uuid-2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
			},
			blockedUUIDs: []string{"uuid-1"},
			wantTLC:      0,
			wantQLC:      2000,
		},
		{
			name: "drive blocked by serial excluded",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
				{Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
			},
			blockedSerials: []string{"SN2"},
			wantTLC:        1000,
			wantQLC:        0,
		},
		{
			name: "drive blocked by both uuid and serial is excluded once, not double-subtracted",
			drives: []SharedDriveInfo{
				{PhysicalUUID: "uuid-1", Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
				{PhysicalUUID: "uuid-2", Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
			},
			blockedUUIDs:   []string{"uuid-1"},
			blockedSerials: []string{"SN1"},
			wantTLC:        0,
			wantQLC:        2000,
		},
		{
			name: "all drives blocked sums to zero",
			drives: []SharedDriveInfo{
				{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
				{Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
			},
			blockedSerials: []string{"SN1", "SN2"},
			wantTLC:        0,
			wantQLC:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tlc, qlc := SumSharedDriveCapacityByType(tt.drives, tt.blockedUUIDs, tt.blockedSerials)
			if tlc != tt.wantTLC {
				t.Errorf("tlcGiB = %d, want %d", tlc, tt.wantTLC)
			}
			if qlc != tt.wantQLC {
				t.Errorf("qlcGiB = %d, want %d", qlc, tt.wantQLC)
			}
		})
	}
}

// TestSumSharedDriveCapacityByType_LegacyAnnotationBackCompat proves that a shared-drive
// annotation written before the "type" field existed (no "type" key at all in the JSON)
// still unmarshals to an empty Type and is counted as TLC capacity, not dropped to zero.
func TestSumSharedDriveCapacityByType_LegacyAnnotationBackCompat(t *testing.T) {
	raw := `[{"physical_uuid":"uuid-1","serial":"SN1","capacity_gib":1000}]`

	var drives []SharedDriveInfo
	if err := json.Unmarshal([]byte(raw), &drives); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if len(drives) != 1 {
		t.Fatalf("expected 1 drive, got %d", len(drives))
	}
	if drives[0].Type != "" {
		t.Fatalf("expected Type to be empty for legacy annotation without a type field, got %q", drives[0].Type)
	}

	tlc, qlc := SumSharedDriveCapacityByType(drives, nil, nil)
	if tlc != 1000 {
		t.Errorf("expected legacy drive without type field to count as 1000 GiB TLC, got tlc=%d qlc=%d", tlc, qlc)
	}
	if qlc != 0 {
		t.Errorf("expected qlc capacity 0 for legacy drive, got %d", qlc)
	}

	// A model rule must not match it (no recorded model), but a capacity rule must.
	if out, changed, _ := ApplyDriveTypeOverrides(drives, []v1alpha1.DriveTypeOverrideRule{
		{Model: "Samsung PM1733", Type: "QLC"},
	}); changed != 0 || out[0].Type != "" {
		t.Errorf("model rule must not match a legacy drive with no model: changed=%d type=%q", changed, out[0].Type)
	}

	overridden, changed, _ := ApplyDriveTypeOverrides(drives, []v1alpha1.DriveTypeOverrideRule{
		{CapacityGiB: 1000, Type: "QLC"},
	})
	if changed != 1 || overridden[0].Type != "QLC" {
		t.Fatalf("capacity rule should match a legacy drive: changed=%d type=%q", changed, overridden[0].Type)
	}

	// Round-tripping must not introduce a "model" key while Model is still empty — the field
	// is omitempty specifically so existing annotations don't grow on rewrite.
	roundTripped, err := json.Marshal(overridden)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if strings.Contains(string(roundTripped), `"model"`) {
		t.Errorf("re-marshalled legacy drive must not gain a model key, got %s", roundTripped)
	}
}

func TestSetSharedDriveCapacityResources(t *testing.T) {
	t.Run("recomputes both TLC and QLC resources", func(t *testing.T) {
		node := &corev1.Node{
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{},
				Allocatable: corev1.ResourceList{},
			},
		}
		drives := []SharedDriveInfo{
			{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
			{Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
		}

		SetSharedDriveCapacityResources(node, drives, nil, nil)

		assertQuantity(t, node.Status.Capacity, consts.ResourceSharedDrivesCapacity, 1000)
		assertQuantity(t, node.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 1000)
		assertQuantity(t, node.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 2000)
		assertQuantity(t, node.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 2000)
	})

	t.Run("stale value is overwritten to zero, not left stale", func(t *testing.T) {
		node := &corev1.Node{
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					consts.ResourcesSharedDrivesCapacityQLC: *resource.NewQuantity(5000, resource.DecimalSI),
				},
				Allocatable: corev1.ResourceList{
					consts.ResourcesSharedDrivesCapacityQLC: *resource.NewQuantity(5000, resource.DecimalSI),
				},
			},
		}
		// No QLC drives remain (e.g. all were reclassified to TLC by overrides, or unplugged).
		drives := []SharedDriveInfo{
			{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		}

		SetSharedDriveCapacityResources(node, drives, nil, nil)

		assertQuantity(t, node.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 0)
		assertQuantity(t, node.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 0)
		assertQuantity(t, node.Status.Capacity, consts.ResourceSharedDrivesCapacity, 1000)
	})

	t.Run("all-QLC drives writes TLC resource as 0, not omitted", func(t *testing.T) {
		node := &corev1.Node{
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{},
				Allocatable: corev1.ResourceList{},
			},
		}
		drives := []SharedDriveInfo{
			{Serial: "SN1", CapacityGiB: 3000, Type: "QLC"},
		}

		SetSharedDriveCapacityResources(node, drives, nil, nil)

		assertQuantity(t, node.Status.Capacity, consts.ResourceSharedDrivesCapacity, 0)
		assertQuantity(t, node.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 0)
		assertQuantity(t, node.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 3000)
		assertQuantity(t, node.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 3000)
	})
}

// assertDriveTypes checks that out has the expected Type for each drive, in order.
func assertDriveTypes(t *testing.T, out []SharedDriveInfo, wantTypes []string) {
	t.Helper()
	if len(out) != len(wantTypes) {
		t.Fatalf("got %d drives, want %d", len(out), len(wantTypes))
	}
	for i, want := range wantTypes {
		if out[i].Type != want {
			t.Errorf("drive %d: Type = %q, want %q", i, out[i].Type, want)
		}
	}
}

// assertQuantity checks that resources[name] equals want when read as an int64 value.
func assertQuantity(t *testing.T, resources corev1.ResourceList, name string, want int64) {
	t.Helper()
	q, ok := resources[corev1.ResourceName(name)]
	if !ok {
		t.Fatalf("resource %q not set", name)
	}
	if q.Value() != want {
		t.Errorf("resource %q = %d, want %d", name, q.Value(), want)
	}
}

// TestSetSharedDriveCapacityResources_NilMaps proves the helper does not panic on a Node whose
// Status.Capacity/Allocatable maps are nil. Real API-server Nodes always populate them, but a
// nil-map assignment would panic the controller.
func TestSetSharedDriveCapacityResources_NilMaps(t *testing.T) {
	node := &corev1.Node{}

	SetSharedDriveCapacityResources(node, []SharedDriveInfo{
		{Serial: "SN1", CapacityGiB: 1000, Type: "TLC"},
		{Serial: "SN2", CapacityGiB: 2000, Type: "QLC"},
	}, nil, nil)

	assertQuantity(t, node.Status.Capacity, consts.ResourceSharedDrivesCapacity, 1000)
	assertQuantity(t, node.Status.Allocatable, consts.ResourceSharedDrivesCapacity, 1000)
	assertQuantity(t, node.Status.Capacity, consts.ResourcesSharedDrivesCapacityQLC, 2000)
	assertQuantity(t, node.Status.Allocatable, consts.ResourcesSharedDrivesCapacityQLC, 2000)
}
