package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withDefaultProtection sets the DriveSharing default protection values on the
// global config for the duration of the test, restoring originals via t.Cleanup.
func withDefaultProtection(t *testing.T, sw, rl, hs int) {
	t.Helper()
	prevSW := globalconfig.Config.DriveSharing.DefaultStripeWidth
	prevRL := globalconfig.Config.DriveSharing.DefaultRedundancyLevel
	prevHS := globalconfig.Config.DriveSharing.DefaultHotSpare
	globalconfig.Config.DriveSharing.DefaultStripeWidth = sw
	globalconfig.Config.DriveSharing.DefaultRedundancyLevel = rl
	globalconfig.Config.DriveSharing.DefaultHotSpare = hs
	t.Cleanup(func() {
		globalconfig.Config.DriveSharing.DefaultStripeWidth = prevSW
		globalconfig.Config.DriveSharing.DefaultRedundancyLevel = prevRL
		globalconfig.Config.DriveSharing.DefaultHotSpare = prevHS
	})
}

func TestClusterCapacityProtection_DefaultsHonored(t *testing.T) {
	// Defaults at the production floor (3+2+0) should pass when the spec is all-zero.
	withDefaultProtection(t, 3, 2, 0)

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID("u-defaults")},
	}
	cluster.Spec.StripeWidth = 0
	cluster.Spec.RedundancyLevel = 0
	cluster.Spec.HotSpare = 0
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: "300TiB"}

	v := clusterCapacityProtection{}
	errs := v.Validate(context.Background(), nil, cluster)
	if len(errs) != 0 {
		t.Errorf("expected no errors when defaults meet the floor, got: %v", errs)
	}
}

func TestClusterCapacityProtection_BelowFloorRejected(t *testing.T) {
	// Spec values below the floor (sw=2 < minSW=3, rl=1 < minRL=2) with zero defaults
	// (so spec values win) should produce two field errors.
	withDefaultProtection(t, 0, 0, 0)

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID("u-below")},
	}
	cluster.Spec.StripeWidth = 2
	cluster.Spec.RedundancyLevel = 1
	cluster.Spec.HotSpare = 0
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: "300TiB"}

	v := clusterCapacityProtection{}
	errs := v.Validate(context.Background(), nil, cluster)
	if len(errs) != 2 {
		t.Errorf("expected two field errors (stripeWidth + redundancyLevel), got %d: %v", len(errs), errs)
	}
}

func TestClusterCapacityProtection_SkipsNonClusterCapacity(t *testing.T) {
	// A cluster using ContainerCapacity (not ClusterCapacity) must be skipped entirely.
	withDefaultProtection(t, 0, 0, 0)

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID("u-cc")},
	}
	cluster.Spec.StripeWidth = 0
	cluster.Spec.RedundancyLevel = 0
	cluster.Spec.HotSpare = 0
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ContainerCapacity: 8000}

	v := clusterCapacityProtection{}
	errs := v.Validate(context.Background(), nil, cluster)
	if len(errs) != 0 {
		t.Errorf("expected no errors for non-clusterCapacity cluster, got: %v", errs)
	}
}

func TestClusterCapacityProtection_SkipsNilDynamic(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID("u-nil")},
	}
	// Dynamic is nil — validator must return nil immediately.

	v := clusterCapacityProtection{}
	errs := v.Validate(context.Background(), nil, cluster)
	if len(errs) != 0 {
		t.Errorf("expected no errors for nil Dynamic, got: %v", errs)
	}
}

func TestClusterCapacityProtection_AllowSingleParity(t *testing.T) {
	// When AllowSingleParity is set the floor drops to 2+1+0 — a 2+1+0 spec must pass.
	prevASP := globalconfig.Config.DriveSharing.AllowSingleParity
	globalconfig.Config.DriveSharing.AllowSingleParity = true
	t.Cleanup(func() { globalconfig.Config.DriveSharing.AllowSingleParity = prevASP })

	withDefaultProtection(t, 0, 0, 0)

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID("u-sp")},
	}
	cluster.Spec.StripeWidth = 2
	cluster.Spec.RedundancyLevel = 1
	cluster.Spec.HotSpare = 0
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: "300TiB"}

	v := clusterCapacityProtection{}
	errs := v.Validate(context.Background(), nil, cluster)
	if len(errs) != 0 {
		t.Errorf("expected no errors with AllowSingleParity and 2+1+0 spec, got: %v", errs)
	}
}
