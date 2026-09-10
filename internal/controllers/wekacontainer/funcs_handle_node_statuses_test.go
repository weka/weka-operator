package wekacontainer

import (
	"context"
	"reflect"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
)

func nodeWithReady(status v1.ConditionStatus) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status:     v1.NodeStatus{Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: status}}},
	}
}

func newNodeStatusLoop(node *v1.Node, pod *v1.Pod) *containerReconcilerLoop {
	return &containerReconcilerLoop{
		node: node,
		pod:  pod,
		container: &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "client-1", Namespace: "weka"},
			Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient},
			Status:     weka.WekaContainerStatus{NodeAffinity: "node-1", Status: weka.PodTerminating},
		},
	}
}

// stepFor returns the first SimpleStep whose Run is the given method value.
func stepFor(t *testing.T, label string, steps []lifecycle.Step, fn func(context.Context) error) *lifecycle.SimpleStep {
	t.Helper()
	want := reflect.ValueOf(fn).Pointer()
	for _, s := range steps {
		ss, ok := s.(*lifecycle.SimpleStep)
		if ok && ss.Run != nil && reflect.ValueOf(ss.Run).Pointer() == want {
			return ss
		}
	}
	t.Fatalf("step %s not found in flow", label)
	return nil
}

func predicatesPass(step *lifecycle.SimpleStep) bool {
	for _, p := range step.Predicates {
		if !p() {
			return false
		}
	}
	return true
}

func TestNodeIsReadyOrUnset(t *testing.T) {
	cases := map[string]struct {
		node *v1.Node
		want bool
	}{
		"nil node":           {nil, true},
		"ready node":         {nodeWithReady(v1.ConditionTrue), true},
		"notready node":      {nodeWithReady(v1.ConditionFalse), false},
		"unknown node":       {nodeWithReady(v1.ConditionUnknown), false},
		"no ready condition": {&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newNodeStatusLoop(tc.node, nil)
			if got := r.NodeIsReadyOrUnset(); got != tc.want {
				t.Fatalf("NodeIsReadyOrUnset() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNodeAgentStepsSkippedOnNotReadyNode(t *testing.T) {
	prev := config.Config.Metrics.Containers.Enabled
	config.Config.Metrics.Containers.Enabled = true
	t.Cleanup(func() { config.Config.Metrics.Containers.Enabled = prev })

	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "client-1"}}
	for name, tc := range map[string]struct {
		node *v1.Node
		want bool
	}{
		"ready node runs":     {nodeWithReady(v1.ConditionTrue), true},
		"notready node skips": {nodeWithReady(v1.ConditionFalse), false},
		"no node yet runs":    {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNodeStatusLoop(tc.node, pod)
			for _, s := range []struct {
				name  string
				steps []lifecycle.Step
				fn    func(context.Context) error
			}{
				{"SetStatusMetrics", MetricsSteps(r), r.SetStatusMetrics},
				{"RegisterContainerOnMetrics", MetricsSteps(r), r.RegisterContainerOnMetrics},
			} {
				if got := predicatesPass(stepFor(t, s.name, s.steps, s.fn)); got != tc.want {
					t.Fatalf("%s predicates = %v, want %v", s.name, got, tc.want)
				}
			}
		})
	}
}

func TestNodeInfoMismatchStepSkippedOnNotReadyNode(t *testing.T) {
	for name, tc := range map[string]struct {
		node *v1.Node
		want bool
	}{
		"ready node runs":     {nodeWithReady(v1.ConditionTrue), true},
		"notready node skips": {nodeWithReady(v1.ConditionFalse), false},
	} {
		t.Run(name, func(t *testing.T) {
			// live pod, container not Running → deletePodIfNodeInfoMismatch is otherwise eligible
			r := newNodeStatusLoop(tc.node, &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "client-1"}})
			if got := predicatesPass(stepFor(t, "deletePodIfNodeInfoMismatch", ActiveStateFlow(r), r.deletePodIfNodeInfoMismatch)); got != tc.want {
				t.Fatalf("deletePodIfNodeInfoMismatch predicates = %v, want %v", got, tc.want)
			}
		})
	}
}
