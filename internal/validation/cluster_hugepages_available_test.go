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

// hugepagesNode builds a Node with the given allocatable hugepages-2Mi (MiB) and labels.
func hugepagesNode(name string, labels map[string]string, allocatableMiB int) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceName("hugepages-2Mi"): *resource.NewQuantity(int64(allocatableMiB)*mib, resource.BinarySI),
			},
		},
	}
}

func TestClusterHugepagesAvailable(t *testing.T) {
	v := &clusterHugepagesAvailable{}
	ctx := context.Background()
	labels := map[string]string{"role": "backend"}

	tests := []struct {
		name      string
		nodeMiB   []int
		dynamic   *weka.WekaClusterTemplate
		wantN     int
		wantSubs  []string
		wantNoSub []string
	}{
		{
			name:    "no hugepages pin is skipped",
			nodeMiB: []int{10000},
			dynamic: &weka.WekaClusterTemplate{DriveContainers: 5, ComputeContainers: 5},
		},
		{
			// The daemonset mode: both counts unset by definition, so the aggregate check cannot run —
			// but the pin still cannot fit any node, and admission must say so.
			name:     "daemonset mode: drive pin above every node is reported without a container count",
			nodeMiB:  []int{10000, 60000},
			dynamic:  &weka.WekaClusterTemplate{DriveHugepages: 90000},
			wantN:    1,
			wantSubs: []string{"driveHugepages", "90000 MiB", "10000 MiB", "At least one matched node cannot host"},
			// No aggregate complaint: 90000 <= 70000 is false, but the count is unset so it must not be checked.
			wantNoSub: []string{"driveContainers"},
		},
		{
			// Only the weak node cannot host the pin, and the planner picks nodes by fit, so this plans
			// cleanly. The warning is still emitted, but it must not claim no node can host the container.
			name:      "heterogeneous fleet: warning names one node, never claims none can host",
			nodeMiB:   []int{8000, 64000, 64000},
			dynamic:   &weka.WekaClusterTemplate{ComputeHugepages: 12000},
			wantN:     1,
			wantSubs:  []string{"computeHugepages", "At least one matched node cannot host", "8000 MiB"},
			wantNoSub: []string{"No matched node"},
		},
		{
			name:     "daemonset mode: compute pin above every node is reported too",
			nodeMiB:  []int{20000},
			dynamic:  &weka.WekaClusterTemplate{ComputeHugepages: 30000},
			wantN:    1,
			wantSubs: []string{"computeHugepages", "30000 MiB", "20000 MiB"},
		},
		{
			name:    "daemonset mode: pin that fits the weakest node is accepted",
			nodeMiB: []int{20000, 60000},
			dynamic: &weka.WekaClusterTemplate{DriveHugepages: 20000},
		},
		{
			// A frontend role's unset count is literal, not operator-derived: no s3 container is created,
			// so a pin no node could host is not a problem to report.
			name:    "frontend role without a count deploys nothing and is skipped",
			nodeMiB: []int{10000},
			dynamic: &weka.WekaClusterTemplate{S3FrontendHugepages: 90000},
		},
		{
			name:      "frontend role with a count is checked like any other",
			nodeMiB:   []int{10000, 90000},
			dynamic:   &weka.WekaClusterTemplate{S3FrontendHugepages: 90000, S3Containers: 1},
			wantN:     1,
			wantSubs:  []string{"s3FrontendHugepages", "10000 MiB", "At least one matched node cannot host"},
			wantNoSub: []string{"s3Containers"}, // one container fits the fleet total; only the weakest node does not
		},
		{
			// Regression guard for the split: with a count set, both checks still apply.
			name:     "counted mode: fits one node but not the fleet",
			nodeMiB:  []int{10000, 10000},
			dynamic:  &weka.WekaClusterTemplate{DriveHugepages: 9000, DriveContainers: 5},
			wantN:    1,
			wantSubs: []string{"driveHugepages", "driveContainers", "45000 MiB", "20000 MiB"},
		},
		{
			name:     "counted mode: fits neither one node nor the fleet reports both",
			nodeMiB:  []int{10000, 10000},
			dynamic:  &weka.WekaClusterTemplate{DriveHugepages: 30000, DriveContainers: 5},
			wantN:    2,
			wantSubs: []string{"At least one matched node cannot host", "Some containers will fail to schedule"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes := make([]*corev1.Node, 0, len(tc.nodeMiB))
			for i, mib := range tc.nodeMiB {
				nodes = append(nodes, hugepagesNode(string(rune('a'+i)), labels, mib))
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
