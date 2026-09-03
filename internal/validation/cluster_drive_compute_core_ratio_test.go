package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

func ratioCluster(dynamic *weka.WekaClusterTemplate) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	return c
}

// setRatioConfig sets the ratio knobs the validator reads (zero by default since LoadCapacityEnv isn't
// called in unit tests) and restores them on cleanup.
func setRatioConfig(t *testing.T, tlcRatio, fullDrivesRatio float64) {
	prevTlc := globalconfig.Config.CapacityPlanner.ComputeToTlcDriveCoreRatio
	prevFullDrives := globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio
	globalconfig.Config.CapacityPlanner.ComputeToTlcDriveCoreRatio = tlcRatio
	globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = fullDrivesRatio
	t.Cleanup(func() {
		globalconfig.Config.CapacityPlanner.ComputeToTlcDriveCoreRatio = prevTlc
		globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio = prevFullDrives
	})
}

func TestClusterDriveComputeCoreRatio(t *testing.T) {
	// Pinned above the shipped drive-sharing 1.0, where the advisory is inert by design, so these cases are
	// observable at all. TestClusterDriveComputeCoreRatio_FullDrivesUsesFullDrivesRatio covers the shipped pair.
	setRatioConfig(t, 2.0, 2.0)

	v := &clusterDriveComputeCoreRatio{}
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
			name: "explicit cores within recommended ratio passes",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 1, ComputeCores: 4,
			},
			wantErr: false,
		},
		{
			// driveSide=2, computeSide=2: clears the floor but required=ceil(2.0*2)=4 -> flagged.
			name: "explicit cores at the 1:1 floor but below recommended ratio flagged",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 1, ComputeCores: 1,
			},
			wantErr: true,
		},
		{
			// Same shape as the flagged case above, but under clusterCapacity the planner assigns both
			// sides, so the template's numbers are not the ones the cluster runs on.
			name: "clusterCapacity skipped even below the recommended ratio",
			dynamic: &weka.WekaClusterTemplate{
				ClusterCapacity: "1PiB",
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 1, ComputeCores: 1,
			},
			wantErr: false,
		},
		{
			name: "unset cores on both sides default to 1:1 per container, within ratio",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 2,
			},
			wantErr: false,
		},
		{
			// driveSide=8, computeSide=1: below floor, owned exclusively by cluster_compute_drive_cores_floor.
			name: "computeSide below driveSide is owned by cluster_compute_drive_cores_floor, skipped here",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 4, ComputeContainers: 1,
				DriveCores: 2, ComputeCores: 1,
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

// TestClusterDriveComputeCoreRatio_ZeroRatioDisables verifies a ratio of 0 (also the default, since
// LoadCapacityEnv isn't called in unit tests) disables the advisory entirely.
func TestClusterDriveComputeCoreRatio_ZeroRatioDisables(t *testing.T) {
	setRatioConfig(t, 0, 0)

	v := &clusterDriveComputeCoreRatio{}
	ctx := context.Background()

	dynamic := &weka.WekaClusterTemplate{
		DriveContainers: 1, ComputeContainers: 1,
		DriveCores: 1, ComputeCores: 1, ContainerCapacity: 1000,
	}
	errs := v.Validate(ctx, nil, ratioCluster(dynamic))
	if len(errs) != 0 {
		t.Errorf("expected no error with ratio disabled, got %v", errs)
	}
}

// TestClusterDriveComputeCoreRatio_FullDrivesUsesFullDrivesRatio verifies exclusive full-drives mode
// (numDrives>0 && driveCapacity==0) reads FullDrivesComputeToDriveCoreRatio instead of
// ComputeToTlcDriveCoreRatio. Auto-full-drives mode cannot reach this validator at all: it requires
// both container counts unset, and both being set is the precondition for running.
func TestClusterDriveComputeCoreRatio_FullDrivesUsesFullDrivesRatio(t *testing.T) {
	// tlc ratio 1.0 and full-drives ratio 2.0 disagree on 2 drive / 2 compute cores, so which applies is observable.
	setRatioConfig(t, 1.0, 2.0)

	v := &clusterDriveComputeCoreRatio{}
	ctx := context.Background()

	tests := []struct {
		name    string
		dynamic *weka.WekaClusterTemplate
		wantErr bool
	}{
		{
			name: "exclusive full-drives (numDrives>0, driveCapacity==0) uses full-drives ratio, flagged",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 1, ComputeCores: 1, NumDrives: 4, DriveCapacity: 0,
			},
			wantErr: true,
		},
		{
			name: "numDrives+driveCapacity>0 is drive-sharing, not exclusive full-drives, uses tlc ratio",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 2, ComputeContainers: 2,
				DriveCores: 1, ComputeCores: 1, NumDrives: 4, DriveCapacity: 2000,
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

// TestClusterDriveComputeCoreRatio_AutoDerivedCores verifies an auto-derived drive-core count (e.g. from
// containerCapacity) is reflected in the ratio check too, not just the user-set spec value.
func TestClusterDriveComputeCoreRatio_AutoDerivedCores(t *testing.T) {
	setRatioConfig(t, 1.0, 2.0)

	// Per-core capacity caps set deterministically; mirrors internal/controllers/allocator/templates_test.go.
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	prevQlc := globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024  // 5120 GiB/core
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 50 * 1024 // 51200 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
		globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = prevQlc
	})

	v := &clusterDriveComputeCoreRatio{}
	ctx := context.Background()

	tests := []struct {
		name    string
		dynamic *weka.WekaClusterTemplate
		wantErr bool
	}{
		{
			// 6000 GiB needs ceil(6000/5120)=2 drive cores; driveSide=2, computeSide=3 >= required=2.
			name: "containerCapacity forces 2 drive cores, still within 1:1 ratio",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 3,
				ContainerCapacity: 6000,
			},
			wantErr: false,
		},
		{
			// driveSide=2, computeSide=2: exactly the 1:1 floor, and ratio required is also 2.
			name: "containerCapacity forces 2 drive cores, computeSide equals driveSide",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 2,
				ContainerCapacity: 6000,
			},
			wantErr: false,
		},
		{
			// 5000 <= 5120 derives to 1 core.
			name: "containerCapacity within one core's worth unaffected",
			dynamic: &weka.WekaClusterTemplate{
				DriveContainers: 1, ComputeContainers: 3,
				ContainerCapacity: 5000,
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
