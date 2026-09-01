package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func minDrivesCluster(dynamic *weka.WekaClusterTemplate, minNumDrives int) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	if minNumDrives > 0 {
		c.Spec.StartIoConditions = &weka.StartIoConditions{MinNumDrives: minNumDrives}
	}
	return c
}

// Count-based behavior — regression guard for the driveContainers × numDrives check (nil client: this
// path never touches it). Every case here sets a field that leaves auto-full-drives mode, so none of
// them reaches the node-listing branch.
func TestClusterMinDrivesFeasibility_CountBased(t *testing.T) {
	v := &clusterMinDrivesFeasibility{}
	ctx := context.Background()

	tests := []struct {
		name    string
		dynamic *weka.WekaClusterTemplate
		minNum  int
		wantErr bool
	}{
		{
			name:    "minNumDrives unset skipped",
			dynamic: &weka.WekaClusterTemplate{DriveContainers: 2, NumDrives: 2},
			minNum:  0,
			wantErr: false,
		},
		{
			name:    "driveContainers/numDrives unset (operator-derived) skipped",
			dynamic: &weka.WekaClusterTemplate{ContainerCapacity: 6000},
			minNum:  10,
			wantErr: false,
		},
		{
			name:    "minNumDrives within total passes",
			dynamic: &weka.WekaClusterTemplate{DriveContainers: 2, NumDrives: 4},
			minNum:  8,
			wantErr: false,
		},
		{
			name:    "minNumDrives exceeding total flagged",
			dynamic: &weka.WekaClusterTemplate{DriveContainers: 2, NumDrives: 4},
			minNum:  9,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := v.Validate(ctx, nil, minDrivesCluster(tt.dynamic, tt.minNum))
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected an error, got none")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Errorf("expected no error, got %v", errs)
			}
		})
	}
}

// Auto-full-drives behavior: both container counts are 0 in this mode, so the total is derived from
// signed full drives on drive-role-matched nodes instead.
func TestClusterMinDrivesFeasibility_AutoFullDrives(t *testing.T) {
	v := &clusterMinDrivesFeasibility{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}
	autoCluster := func(dynamic *weka.WekaClusterTemplate, minNumDrives int) *weka.WekaCluster {
		c := minDrivesCluster(dynamic, minNumDrives)
		c.Spec.NodeSelector = labels
		return c
	}
	emptyTemplate := func(minNumDrives int) *weka.WekaCluster {
		return autoCluster(&weka.WekaClusterTemplate{}, minNumDrives)
	}

	t.Run("sufficient signed drives passes", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000, 1000}), // 3 drives
			driveRoleNode(t, "n2", labels, []int{1000, 1000}),       // 2 drives
		)
		errs := v.Validate(ctx, c, emptyTemplate(5))
		if len(errs) != 0 {
			t.Errorf("expected no error, got %v", errs)
		}
	})

	t.Run("minNumDrives exceeding total signed flagged", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000, 1000}), // 3 drives
			driveRoleNode(t, "n2", labels, []int{1000, 1000}),       // 2 drives
		)
		errs := v.Validate(ctx, c, emptyTemplate(6))
		if len(errs) == 0 {
			t.Errorf("expected an error, got none")
		}
	})

	// A nil dynamicTemplate is auto-full-drives mode, not "nothing configured": it must take this
	// branch rather than being skipped, or an unsatisfiable minNumDrives sails through.
	t.Run("nil dynamicTemplate takes the auto-full-drives branch", func(t *testing.T) {
		c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, []int{1000, 1000}))
		if errs := v.Validate(ctx, c, autoCluster(nil, 2)); len(errs) != 0 {
			t.Errorf("expected no error at minNumDrives=2, got %v", errs)
		}
		if errs := v.Validate(ctx, c, autoCluster(nil, 3)); len(errs) == 0 {
			t.Errorf("expected an error at minNumDrives=3 with only 2 signed drives, got none")
		}
	})

	// The over-count bug: a pinned numDrives caps each node's contribution, so the reachable total is
	// min(signed, numDrives) per node, not the raw sum.
	t.Run("pinned numDrives caps each node's contribution", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000, 1000, 1000, 1000}), // 5 signed
			driveRoleNode(t, "n2", labels, []int{1000, 1000, 1000, 1000, 1000}), // 5 signed
		)
		pinned := func(min int) *weka.WekaCluster {
			return autoCluster(&weka.WekaClusterTemplate{NumDrives: 2}, min)
		}
		// numDrives=2 means 2 per node = 4 reachable, not 10.
		if errs := v.Validate(ctx, c, pinned(4)); len(errs) != 0 {
			t.Errorf("expected no error at minNumDrives=4, got %v", errs)
		}
		errs := v.Validate(ctx, c, pinned(5))
		if len(errs) != 1 {
			t.Fatalf("expected an error at minNumDrives=5 (only 4 reachable under the pin), got %v", errs)
		}
		if !strings.Contains(errs[0].Detail, "pinned numDrives=2") {
			t.Errorf("expected the message to name the pin as the cause, got %q", errs[0].Detail)
		}
	})

	// No bootstrap skip: unsigned nodes mean the mode has nothing to consume, so any positive
	// minNumDrives is rejected rather than silently admitted.
	t.Run("pre-signing (no annotations) rejected", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, nil),
			driveRoleNode(t, "n2", labels, nil),
		)
		errs := v.Validate(ctx, c, emptyTemplate(100))
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %v", errs)
		}
		if !strings.Contains(errs[0].Detail, "has any signed") {
			t.Errorf("expected the unsigned-specific message, got %q", errs[0].Detail)
		}
	})

	t.Run("partially signed counts only what is signed", func(t *testing.T) {
		c := fakeClientWithNodes(t,
			driveRoleNode(t, "n1", labels, []int{1000, 1000}), // 2 drives
			driveRoleNode(t, "n2", labels, nil),               // not signed yet
		)
		if errs := v.Validate(ctx, c, emptyTemplate(2)); len(errs) != 0 {
			t.Errorf("expected no error at minNumDrives=2, got %v", errs)
		}
		if errs := v.Validate(ctx, c, emptyTemplate(3)); len(errs) == 0 {
			t.Errorf("expected an error at minNumDrives=3, got none")
		}
	})

	t.Run("no matched nodes skipped", func(t *testing.T) {
		c := fakeClientWithNodes(t)
		errs := v.Validate(ctx, c, emptyTemplate(100))
		if len(errs) != 0 {
			t.Errorf("expected no error (no matched nodes), got %v", errs)
		}
	})
}
