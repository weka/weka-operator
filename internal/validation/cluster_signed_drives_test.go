package validation

import (
	"context"
	"fmt"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func signedDrivesCluster(dynamic *weka.WekaClusterTemplate, selector map[string]string) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	c.Spec.NodeSelector = selector
	return c
}

// Count-based behavior — regression guard for the driveContainers × numDrives vs. signed-drives check.
func TestClusterSignedDrives_CountBased(t *testing.T) {
	v := &clusterSignedDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	t.Run("sufficient signed drives passes", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000, 1000, 1000}), // 4 drives
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 1, NumDrives: 4}
		errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(errs) != 0 {
			t.Errorf("expected no error, got %v", errs)
		}
	})

	t.Run("requested exceeding signed flagged", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000}), // 2 drives
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 1, NumDrives: 4}
		errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(errs) == 0 {
			t.Errorf("expected an error, got none")
		}
	})

	t.Run("pre-signing (no annotations) skipped, not rejected", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 1, NumDrives: 4}
		errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(errs) != 0 {
			t.Errorf("expected no error (bootstrap skip), got %v", errs)
		}
	})

	t.Run("driveContainers or numDrives unset skipped", func(t *testing.T) {
		c := fakeClientWithNodes(t)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 0, NumDrives: 4}
		errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(errs) != 0 {
			t.Errorf("expected no error, got %v", errs)
		}
	})

	// Shared drives are a disjoint population and must not pad the availability count into a false pass.
	t.Run("shared drives do not count toward a full-drives request", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000}), // 1 full drive, signed
			sharedDriveRoleNode("n2", labels),           // proxy-signed; unusable here
		)
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 1, NumDrives: 2}
		errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(errs) == 0 {
			t.Errorf("expected an error: 2 requested vs 1 usable full drive")
		}
	})

	// Bootstrap skip keys on the full-drives annotation only; proxy-signed nodes aren't "signed" here.
	t.Run("only shared-signed nodes bootstrap-skipped", func(t *testing.T) {
		c := fakeClientWithNodes(t, sharedDriveRoleNode("n1", labels))
		dynamic := &weka.WekaClusterTemplate{DriveContainers: 1, NumDrives: 4}
		if errs := v.Validate(ctx, c, signedDrivesCluster(dynamic, labels)); len(errs) != 0 {
			t.Errorf("expected no error (bootstrap skip), got %v", errs)
		}
		advisory := (&clusterDrivesUnsignedAdvisory{}).Validate(ctx, c, signedDrivesCluster(dynamic, labels))
		if len(advisory) != 1 {
			t.Errorf("expected the unsigned advisory to cover this state, got %v", advisory)
		}
	})
}

// Drive-sharing clusters: numDrives counts virtual drives, so comparing to physical count is a category
// error. Skipped entirely — cluster_capacity_* owns feasibility there.
func TestClusterSignedDrives_DriveSharingSkipped(t *testing.T) {
	v := &clusterSignedDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	// 8×6=48 virtual drives against 1 physical shared drive: counting here would wrongly reject.
	c := fakeClientWithNodes(t, sharedDriveRoleNode("n1", labels))
	dynamic := &weka.WekaClusterTemplate{DriveContainers: 6, NumDrives: 8, DriveCapacity: 100}

	cluster := signedDrivesCluster(dynamic, labels)
	if !cluster.IsDriveSharing() {
		t.Fatalf("test premise broken: driveCapacity cluster is not IsDriveSharing()")
	}
	if errs := v.Validate(ctx, c, cluster); len(errs) != 0 {
		t.Errorf("expected drive-sharing to be skipped, got %v", errs)
	}
}

// Auto-full-drives: driveContainers is always 0 in that mode, so this validator always skips
// regardless of how few drives are signed. Locks in the explicit UsesAutoFullDrives() early-return.
func TestClusterSignedDrives_AutoFullDrivesAlwaysSkipped(t *testing.T) {
	v := &clusterSignedDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	tests := []struct {
		name  string
		nodes []int // per-node signed drive counts; nil node list when empty
	}{
		{name: "no matched nodes"},
		{name: "matched nodes, none signed yet", nodes: []int{0}},
		{name: "matched nodes with signed drives", nodes: []int{4, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nodes []*corev1.Node
			for i, count := range tt.nodes {
				var caps []int
				if count > 0 {
					caps = make([]int, count)
					for j := range caps {
						caps[j] = 1000
					}
				}
				nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("n%d", i), labels, caps))
			}
			c := fakeClientWithNodes(t, nodes...)
			errs := v.Validate(ctx, c, signedDrivesCluster(&weka.WekaClusterTemplate{}, labels))
			if len(errs) != 0 {
				t.Errorf("expected no error in auto-full-drives mode, got %v", errs)
			}
		})
	}
}
