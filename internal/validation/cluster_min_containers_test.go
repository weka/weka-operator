package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withFormClusterMinContainers sets the two minimums the validator reads and restores them after the test.
func withFormClusterMinContainers(t *testing.T, drive, compute int) {
	t.Helper()
	prevD := globalconfig.Consts.FormClusterMinDriveContainers
	prevC := globalconfig.Consts.FormClusterMinComputeContainers
	globalconfig.Consts.FormClusterMinDriveContainers = drive
	globalconfig.Consts.FormClusterMinComputeContainers = compute
	t.Cleanup(func() {
		globalconfig.Consts.FormClusterMinDriveContainers = prevD
		globalconfig.Consts.FormClusterMinComputeContainers = prevC
	})
}

// TestClusterMinContainers covers pinned counts below the form-cluster minimum: a feasible plan whose
// pods run, yet the cluster never forms.
func TestClusterMinContainers(t *testing.T) {
	v := &clusterMinContainers{}
	ctx := context.Background()

	tests := []struct {
		name        string
		minDrive    int
		minCompute  int
		dynamic     *weka.WekaClusterTemplate
		wantN       int
		wantSubs    []string
		wantNotSubs []string
	}{
		{
			name:       "no dynamic template skipped",
			minDrive:   5,
			minCompute: 5,
			dynamic:    nil,
		},
		{
			// Unset counts are derived elsewhere and default to these minimums, so admission must not object.
			name:       "both unset skipped",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{},
		},
		{
			name:       "at the minimum is allowed",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 5, ComputeContainers: 5},
		},
		{
			name:       "above the minimum is allowed",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 8, ComputeContainers: 6},
		},
		{
			// clusterCapacity alongside a single pinned count: the planner accepts it, the pods run,
			// the cluster never forms.
			name:       "driveContainers below the minimum is rejected",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{ClusterCapacity: "500TiB", DriveContainers: 3},
			wantN:      1,
			wantSubs: []string{
				"driveContainers", "(3)", "below the 5 drive container(s)", "MinContainersNotReady",
				"raise driveContainers to at least 5",
			},
		},
		{
			name:       "computeContainers below the minimum is rejected",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{ComputeContainers: 2},
			wantN:      1,
			wantSubs:   []string{"computeContainers", "(2)", "below the 5 compute container(s)"},
			// The auto-full-drives remedies are gone: they were unreachable (both counts are 0 in that
			// mode, so the "unset" skip fires first) and their wording was wrong.
			wantNotSubs: []string{"caps how many eligible nodes are used", "remove the pin"},
		},
		{
			name:       "both below the minimum reported separately",
			minDrive:   5,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 1, ComputeContainers: 2},
			wantN:      2,
		},
		{
			// ALLOW_SINGLE_PARITY lowers the minimum to 3; the validator tracks config, not a hard-coded 5.
			name:       "honors a lowered minimum (single parity)",
			minDrive:   3,
			minCompute: 3,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 3, ComputeContainers: 3},
		},
		{
			name:       "honors a raised minimum",
			minDrive:   7,
			minCompute: 7,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 5},
			wantN:      1,
			wantSubs:   []string{"below the 7 drive container(s)"},
		},
		{
			// A non-positive minimum switches the check off; admission must agree, not substitute a default.
			name:       "minimum of zero disables the check",
			minDrive:   0,
			minCompute: 0,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 1, ComputeContainers: 1},
		},
		{
			// The two roles are independent: a drive minimum of 0 must not suppress the compute violation.
			name:       "roles are independent",
			minDrive:   0,
			minCompute: 5,
			dynamic:    &weka.WekaClusterTemplate{DriveContainers: 1, ComputeContainers: 1},
			wantN:      1,
			wantSubs:   []string{"computeContainers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFormClusterMinContainers(t, tt.minDrive, tt.minCompute)
			cluster := &weka.WekaCluster{Spec: weka.WekaClusterSpec{Dynamic: tt.dynamic}}

			errs := v.Validate(ctx, nil, cluster)
			if len(errs) != tt.wantN {
				t.Fatalf("got %d violation(s), want %d: %v", len(errs), tt.wantN, errs)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(errs[0].Detail, sub) {
					t.Errorf("detail missing %q, got: %s", sub, errs[0].Detail)
				}
			}
			for _, sub := range tt.wantNotSubs {
				if strings.Contains(errs[0].Detail, sub) {
					t.Errorf("detail must not contain %q, got: %s", sub, errs[0].Detail)
				}
			}
		})
	}
}
