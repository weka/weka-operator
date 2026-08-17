package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func TestClusterComputeDriveCoresFloor(t *testing.T) {
	v := &clusterComputeDriveCoresFloor{}
	ctx := context.Background()

	tests := []struct {
		name    string
		dynamic *weka.WekaClusterTemplate
		wantErr bool
	}{
		{
			name:    "no dynamic template skipped",
			dynamic: nil,
			wantErr: false,
		},
		{
			name: "zero drive containers skipped",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 0, ComputeContainers: 4,
			},
			wantErr: false,
		},
		{
			name: "zero compute containers skipped",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 4, ComputeContainers: 0,
			},
			wantErr: false,
		},
		{
			name: "compute cores equal to drive cores clears the floor",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 2, ComputeCores: 2,
			},
			wantErr: false,
		},
		{
			name: "compute cores above drive cores clears the floor",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 4,
				DriveCores: 1, ComputeCores: 1,
			},
			wantErr: false,
		},
		{
			name: "compute cores below drive cores violates the 1:1 floor",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 4, ComputeContainers: 1,
				DriveCores: 2, ComputeCores: 1,
			},
			wantErr: true,
		},
		{
			name: "unset cores default to 1:1 per container, floor cleared",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 1,
			},
			wantErr: false,
		},
		{
			// The planner derives compute cores when computeCores is unset (funcs_fd_planning.go passes
			// 0 == auto-derive), so the template's 1-core default describes a cluster that never runs.
			// Reading it here would reject a spec the planner sizes correctly.
			name: "clusterCapacity with more drive than compute containers skipped",
			dynamic: &weka.WekaClusterTemplate{
				ClusterCapacity: "1PiB",
				DriveContainers: 8, ComputeContainers: 5,
			},
			wantErr: false,
		},
		{
			name: "clusterCapacity with a pinned driveCores skipped",
			dynamic: &weka.WekaClusterTemplate{
				ClusterCapacity: "1PiB",
				DriveContainers: 6, ComputeContainers: 6,
				DriveCores: 4,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := v.Validate(ctx, nil, ratioCluster(tt.dynamic))
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected an error, got none")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Errorf("expected no error, got %v", errs)
			}
		})
	}
}
