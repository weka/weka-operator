package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

func TestClusterNumDrivesBelowRequiredCores(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024 // 5120 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
	})

	v := &clusterNumDrivesBelowRequiredCores{}
	ctx := context.Background()

	tests := []struct {
		name     string
		dynamic  *weka.WekaClusterTemplate
		wantErr  bool
		wantSubs []string
	}{
		{
			name:    "no dynamic template skipped",
			dynamic: nil,
			wantErr: false,
		},
		{
			name: "containerCapacity mode skipped: no drive count to bound cores",
			dynamic: &weka.WekaClusterTemplate{
				ContainerCapacity: 40000, // ceil(40000/5120)=8, but there is no numDrives to compare against
			},
			wantErr: false,
		},
		{
			name: "pure full-drives mode skipped: no driveCapacity",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives: 4,
			},
			wantErr: false,
		},
		{
			name: "clusterCapacity skipped: planner assigns cores itself",
			dynamic: &weka.WekaClusterTemplate{
				ClusterCapacity: "1PiB",
				NumDrives:       4,
				DriveCapacity:   8000,
			},
			wantErr: false,
		},
		{
			// 4 drives x 2000 GiB = 8000 GiB, ceil(8000/5120)=2 <= numDrives=4: reachable, no finding.
			name: "required cores at or below numDrives is reachable",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 2000,
			},
			wantErr: false,
		},
		{
			// 4 drives x 1280 GiB = 5120 GiB, ceil(5120/5120)=1: the boundary case, still reachable.
			name: "required cores exactly at the per-core capacity is reachable",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 1280,
			},
			wantErr: false,
		},
		{
			// 4 drives x 8000 GiB = 32000 GiB, ceil(32000/5120)=7 > numDrives=4: unreachable at any legal
			// driveCores, since CEL caps driveCores at numDrives.
			name: "required cores above numDrives is unreachable",
			dynamic: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 8000,
			},
			wantErr: true,
			wantSubs: []string{
				"driveCapacity (8000 GiB per drive × numDrives 4 = 32000 GiB) needs 7 drive core(s)",
				"numDrives caps driveCores at 4",
				"Raising numDrives does not help",
				"Lower driveCapacity to at most 5120 GiB",
				"clusterCapacity.tlcCapacityPerCoreGiB",
				"switch to spec.dynamicTemplate.containerCapacity",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{Spec: weka.WekaClusterSpec{Dynamic: tt.dynamic}}
			errs := v.Validate(ctx, nil, cluster)
			if tt.wantErr && len(errs) == 0 {
				t.Fatalf("expected an error, got none")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Fatalf("expected no error, got %v", errs)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(errs[0].Detail, sub) {
					t.Errorf("detail missing %q, got: %s", sub, errs[0].Detail)
				}
			}
		})
	}
}
