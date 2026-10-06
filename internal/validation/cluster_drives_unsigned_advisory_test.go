package validation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/weka/weka-operator/internal/consts"
)

// sharedDriveRoleNode builds a proxy-mode node: matched by the drive-role selector and signed via
// weka-shared-drives rather than weka-full-drives.
func sharedDriveRoleNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
			Annotations: map[string]string{
				consts.AnnotationSharedDrives: `[["550e8400-e29b-41d4-a716-446655440000","S1",7000,"/dev/nvme0n1"]]`,
			},
		},
	}
}

func TestClusterDrivesUnsignedAdvisory(t *testing.T) {
	v := &clusterDrivesUnsignedAdvisory{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	withSelector := func(dynamic *weka.WekaClusterTemplate, minNumDrives int) *weka.WekaCluster {
		c := minDrivesCluster(dynamic, minNumDrives)
		c.Spec.NodeSelector = labels
		return c
	}
	sized := &weka.WekaClusterTemplate{DriveContainers: 2, NumDrives: 4}
	autoFullDrives := &weka.WekaClusterTemplate{}
	// Capacity-based sizing is what makes IsDriveSharing() true.
	sharing := &weka.WekaClusterTemplate{ClusterCapacity: "100TiB"}

	t.Run("all matched nodes unsigned warns", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
			driveRoleNode(t, "n2", labels, nil),
		)
		errs := v.Validate(ctx, c, withSelector(sized, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		if d := errs[0].Detail; !strings.Contains(d, "role=drive") || !strings.Contains(d, "n1, n2") {
			t.Errorf("expected selector and node names in the message, got %q", d)
		}
	})

	// The whole point of generalizing: auto-full-drives clusters have no drive count to check, so
	// nothing else would say anything here.
	t.Run("auto-full-drives without minNumDrives warns", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		if errs := v.Validate(ctx, c, withSelector(autoFullDrives, 0)); len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
	})

	// A nil dynamicTemplate is the default shape of auto-full-drives mode, not an out-of-scope
	// cluster: it needs signed drives just as much as an explicit template does.
	t.Run("nil dynamicTemplate warns", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		if errs := v.Validate(ctx, c, withSelector(nil, 0)); len(errs) != 1 {
			t.Fatalf("expected 1 advisory for a nil template, got %v", errs)
		}
	})

	// The unsigned advisory is the sole owner of "nothing signed yet" in this mode, whatever minNumDrives is.
	t.Run("auto-full-drives with minNumDrives still warns", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		if errs := v.Validate(ctx, c, withSelector(autoFullDrives, 10)); len(errs) != 1 {
			t.Errorf("expected 1 advisory, got %v", errs)
		}
	})

	t.Run("auto-full-drives lists every unsigned node, not only the none-signed case", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000}),
			driveRoleNode(t, "n2", labels, nil),
			driveRoleNode(t, "n3", labels, nil),
		)
		errs := v.Validate(ctx, c, withSelector(autoFullDrives, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		d := errs[0].Detail
		if !strings.Contains(d, "n2, n3") || strings.Contains(d, "n1") || !strings.Contains(d, "2 of the 3") {
			t.Errorf("expected exactly the unsigned nodes n2, n3, got %q", d)
		}
	})

	t.Run("auto-full-drives treats unparsable, empty and all-blocked nodes as unsigned", func(t *testing.T) {
		bad := driveRoleNode(t, "bad", labels, []int{1000})
		bad.Annotations[consts.AnnotationWekaFullDrives] = "{not json"
		empty := driveRoleNode(t, "empty", labels, []int{})
		blocked := driveRoleNode(t, "blocked", labels, []int{1000})
		blocked.Annotations[consts.AnnotationBlockedDrives] = `["blocked-d0"]`
		c := fakeClientWithNodes(t, driveRoleNode(t, "ok", labels, []int{1000}), bad, empty, blocked)
		errs := v.Validate(ctx, c, withSelector(autoFullDrives, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		d := errs[0].Detail
		if !strings.Contains(d, "bad, blocked, empty") || strings.Contains(d, "ok") || !strings.Contains(d, "3 of the 4") {
			t.Errorf("expected exactly bad, blocked, empty, got %q", d)
		}
	})

	t.Run("auto-full-drives caps the listed nodes at 10", func(t *testing.T) {
		var nodes []*corev1.Node
		for i := 0; i < 12; i++ {
			nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("n%02d", i), labels, nil))
		}
		errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), withSelector(autoFullDrives, 0))
		if len(errs) != 1 || !strings.Contains(errs[0].Detail, "n09 (+2 more)") || strings.Contains(errs[0].Detail, "n10") {
			t.Errorf("expected 10 nodes then (+2 more), got %v", errs)
		}
	})

	t.Run("auto-full-drives all signed is silent", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, []int{1000}))
		if errs := v.Validate(ctx, c, withSelector(autoFullDrives, 0)); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
	})

	t.Run("one full-signed node silences", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000}),
			driveRoleNode(t, "n2", labels, nil),
		)
		if errs := v.Validate(ctx, c, withSelector(sized, 0)); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
	})

	// Mode-awareness: shared-drives signing does not satisfy a full-drives cluster. The populations
	// are disjoint, so this is a mode mismatch, not a signed node.
	t.Run("shared-signed nodes do not satisfy a full-drives cluster", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			sharedDriveRoleNode("n1", labels),
			driveRoleNode(t, "n2", labels, nil),
		)
		errs := v.Validate(ctx, c, withSelector(sized, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		if d := errs[0].Detail; !strings.Contains(d, "1 of the 2") || !strings.Contains(d, "Re-sign") {
			t.Errorf("expected the mode-mismatch message, got %q", d)
		}
	})

	t.Run("drive-sharing cluster is satisfied by shared-signed nodes", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			sharedDriveRoleNode("n1", labels),
			driveRoleNode(t, "n2", labels, nil),
		)
		if errs := v.Validate(ctx, c, withSelector(sharing, 0)); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
	})

	t.Run("full-signed nodes do not satisfy a drive-sharing cluster", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, []int{1000}))
		errs := v.Validate(ctx, c, withSelector(sharing, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		d := errs[0].Detail
		if !strings.Contains(d, consts.AnnotationSharedDrives) {
			t.Errorf("expected the shared-drives annotation to be named as required, got %q", d)
		}
		if !strings.Contains(d, "full-drives mode") {
			t.Errorf("expected the node's actual mode to be named, got %q", d)
		}
	})

	// Mid-migration the nodes are still full-drives-signed; the campaign re-signs them node by node.
	t.Run("full-signed nodes silent for a drive-sharing cluster being migrated", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, []int{1000}))
		cluster := withSelector(&weka.WekaClusterTemplate{DriveContainers: 2, ContainerCapacity: 5000}, 0)
		cluster.Annotations = map[string]string{consts.AnnotationSizingModeMigration: consts.SizingModeMigrationDriveSharing}
		if errs := v.Validate(ctx, c, cluster); len(errs) != 0 {
			t.Errorf("expected no advisory during migration, got %v", errs)
		}
		// Unsigned nodes are not the migration's expected state and still warn.
		c = fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		if errs := v.Validate(ctx, c, cluster); len(errs) != 1 {
			t.Errorf("expected 1 advisory for unsigned nodes during migration, got %v", errs)
		}
	})

	t.Run("drive-sharing cluster with fully unsigned nodes warns", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		errs := v.Validate(ctx, c, withSelector(sharing, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		if d := errs[0].Detail; strings.Contains(d, "Re-sign") {
			t.Errorf("expected the not-signed message, not the mismatch one: %q", d)
		}
	})

	t.Run("unmatched unsigned node ignored", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000}),
			driveRoleNode(t, "other", map[string]string{"role": "compute"}, nil),
		)
		if errs := v.Validate(ctx, c, withSelector(sized, 0)); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
	})

	t.Run("no matched nodes skipped", func(t *testing.T) {
		if errs := v.Validate(ctx, fakeClientWithNodes(t), withSelector(sized, 0)); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
	})

	t.Run("node List failure surfaces as an internal error, not silently admitted", func(t *testing.T) {
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
		listErr := errors.New("boom")
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return listErr
			},
		}).Build()
		errs := v.Validate(ctx, c, withSelector(sized, 0))
		if len(errs) != 1 {
			t.Fatalf("expected the List failure to surface as one error, got %v", errs)
		}
		if errs[0].Type != field.ErrorTypeInternal {
			t.Errorf("expected an InternalError, got %v", errs[0].Type)
		}
	})

	t.Run("many unsigned nodes truncates the name list", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
			driveRoleNode(t, "n2", labels, nil),
			driveRoleNode(t, "n3", labels, nil),
			driveRoleNode(t, "n4", labels, nil),
			driveRoleNode(t, "n5", labels, nil),
		)
		errs := v.Validate(ctx, c, withSelector(sized, 0))
		if len(errs) != 1 {
			t.Fatalf("expected 1 advisory, got %v", errs)
		}
		if d := errs[0].Detail; !strings.Contains(d, "n1, n2, n3 and 2 more") {
			t.Errorf("expected a truncated node list, got %q", d)
		}
	})
}
