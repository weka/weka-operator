package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// withMaxCoresPerContainer sets the config value the validator reads and restores it after the test.
func withMaxCoresPerContainer(t *testing.T, limit int) {
	t.Helper()
	prev := globalconfig.Config.CapacityPlanner.MaxCoresPerContainer
	globalconfig.Config.CapacityPlanner.MaxCoresPerContainer = limit
	t.Cleanup(func() { globalconfig.Config.CapacityPlanner.MaxCoresPerContainer = prev })
}

func TestClusterCoresPerContainerLimit(t *testing.T) {
	v := &clusterCoresPerContainerLimit{}
	ctx := context.Background()

	tests := []struct {
		name     string
		limit    int
		dynamic  *weka.WekaClusterTemplate
		wantN    int
		wantSubs []string
	}{
		{
			name:    "no dynamic template skipped",
			limit:   19,
			dynamic: nil,
		},
		{
			name:    "both unset skipped",
			limit:   19,
			dynamic: &weka.WekaClusterTemplate{},
		},
		{
			name:    "both within the limit",
			limit:   19,
			dynamic: &weka.WekaClusterTemplate{DriveCores: 19, ComputeCores: 1},
		},
		{
			name:     "driveCores above the limit",
			limit:    19,
			dynamic:  &weka.WekaClusterTemplate{DriveCores: 20},
			wantN:    1,
			wantSubs: []string{"driveCores", "20", "limit of 19", "driveContainers"},
		},
		{
			name:     "computeCores above the limit",
			limit:    19,
			dynamic:  &weka.WekaClusterTemplate{ComputeCores: 24},
			wantN:    1,
			wantSubs: []string{"computeCores", "24", "limit of 19", "computeContainers"},
		},
		{
			name:    "both above the limit reported separately",
			limit:   19,
			dynamic: &weka.WekaClusterTemplate{DriveCores: 20, ComputeCores: 21},
			wantN:   2,
		},
		{
			// 0 disables the cap in the planners; admission must agree, not fall back to 19.
			name:    "limit of zero disables the check",
			limit:   0,
			dynamic: &weka.WekaClusterTemplate{DriveCores: 200, ComputeCores: 200},
		},
		{
			// Limit is Helm-configurable; must not be hard-coded to 19.
			name:     "honors a lowered configured limit",
			limit:    8,
			dynamic:  &weka.WekaClusterTemplate{DriveCores: 12},
			wantN:    1,
			wantSubs: []string{"limit of 8"},
		},
		{
			name:    "honors a raised configured limit",
			limit:   32,
			dynamic: &weka.WekaClusterTemplate{DriveCores: 24},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withMaxCoresPerContainer(t, tt.limit)
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
		})
	}
}
