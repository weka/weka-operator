package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// coresNode builds a Node with the given allocatable CPU (whole cores) and labels.
func coresNode(name string, labels map[string]string, allocatableCores int) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: *resource.NewQuantity(int64(allocatableCores), resource.DecimalSI),
			},
		},
	}
}

func TestClusterCoresAvailable(t *testing.T) {
	v := &clusterCoresAvailable{}
	ctx := context.Background()
	labels := map[string]string{"role": "backend"}

	tests := []struct {
		name      string
		nodeCores []int
		dynamic   *weka.WekaClusterTemplate
		wantN     int
		wantSubs  []string
		wantNoSub []string
	}{
		{
			name:      "no cores pin is skipped",
			nodeCores: []int{10},
			dynamic:   &weka.WekaClusterTemplate{DriveContainers: 5, ComputeContainers: 5},
		},
		{
			// The daemonset mode: both counts unset by definition, so the aggregate check cannot run —
			// but the pin still cannot fit any node, and admission must say so.
			name:      "daemonset mode: drive pin above every node is reported without a container count",
			nodeCores: []int{10, 60},
			dynamic:   &weka.WekaClusterTemplate{DriveCores: 90},
			wantN:     1,
			wantSubs:  []string{"driveCores", "90 cores", "10000m", "At least one matched node cannot host"},
			// No aggregate complaint: the count is unset so it must not be checked.
			wantNoSub: []string{"driveContainers"},
		},
		{
			// Only the weak node cannot host the pin, and the planner picks nodes by fit, so this plans
			// cleanly. The warning is still emitted, but it must not claim no node can host the container.
			name:      "heterogeneous fleet: warning names one node, never claims none can host",
			nodeCores: []int{8, 64, 64},
			dynamic:   &weka.WekaClusterTemplate{ComputeCores: 12},
			wantN:     1,
			wantSubs:  []string{"computeCores", "At least one matched node cannot host", "8000m"},
			wantNoSub: []string{"No matched node"},
		},
		{
			name:      "daemonset mode: compute pin above every node is reported too",
			nodeCores: []int{20},
			dynamic:   &weka.WekaClusterTemplate{ComputeCores: 30},
			wantN:     1,
			wantSubs:  []string{"computeCores", "30 cores", "20000m"},
			wantNoSub: []string{"computeContainers"},
		},
		{
			name:      "daemonset mode: pin that fits the weakest node is accepted",
			nodeCores: []int{20, 60},
			dynamic:   &weka.WekaClusterTemplate{DriveCores: 20},
		},
		{
			// A frontend role's unset count is literal, not operator-derived: no s3 container is created,
			// so a pin no node could host is not a problem to report.
			name:      "frontend role without a count deploys nothing and is skipped",
			nodeCores: []int{10},
			dynamic:   &weka.WekaClusterTemplate{S3Cores: 90},
		},
		{
			// Regression guard for the split: with a count set, both checks still apply.
			name:      "counted mode: fits one node but not the fleet",
			nodeCores: []int{10, 10},
			dynamic:   &weka.WekaClusterTemplate{DriveCores: 9, DriveContainers: 5},
			wantN:     1,
			wantSubs:  []string{"driveCores", "driveContainers", "45 cores", "20000m"},
		},
		{
			name:      "counted mode: fits neither one node nor the fleet reports both",
			nodeCores: []int{10, 10},
			dynamic:   &weka.WekaClusterTemplate{DriveCores: 30, DriveContainers: 5},
			wantN:     2,
			wantSubs:  []string{"At least one matched node cannot host", "Some containers will fail to schedule"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes := make([]*corev1.Node, 0, len(tc.nodeCores))
			for i, cores := range tc.nodeCores {
				nodes = append(nodes, coresNode(string(rune('a'+i)), labels, cores))
			}
			cluster := &weka.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
				Spec:       weka.WekaClusterSpec{NodeSelector: labels, Dynamic: tc.dynamic},
			}
			errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), cluster)
			if len(errs) != tc.wantN {
				t.Fatalf("got %d error(s), want %d: %v", len(errs), tc.wantN, errs)
			}
			joined := errs.ToAggregate()
			text := ""
			if joined != nil {
				text = joined.Error()
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(text, sub) {
					t.Errorf("message missing %q; got: %s", sub, text)
				}
			}
			for _, sub := range tc.wantNoSub {
				if strings.Contains(text, sub) {
					t.Errorf("message unexpectedly contains %q; got: %s", sub, text)
				}
			}
		})
	}
}
