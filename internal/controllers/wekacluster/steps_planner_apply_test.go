package wekacluster

import (
	"context"
	"testing"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/client-go/tools/record"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// dynamicTemplate omitted entirely is the shortest documented way to request daemonset mode
// (doc/operator/deployment/act-as-daemonset.md). plannerSizingMode and allocator.IsPlannerManaged
// must agree for every shape of dynamicTemplate, and a nil one must resolve to daemonset mode.
func TestPlannerSizingModeIsNilSafeAndAgreesWithIsPlannerManaged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dynamic  *weka.WekaClusterTemplate
		wantMode plannerSizing
	}{
		{"dynamicTemplate omitted entirely", nil, sizingAutoFullDrives},
		{"empty dynamicTemplate", &weka.WekaClusterTemplate{}, sizingAutoFullDrives},
		{"numDrives pin only", &weka.WekaClusterTemplate{NumDrives: 4}, sizingAutoFullDrives},
		{"driveCores pin only", &weka.WekaClusterTemplate{DriveCores: 3}, sizingAutoFullDrives},
		{"clusterCapacity", &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"}, sizingClusterCapacity},
		{"both container counts", &weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6}, sizingCountBased},
		{"containerCapacity", &weka.WekaClusterTemplate{ContainerCapacity: 20000}, sizingCountBased},
		{"numDrives + driveCapacity", &weka.WekaClusterTemplate{NumDrives: 6, DriveCapacity: 3500}, sizingCountBased},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &weka.WekaClusterSpec{Dynamic: tc.dynamic}

			mode, managed := plannerSizingMode(spec)
			if mode != tc.wantMode {
				t.Errorf("plannerSizingMode = %v, want %v", mode, tc.wantMode)
			}
			if want := tc.wantMode != sizingCountBased; managed != want {
				t.Errorf("plannerManaged = %v, want %v", managed, want)
			}
			if got := allocator.IsPlannerManaged(tc.dynamic); got != managed {
				t.Errorf("allocator.IsPlannerManaged = %v but plannerSizingMode reports %v — the two detection "+
					"sites disagree, which is exactly what left a cluster with no drive containers", got, managed)
			}
		})
	}
}

// The loop-level helper must answer for the loop's own cluster, including when its spec omits dynamicTemplate.
func TestPlannerManagedHelperHandlesNilDynamicTemplate(t *testing.T) {
	r := &wekaClusterReconcilerLoop{cluster: &weka.WekaCluster{}}
	if !r.plannerManaged() {
		t.Error("a cluster with no dynamicTemplate is the daemonset mode and must be planner-managed")
	}
}

// The other half of the B2 regression: detection agreeing is not enough, dispatch must actually route a
// nil-dynamicTemplate cluster into the planner. Verified by observing the planner's inventory seam is
// reached at all.
func TestNilDynamicTemplateReachesThePlanner(t *testing.T) {
	cluster := &weka.WekaCluster{} // Spec.Dynamic stays nil — dynamicTemplate omitted entirely
	calls := 0
	r := &wekaClusterReconcilerLoop{
		cluster:   cluster,
		Recorder:  record.NewFakeRecorder(8),
		Throttler: throttling.NewSyncMapThrottler(),
		buildFullDrivesInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			calls++
			// No signed drives: planAutoFullDrives defers with a WaitError, proving dispatch reached it.
			return map[string]string{}, nil, map[string]bool{}, nil
		},
	}

	if _, err := r.BuildMissingContainers(context.Background()); err == nil {
		t.Fatal("want the no-signed-drives WaitError from the planner, got nil — the planner was never consulted")
	}
	if calls != 1 {
		t.Fatalf("full-drives inventory was consulted %d time(s), want 1 — a cluster that omits dynamicTemplate "+
			"must still be planned as a daemonset", calls)
	}
}
