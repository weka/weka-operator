package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

func TestClusterDriveCoresBelowCapacity(t *testing.T) {
	// Per-core capacity caps set deterministically; mirrors internal/controllers/allocator/templates_test.go.
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	prevQlc := globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024  // 5120 GiB/core
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 50 * 1024 // 51200 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
		globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = prevQlc
	})

	v := &clusterDriveCoresBelowCapacity{}
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
			name: "driveCores unset skipped",
			dynamic: &weka.WekaClusterTemplate{
				ContainerCapacity: 6000, // would derive to 2 cores
			},
			wantErr: false,
		},
		{
			name: "containerCapacity mode: explicit below derived warns",
			dynamic: &weka.WekaClusterTemplate{
				ContainerCapacity: 6000, // ceil(6000/5120)=2
				DriveCores:        1,
			},
			wantErr: true,
		},
		{
			name: "containerCapacity mode: explicit equal to derived no warning",
			dynamic: &weka.WekaClusterTemplate{
				ContainerCapacity: 6000, // ceil(6000/5120)=2
				DriveCores:        2,
			},
			wantErr: false,
		},
		{
			name: "containerCapacity mode: explicit above derived no warning",
			dynamic: &weka.WekaClusterTemplate{
				ContainerCapacity: 6000, // ceil(6000/5120)=2
				DriveCores:        3,
			},
			wantErr: false,
		},
		{
			name: "numDrives+driveCapacity mode: explicit below derived warns",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 2000, // 8000 GiB, ceil(8000/5120)=2
				DriveCores:    1,
			},
			wantErr: true,
		},
		{
			name: "numDrives+driveCapacity mode: explicit equal to derived no warning",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 2000, // 8000 GiB, ceil(8000/5120)=2
				DriveCores:    2,
			},
			wantErr: false,
		},
		{
			name: "numDrives+driveCapacity mode: explicit above derived no warning",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 2000, // 8000 GiB, ceil(8000/5120)=2
				DriveCores:    3,
			},
			wantErr: false,
		},
		{
			name: "no capacity basis to derive from: no warning regardless of driveCores",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:  4, // pure full-drives mode, no driveCapacity
				DriveCores: 1,
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
