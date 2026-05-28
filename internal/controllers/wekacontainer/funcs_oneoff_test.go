package wekacontainer

import (
	"slices"
	"testing"

	"github.com/weka/weka-operator/internal/controllers/operations"
)

func TestAppendMissingDrivesToBlocked(t *testing.T) {
	t.Run("defers when kernel view is incomplete", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: "B1"}},
			KernelViewComplete: false,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"X"})
		if len(missing) != 0 {
			t.Fatalf("expected defer (no additions), got %v", missing)
		}
		if !slices.Equal(blocked, []string{"X"}) {
			t.Fatalf("expected blocked unchanged [X], got %v", blocked)
		}
	})

	t.Run("blocks missing serials when kernel view is complete", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives: []operations.DriveRawInfo{
				{SerialId: "B1"},
				{SerialId: "B2"},
			},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2", "B1"}, op, nil)
		slices.Sort(missing)
		if want := []string{"A1", "A2"}; !slices.Equal(missing, want) {
			t.Fatalf("expected missing=%v, got %v", want, missing)
		}
		slices.Sort(blocked)
		if want := []string{"A1", "A2"}; !slices.Equal(blocked, want) {
			t.Fatalf("expected blocked=%v, got %v", want, blocked)
		}
	})

	t.Run("no-op when all annotation serials are kernel-visible", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives: []operations.DriveRawInfo{
				{SerialId: "A1"},
				{SerialId: "A2"},
			},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"X"})
		if len(missing) != 0 {
			t.Fatalf("expected no additions, got %v", missing)
		}
		if len(blocked) != 1 || blocked[0] != "X" {
			t.Fatalf("expected blocked=[X], got %v", blocked)
		}
	})

	t.Run("dedupes against existing blocked serials", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: "B1"}},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"A1", "A2"}, op, []string{"A1"})
		if !slices.Equal(missing, []string{"A2"}) {
			t.Fatalf("expected missing=[A2], got %v", missing)
		}
		slices.Sort(blocked)
		if want := []string{"A1", "A2"}; !slices.Equal(blocked, want) {
			t.Fatalf("expected blocked=%v, got %v", want, blocked)
		}
	})

	t.Run("ignores empty serials in input", func(t *testing.T) {
		op := &operations.DriveNodeResults{
			RawDrives:          []operations.DriveRawInfo{{SerialId: ""}, {SerialId: "B1"}},
			KernelViewComplete: true,
		}
		blocked, missing := appendMissingDrivesToBlocked([]string{"", "A1"}, op, nil)
		if !slices.Equal(missing, []string{"A1"}) {
			t.Fatalf("expected missing=[A1], got %v", missing)
		}
		if !slices.Equal(blocked, []string{"A1"}) {
			t.Fatalf("expected blocked=[A1], got %v", blocked)
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		op := &operations.DriveNodeResults{}
		blocked, missing := appendMissingDrivesToBlocked(nil, op, nil)
		if len(missing) != 0 || len(blocked) != 0 {
			t.Fatalf("expected empty outputs, got blocked=%v missing=%v", blocked, missing)
		}
	})
}
