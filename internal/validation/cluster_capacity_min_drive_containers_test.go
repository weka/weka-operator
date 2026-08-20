package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withFormClusterMinDriveContainers sets FormClusterMinDriveContainers for the duration of the test,
// restoring the original via t.Cleanup.
func withFormClusterMinDriveContainers(t *testing.T, min int) {
	t.Helper()
	prev := globalconfig.Consts.FormClusterMinDriveContainers
	globalconfig.Consts.FormClusterMinDriveContainers = min
	t.Cleanup(func() {
		globalconfig.Consts.FormClusterMinDriveContainers = prev
	})
}

// TestClusterCapacityMinDriveContainers covers clusterCapacity's structural lower bound (the protection
// scheme's failure-domain floor) against the form-cluster minimum, in the one mode where the
// drive-container count isn't a spec field cluster_min_containers can check directly.
func TestClusterCapacityMinDriveContainers(t *testing.T) {
	v := &clusterCapacityMinDriveContainers{}
	ctx := context.Background()

	tests := []struct {
		name       string
		minDrive   int
		sw, rl, hs int // protection defaults (0 lets the spec value, also 0 in most cases, win)
		dynamic    *weka.WekaClusterTemplate
		wantN      int
		wantSubs   []string
	}{
		{
			name:     "auto-full-drives mode skipped",
			minDrive: 7,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{},
		},
		{
			name:     "count-based mode skipped",
			minDrive: 7,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{DriveContainers: 3, ComputeContainers: 3},
		},
		{
			name:     "pinned driveContainers skipped even with a low floor",
			minDrive: 7,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB", DriveContainers: 3},
		},
		{
			name:     "minimum disabled skipped",
			minDrive: 0,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
		},
		{
			// The shipped configuration: 3+2+0 floor (5) meets the default minimum (5) exactly. This must
			// NOT warn.
			name:     "floor equal to the minimum is allowed",
			minDrive: 5,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
		},
		{
			name:     "floor above the minimum is allowed",
			minDrive: 4,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
		},
		{
			name:     "floor below a raised minimum is warned",
			minDrive: 7,
			sw:       3, rl: 2, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
			wantN:   1,
			wantSubs: []string{
				"stripeWidth=3, redundancyLevel=2, hotSpare=0",
				"floor is 5 drive container(s)",
				"below the 7 weka needs to form a cluster",
				"as small as 5 drive containers",
				"MinContainersNotReady",
				"floor reaches 7",
				"driveContainers to at least 7",
			},
		},
		{
			// Protection left at 0 in the spec falls back to the Helm DriveSharing defaults.
			name:     "protection taken from Helm defaults when the spec leaves them 0",
			minDrive: 7,
			sw:       3, rl: 2, hs: 0,
			dynamic:  &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
			wantN:    1,
			wantSubs: []string{"stripeWidth=3, redundancyLevel=2, hotSpare=0"},
		},
		{
			// The SHIPPED chart leaves protection.stripeWidth/redundancyLevel at 0, so an unset spec
			// resolves to 0+0+0 and MinFdNum() is 0. Reporting "a floor of 0 drive container(s)" with a
			// remedy to lower the minimum to 0 is nonsense; clusterCapacityProtection already rejects the
			// scheme outright, so this rule must stay quiet below the floor.
			name:     "protection below the floor is left to cluster_capacity_protection",
			minDrive: 5,
			sw:       0, rl: 0, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
		},
		{
			name:     "partially sub-floor protection is also left alone",
			minDrive: 7,
			sw:       3, rl: 1, hs: 0,
			dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFormClusterMinDriveContainers(t, tt.minDrive)
			withDefaultProtection(t, tt.sw, tt.rl, tt.hs)

			cluster := &weka.WekaCluster{Spec: weka.WekaClusterSpec{Dynamic: tt.dynamic}}
			// Leave spec protection fields at 0 so the Helm defaults set above take effect.

			errs := v.Validate(ctx, nil, cluster)
			if len(errs) != tt.wantN {
				t.Fatalf("got %d finding(s), want %d: %v", len(errs), tt.wantN, errs)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(errs[0].Detail, sub) {
					t.Errorf("detail missing %q, got: %s", sub, errs[0].Detail)
				}
			}
		})
	}
}
