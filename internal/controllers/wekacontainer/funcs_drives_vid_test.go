package wekacontainer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/consts"
)

func nodeWithAnnotations(annotations map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Annotations: annotations}}
}

func TestNodeBlockedListReaders(t *testing.T) {
	node := nodeWithAnnotations(map[string]string{
		consts.AnnotationBlockedDrives:              `["SN1","SN2"]`,
		consts.AnnotationBlockedDrivesPhysicalUuids: `["phys-a"]`,
		consts.AnnotationBlockedDrivesVirtualUuids:  `["vid-1","vid-2","vid-3"]`,
	})
	r := &containerReconcilerLoop{node: node}
	ctx := context.Background()

	// Each reader must pick up its own annotation and no other.
	tests := []struct {
		name string
		read func(context.Context) ([]string, error)
		want []string
	}{
		{"serials", r.getNodeBlockedDriveSerials, []string{"SN1", "SN2"}},
		{"physical uuids", r.getNodeBlockedDriveUuids, []string{"phys-a"}},
		{"virtual uuids", r.getNodeBlockedDriveVirtualUuids, []string{"vid-1", "vid-2", "vid-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.read(ctx)
			if err != nil {
				t.Fatalf("read returned error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got %#v, want %#v", got, tt.want)
					break
				}
			}
		})
	}
}

func TestNodeBlockedListReaders_AbsentMalformedAndNilNode(t *testing.T) {
	ctx := context.Background()

	empty := &containerReconcilerLoop{node: nodeWithAnnotations(map[string]string{})}
	got, err := empty.getNodeBlockedDriveVirtualUuids(ctx)
	if err != nil {
		t.Fatalf("absent annotation returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("absent annotation gave %#v, want empty", got)
	}

	malformed := &containerReconcilerLoop{node: nodeWithAnnotations(map[string]string{
		consts.AnnotationBlockedDrivesVirtualUuids: `{not json`,
	})}
	if _, err := malformed.getNodeBlockedDriveVirtualUuids(ctx); err == nil {
		t.Errorf("malformed annotation returned nil error, want a decode error")
	}

	nilNode := &containerReconcilerLoop{}
	if _, err := nilNode.getNodeBlockedDriveVirtualUuids(ctx); err == nil {
		t.Errorf("nil node returned nil error, want an error")
	}
}

// A malformed annotation must not stop drives being signed or added, so the set degrades to empty
// rather than propagating the decode error.
func TestBlockedVirtualUuidSet(t *testing.T) {
	ctx := context.Background()

	ok := &containerReconcilerLoop{node: nodeWithAnnotations(map[string]string{
		consts.AnnotationBlockedDrivesVirtualUuids: `["vid-1","vid-2"]`,
	})}
	set := ok.blockedVirtualUuidSet(ctx)
	if !set["vid-1"] || !set["vid-2"] {
		t.Errorf("set = %#v, want vid-1 and vid-2 present", set)
	}
	if set["vid-other"] {
		t.Errorf("set = %#v, want vid-other absent", set)
	}

	malformed := &containerReconcilerLoop{node: nodeWithAnnotations(map[string]string{
		consts.AnnotationBlockedDrivesVirtualUuids: `{not json`,
	})}
	if got := malformed.blockedVirtualUuidSet(ctx); len(got) != 0 {
		t.Errorf("malformed annotation gave %#v, want an empty set", got)
	}

	nilNode := &containerReconcilerLoop{}
	if got := nilNode.blockedVirtualUuidSet(ctx); len(got) != 0 {
		t.Errorf("nil node gave %#v, want an empty set", got)
	}
}

// RemoveDrivesByVirtualUuids must return before any proxy or cluster contact when there is nothing
// for this container to do. The loop here has no ExecService and no proxy wiring, so any attempt to
// reach either would panic or error rather than pass quietly.
func TestRemoveDrivesByVirtualUuids_EarlyReturnsMakeNoProxyContact(t *testing.T) {
	ctx := context.Background()

	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drives0", Namespace: "default"},
		Status: weka.WekaContainerStatus{
			Allocations: &weka.ContainerAllocations{
				VirtualDrives: []weka.VirtualDrive{vd("vid-mine", "phys-a", "SN1")},
			},
		},
	}

	tests := []struct {
		name        string
		annotations map[string]string
	}{
		{
			name:        "no blocked virtual uuids at all",
			annotations: map[string]string{},
		},
		{
			name: "blocked virtual uuids belong to another container",
			annotations: map[string]string{
				consts.AnnotationBlockedDrivesVirtualUuids: `["vid-someone-elses"]`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &containerReconcilerLoop{node: nodeWithAnnotations(tt.annotations), container: container}
			if err := r.RemoveDrivesByVirtualUuids(ctx); err != nil {
				t.Errorf("RemoveDrivesByVirtualUuids returned error: %v, want nil with no proxy contact", err)
			}
		})
	}
}

