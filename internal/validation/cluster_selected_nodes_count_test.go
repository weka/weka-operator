package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func selectedNodesCluster(dynamic *weka.WekaClusterTemplate, selector map[string]string) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	c.Spec.NodeSelector = selector
	return c
}

// Count-based behavior — unchanged regression guard.
func TestClusterSelectedNodesCount_CountBased(t *testing.T) {
	v := &clusterSelectedNodesCount{}
	ctx := context.Background()
	labels := map[string]string{"role": "any"}

	t.Run("containers within matched node count passes", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
			driveRoleNode(t, "n2", labels, nil),
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 2}
		errs := v.Validate(ctx, c, selectedNodesCluster(dynamic, labels))
		if len(errs) != 0 {
			t.Errorf("expected no error, got %v", errs)
		}
	})

	t.Run("containers exceeding matched node count flagged", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 2}
		errs := v.Validate(ctx, c, selectedNodesCluster(dynamic, labels))
		if len(errs) == 0 {
			t.Errorf("expected an error, got none")
		}
	})
}

// Auto-full-drives: driveContainers is always 0 in that mode, so the drive-role branch is structurally
// inert — this locks in that such a cluster is never flagged on the drive role, even with zero matched
// nodes. A pinned computeContainers leaves the mode, and that role keeps behaving as before.
func TestClusterSelectedNodesCount_AutoFullDrivesDriveRoleInert(t *testing.T) {
	v := &clusterSelectedNodesCount{}
	ctx := context.Background()
	labels := map[string]string{"role": "any"}

	t.Run("auto-full-drives with zero matched drive nodes: not flagged", func(t *testing.T) {
		c := fakeClientWithNodes(t) // no nodes at all
		dynamic := &weka.WekaClusterTemplate{}
		errs := v.Validate(ctx, c, selectedNodesCluster(dynamic, labels))
		if len(errs) != 0 {
			t.Errorf("expected no error (driveContainers is always 0 in auto-full-drives mode), got %v", errs)
		}
	})

	t.Run("a pinned compute role is still enforced", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
		)
		dynamic := &weka.WekaClusterTemplate{ComputeContainers: 3}
		errs := v.Validate(ctx, c, selectedNodesCluster(dynamic, labels))
		if len(errs) == 0 {
			t.Errorf("expected an error: 3 compute containers pinned against 1 matched node")
		}
	})
}
