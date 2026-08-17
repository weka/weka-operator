package validation

import (
	"context"
	"errors"
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

	// clusterMinDrivesFeasibility rejects this exact state; warning too would double-report it.
	t.Run("auto-full-drives with minNumDrives defers to the feasibility error", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))
		cluster := withSelector(autoFullDrives, 10)
		if errs := v.Validate(ctx, c, cluster); len(errs) != 0 {
			t.Errorf("expected no advisory, got %v", errs)
		}
		// Guard the assumption the suppression rests on: something else does report it.
		if errs := (&clusterMinDrivesFeasibility{}).Validate(ctx, c, cluster); len(errs) == 0 {
			t.Errorf("suppressed the advisory but clusterMinDrivesFeasibility stayed silent too")
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