// With SkipVirtualDrivesRemoval set, a blocked VID owned by this container must not trigger the
// ssdproxy pre-flight: the loop has no proxy wiring, so resolveSSDProxy would error if reached. There
// is no ExecService in this loop either, so the cluster-side removal call it proceeds to is itself
// unreachable and panics; that panic is expected and is what proves execution got past the pre-flight
// into cluster code rather than stopping there. This only proves the pre-flight is skipped, not that
// removal fully succeeds — there is no mock here to let it.
//
// This and the paired NoOverride test below only avoid a nil-client panic inside resolveSSDProxy
// because the fixture container has no node affinity, so findSSDProxyOnNode errors before reaching
// any client; adding NodeAffinity to the fixture would exercise a different, unmocked code path.
func TestRemoveDrivesByVirtualUuids_SkipVirtualDrivesRemoval_NoProxyPreflight(t *testing.T) {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drives0", Namespace: "default"},
		Spec: weka.WekaContainerSpec{
			Overrides: &weka.WekaContainerSpecOverrides{SkipVirtualDrivesRemoval: true},
		},
		Status: weka.WekaContainerStatus{
			Allocations: &weka.ContainerAllocations{
				VirtualDrives: []weka.VirtualDrive{vd("vid-mine", "phys-a", "SN1")},
			},
		},
	}

	r := &containerReconcilerLoop{
		node: nodeWithAnnotations(map[string]string{
			consts.AnnotationBlockedDrivesVirtualUuids: `["vid-mine"]`,
		}),
		container: container,
	}

	defer func() {
		// A nil-ExecService panic from the cluster call is expected and is not a test failure; only a
		// panic that mentions ssdproxy would mean the pre-flight was reached.
		if p := recover(); p != nil {
			if msg := fmt.Sprintf("%v", p); strings.Contains(msg, "ssdproxy") {
				t.Errorf("RemoveDrivesByVirtualUuids panicked in ssdproxy code despite SkipVirtualDrivesRemoval: %v", p)
			}
		}
	}()

	err := r.RemoveDrivesByVirtualUuids(context.Background())
	if err != nil && strings.Contains(err.Error(), "ssdproxy unreachable") {
		t.Errorf("RemoveDrivesByVirtualUuids returned the ssdproxy pre-flight error despite SkipVirtualDrivesRemoval: %v", err)
	}
}

// Without the override, the same blocked VID must hit the ssdproxy pre-flight and stop there: the
// loop has no proxy wiring, so resolveSSDProxy fails cleanly (no node affinity on the fixture
// container) before any cluster contact. Paired with the SkipVirtualDrivesRemoval test above, this
// pins both branches of the pre-flight guard: override skips it, no override is blocked by it.
func TestRemoveDrivesByVirtualUuids_NoOverride_ProxyPreflightBlocks(t *testing.T) {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drives0", Namespace: "default"},
		Status: weka.WekaContainerStatus{
			Allocations: &weka.ContainerAllocations{
				VirtualDrives: []weka.VirtualDrive{vd("vid-mine", "phys-a", "SN1")},
			},
		},
	}

	r := &containerReconcilerLoop{
		node: nodeWithAnnotations(map[string]string{
			consts.AnnotationBlockedDrivesVirtualUuids: `["vid-mine"]`,
		}),
		container: container,
	}

	err := r.RemoveDrivesByVirtualUuids(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ssdproxy unreachable") {
		t.Errorf("RemoveDrivesByVirtualUuids returned %v, want an error containing \"ssdproxy unreachable\"", err)
	}
}

// A container with no allocation record yet must not be treated as owning a blocked VID.
func TestRemoveDrivesByVirtualUuids_NilAllocations(t *testing.T) {
	r := &containerReconcilerLoop{
		node: nodeWithAnnotations(map[string]string{
			consts.AnnotationBlockedDrivesVirtualUuids: `["vid-1"]`,
		}),
		container: &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "compute0", Namespace: "default"}},
	}
	if err := r.RemoveDrivesByVirtualUuids(context.Background()); err != nil {
		t.Errorf("RemoveDrivesByVirtualUuids returned error: %v, want nil", err)
	}
}
