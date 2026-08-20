package validation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	minNodesDriveLabels   = map[string]string{"afd-min-drive": "yes"}
	minNodesComputeLabels = map[string]string{"afd-min-compute": "yes"}
)

// minNodesCluster builds an auto-full-drives cluster with distinct drive and compute role selectors.
func minNodesCluster(dynamic *weka.WekaClusterTemplate) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	drive, compute := minNodesDriveLabels, minNodesComputeLabels
	c.Spec.RoleNodeSelector.Drive = &drive
	c.Spec.RoleNodeSelector.Compute = &compute
	return c
}

// plainNode builds a labelled node with no drive annotation — this policy counts labels, not signing.
func plainNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// minNodesFleet builds `drives` drive-role nodes and `computes` compute-role nodes.
func minNodesFleet(drives, computes int) []*corev1.Node {
	var nodes []*corev1.Node
	for i := 0; i < drives; i++ {
		nodes = append(nodes, plainNode(fmt.Sprintf("drive-%d", i), minNodesDriveLabels))
	}
	for i := 0; i < computes; i++ {
		nodes = append(nodes, plainNode(fmt.Sprintf("compute-%d", i), minNodesComputeLabels))
	}
	return nodes
}

// TestAutoFullDrivesMinNodes_BothRolesBelowFloor: each role is reported independently.
func TestAutoFullDrivesMinNodes_BothRolesBelowFloor(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(3, 2)...)

	errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 2 {
		t.Fatalf("expected one violation per role, got %v", errs)
	}

	var sawDrive, sawCompute bool
	for _, e := range errs {
		switch {
		case strings.Contains(e.Detail, "the drive-role nodeSelector"):
			sawDrive = true
			if e.Field != "spec.roleNodeSelector.drive" {
				t.Errorf("expected the drive selector path, got %q", e.Field)
			}
			for _, want := range []string{
				"matches 3 node(s), below the 5 drive container(s)",
				"afd-min-drive=yes",
				"one drive container per eligible node",
				// The drive side is the silent one — the message must say so.
				"Nothing reports this at runtime",
				"MinContainersNotReady",
				"Label at least 5 node(s) for spec.roleNodeSelector.drive",
			} {
				if !strings.Contains(e.Detail, want) {
					t.Errorf("drive message missing %q, got: %s", want, e.Detail)
				}
			}
		case strings.Contains(e.Detail, "the compute-role nodeSelector"):
			sawCompute = true
			if e.Field != "spec.roleNodeSelector.compute" {
				t.Errorf("expected the compute selector path, got %q", e.Field)
			}
			for _, want := range []string{
				"matches 2 node(s), below the 5 compute container(s)",
				"AutoFullDrivesInfeasible",
			} {
				if !strings.Contains(e.Detail, want) {
					t.Errorf("compute message missing %q, got: %s", want, e.Detail)
				}
			}
		}
	}
	if !sawDrive || !sawCompute {
		t.Errorf("expected both roles reported, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_RolesAreIndependent: a satisfied role must not mask a starved one.
func TestAutoFullDrivesMinNodes_RolesAreIndependent(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	t.Run("drive short, compute fine", func(t *testing.T) {
		c := fakeClientWithNodes(t, minNodesFleet(4, 6)...)
		errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
		if len(errs) != 1 || !strings.Contains(errs[0].Detail, "the drive-role nodeSelector") {
			t.Fatalf("expected only the drive violation, got %v", errs)
		}
	})

	t.Run("compute short, drive fine", func(t *testing.T) {
		c := fakeClientWithNodes(t, minNodesFleet(6, 4)...)
		errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
		if len(errs) != 1 || !strings.Contains(errs[0].Detail, "the compute-role nodeSelector") {
			t.Fatalf("expected only the compute violation, got %v", errs)
		}
	})
}

// TestAutoFullDrivesMinNodes_AtTheFloorPasses: exactly the floor is enough.
func TestAutoFullDrivesMinNodes_AtTheFloorPasses(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(5, 5)...)
	if errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("expected no violation at exactly the floor, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_UnsignedNodesStillCount is the bootstrap path. This policy counts LABELS,
// not signed drives: applying before sign-drives runs is a valid order of operations, and those nodes
// will host containers once signing completes. A labelled-but-unsigned fleet must be admitted.
func TestAutoFullDrivesMinNodes_UnsignedNodesStillCount(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	// Six drive nodes with no weka-full-drives annotation at all, six compute nodes.
	c := fakeClientWithNodes(t, minNodesFleet(6, 6)...)
	if errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("pre-signing fleets must be admitted — this policy counts labels, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_SinglePartyLowersTheFloor: the floors track configuration
// (ALLOW_SINGLE_PARITY lowers both to 3), not a hard-coded 5.
func TestAutoFullDrivesMinNodes_SingleParityLowersTheFloor(t *testing.T) {
	withFormClusterMinContainers(t, 3, 3)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(3, 3)...)
	if errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Errorf("expected no violation at the single-parity floor of 3, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_FloorOfZeroDisablesTheLeg: a non-positive floor switches the check off,
// per role, rather than substituting a default.
func TestAutoFullDrivesMinNodes_FloorOfZeroDisablesTheLeg(t *testing.T) {
	withFormClusterMinContainers(t, 0, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(1, 1)...)
	errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 || !strings.Contains(errs[0].Detail, "the compute-role nodeSelector") {
		t.Fatalf("expected the drive leg disabled and only compute reported, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_ZeroMatchedNodesIsReported: an empty selector match is the loudest form
// of this misconfiguration, and nothing else catches it in this mode — clusterSelectedNodesCount
// iterates the pinned counts, which are 0 here.
func TestAutoFullDrivesMinNodes_ZeroMatchedNodesIsReported(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, plainNode("unrelated", map[string]string{"other": "label"}))
	errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 2 {
		t.Fatalf("expected both roles reported when the selectors match nothing, got %v", errs)
	}
	// Guard the assumption this policy rests on: the pinned-count validator really is inert here.
	if other := (&clusterSelectedNodesCount{}).Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{})); len(other) != 0 {
		t.Errorf("clusterSelectedNodesCount was expected to no-op in this mode, got %v", other)
	}
}

// TestAutoFullDrivesMinNodes_NilTemplate: a nil dynamicTemplate is the mode's default shape and must
// be evaluated, not skipped.
func TestAutoFullDrivesMinNodes_NilTemplate(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(2, 2)...)
	if errs := v.Validate(ctx, c, minNodesCluster(nil)); len(errs) != 2 {
		t.Errorf("expected a nil template to be evaluated as auto-full-drives, got %v", errs)
	}
}

// TestAutoFullDrivesMinNodes_OtherSizingModesSkipped: with explicit counts or a capacity target the
// node count is not the container count, and clusterSelectedNodesCount / clusterMinContainers own it.
func TestAutoFullDrivesMinNodes_OtherSizingModesSkipped(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, minNodesFleet(1, 1)...)
	for name, dyn := range map[string]*weka.WekaClusterTemplate{
		"counts":            {ComputeContainers: 6, DriveContainers: 6},
		"clusterCapacity":   {ClusterCapacity: "500TiB"},
		"containerCapacity": {ContainerCapacity: 6000},
	} {
		t.Run(name, func(t *testing.T) {
			if errs := v.Validate(ctx, c, minNodesCluster(dyn)); len(errs) != 0 {
				t.Errorf("expected no violation outside auto-full-drives mode, got %v", errs)
			}
		})
	}
}

// TestAutoFullDrivesMinNodes_ListFailureSurfaces: a List error must not be silently admitted.
func TestAutoFullDrivesMinNodes_ListFailureSurfaces(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesMinNodes{}
	ctx := context.Background()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errors.New("boom")
		},
	}).Build()

	errs := v.Validate(ctx, c, minNodesCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 2 {
		t.Fatalf("expected one internal error per role, got %v", errs)
	}
	for _, e := range errs {
		if e.Type != field.ErrorTypeInternal {
			t.Errorf("expected an InternalError, got %v", e.Type)
		}
	}
}
