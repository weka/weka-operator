package wekacontainer

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// vd is a shorthand VirtualDrive builder for table tests below.
func vd(vuid, puid, serial string) weka.VirtualDrive {
	return weka.VirtualDrive{VirtualUUID: vuid, PhysicalUUID: puid, Serial: serial}
}

func TestFilterVirtualDrives(t *testing.T) {
	all := []weka.VirtualDrive{
		vd("v1", "p1", "s1"),
		vd("v2", "p1", "s2"),
		vd("v3", "p2", "s3"),
	}

	tests := []struct {
		name        string
		vds         []weka.VirtualDrive
		match       func(weka.VirtualDrive) bool
		wantKept    []weka.VirtualDrive
		wantChanged bool
	}{
		{
			name:        "nothing matches",
			vds:         all,
			match:       func(weka.VirtualDrive) bool { return false },
			wantKept:    all,
			wantChanged: false,
		},
		{
			name:        "everything matches",
			vds:         all,
			match:       func(weka.VirtualDrive) bool { return true },
			wantKept:    []weka.VirtualDrive{},
			wantChanged: true,
		},
		{
			name: "some match",
			vds:  all,
			match: func(v weka.VirtualDrive) bool {
				return v.VirtualUUID == "v2"
			},
			wantKept:    []weka.VirtualDrive{vd("v1", "p1", "s1"), vd("v3", "p2", "s3")},
			wantChanged: true,
		},
		{
			name:        "empty input",
			vds:         []weka.VirtualDrive{},
			match:       func(weka.VirtualDrive) bool { return true },
			wantKept:    []weka.VirtualDrive{},
			wantChanged: false,
		},
		{
			name:        "nil input",
			vds:         nil,
			match:       func(weka.VirtualDrive) bool { return true },
			wantKept:    []weka.VirtualDrive{},
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, changed := filterVirtualDrives(tt.vds, tt.match)
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if len(kept) != len(tt.wantKept) {
				t.Fatalf("kept = %v, want %v", kept, tt.wantKept)
			}
			for i := range kept {
				if kept[i] != tt.wantKept[i] {
					t.Fatalf("kept[%d] = %v, want %v", i, kept[i], tt.wantKept[i])
				}
			}
		})
	}
}

// newAllocationTestLoop builds a containerReconcilerLoop backed by a fake client so
// deallocate* functions can exercise their real r.Status().Update(ctx, container) call.
func newAllocationTestLoop(t *testing.T, container *weka.WekaContainer) *containerReconcilerLoop {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka to scheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&weka.WekaContainer{}).
		WithObjects(container).
		Build()
	return &containerReconcilerLoop{Client: c, container: container}
}

func newTestContainer(vds []weka.VirtualDrive, drives []string) *weka.WekaContainer {
	return &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
		Status: weka.WekaContainerStatus{
			Allocations: &weka.ContainerAllocations{
				VirtualDrives: vds,
				Drives:        drives,
			},
		},
	}
}

// TestDeallocateDrivesByVirtualUuids_KeepsSiblingsOnSamePhysicalDrive is the whole point of the
// feature: removing one VID (e.g. during a single-virtual-drive replacement) must not touch other
// VIDs that happen to share the same PhysicalUUID.
func TestDeallocateDrivesByVirtualUuids_KeepsSiblingsOnSamePhysicalDrive(t *testing.T) {
	container := newTestContainer([]weka.VirtualDrive{
		vd("v1", "p1", "s1"),
		vd("v2", "p1", "s2"), // shares PhysicalUUID with v1, must survive
		vd("v3", "p2", "s3"),
	}, nil)
	r := newAllocationTestLoop(t, container)

	if err := r.deallocateDrivesByVirtualUuids(context.Background(), []string{"v1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(container), got); err != nil {
		t.Fatalf("failed to get container: %v", err)
	}

	want := []weka.VirtualDrive{vd("v2", "p1", "s2"), vd("v3", "p2", "s3")}
	if len(got.Status.Allocations.VirtualDrives) != len(want) {
		t.Fatalf("VirtualDrives = %v, want %v", got.Status.Allocations.VirtualDrives, want)
	}
	for i := range want {
		if got.Status.Allocations.VirtualDrives[i] != want[i] {
			t.Fatalf("VirtualDrives[%d] = %v, want %v", i, got.Status.Allocations.VirtualDrives[i], want[i])
		}
	}
}

func TestDeallocateDrivesByVirtualUuids_NoopWhenNoneMatch(t *testing.T) {
	original := []weka.VirtualDrive{vd("v1", "p1", "s1"), vd("v2", "p2", "s2")}
	container := newTestContainer(original, nil)
	r := newAllocationTestLoop(t, container)

	if err := r.deallocateDrivesByVirtualUuids(context.Background(), []string{"does-not-exist"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(container), got); err != nil {
		t.Fatalf("failed to get container: %v", err)
	}
	if len(got.Status.Allocations.VirtualDrives) != len(original) {
		t.Fatalf("VirtualDrives = %v, want untouched %v", got.Status.Allocations.VirtualDrives, original)
	}
}

func TestDeallocateDrivesByVirtualUuids_MultipleVidsInOneCall(t *testing.T) {
	container := newTestContainer([]weka.VirtualDrive{
		vd("v1", "p1", "s1"),
		vd("v2", "p1", "s2"),
		vd("v3", "p2", "s3"),
		vd("v4", "p3", "s4"),
	}, nil)
	r := newAllocationTestLoop(t, container)

	if err := r.deallocateDrivesByVirtualUuids(context.Background(), []string{"v1", "v3"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(container), got); err != nil {
		t.Fatalf("failed to get container: %v", err)
	}

	want := []weka.VirtualDrive{vd("v2", "p1", "s2"), vd("v4", "p3", "s4")}
	if len(got.Status.Allocations.VirtualDrives) != len(want) {
		t.Fatalf("VirtualDrives = %v, want %v", got.Status.Allocations.VirtualDrives, want)
	}
	for i := range want {
		if got.Status.Allocations.VirtualDrives[i] != want[i] {
			t.Fatalf("VirtualDrives[%d] = %v, want %v", i, got.Status.Allocations.VirtualDrives[i], want[i])
		}
	}
}

// TestDeallocateDrivesBySerials_RegularDriveOnlyLeavesVirtualDrivesNil guards a subtle API-shape
// regression: when only Allocations.Drives (regular-drive-mode) matches, VirtualDrives must stay
// nil rather than becoming an allocated-but-empty []weka.VirtualDrive{} — the two serialize
// differently (absent vs "virtualDrives":[]) on the status subresource.
func TestDeallocateDrivesBySerials_RegularDriveOnlyLeavesVirtualDrivesNil(t *testing.T) {
	container := newTestContainer(nil, []string{"blocked-serial", "keep-me"})
	r := newAllocationTestLoop(t, container)

	if err := r.deallocateDrivesBySerials(context.Background(), []string{"blocked-serial"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(container), got); err != nil {
		t.Fatalf("failed to get container: %v", err)
	}

	if got.Status.Allocations.VirtualDrives != nil {
		t.Fatalf("VirtualDrives = %#v, want nil (untouched)", got.Status.Allocations.VirtualDrives)
	}
	if len(got.Status.Allocations.Drives) != 1 || got.Status.Allocations.Drives[0] != "keep-me" {
		t.Fatalf("Drives = %v, want [keep-me]", got.Status.Allocations.Drives)
	}
}
