package node

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func testNode(ready corev1.ConditionStatus, labelled bool) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}},
	}
	if labelled {
		n.Labels[domain.WekaLabelNotReady] = "true"
	}
	return n
}

func TestReconcileNodeLabel(t *testing.T) {
	tests := []struct {
		name        string
		ready       corev1.ConditionStatus
		labelled    bool
		tolerateAll bool
		wantLabel   bool
		wantPatch   bool
	}{
		{"flag on, not ready: labelled", corev1.ConditionFalse, false, true, true, true},
		{"flag on, unreachable: labelled", corev1.ConditionUnknown, false, true, true, true},
		{"flag on, ready: label removed", corev1.ConditionTrue, true, true, false, true},
		{"flag on, ready, unlabelled: untouched", corev1.ConditionTrue, false, true, false, false},
		{"flag on, not ready, labelled: untouched", corev1.ConditionFalse, true, true, true, false},
		{"flag off, not ready: label removed", corev1.ConditionFalse, true, false, false, true},
		{"flag off, not ready, unlabelled: untouched", corev1.ConditionFalse, false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			patches := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testNode(tt.ready, tt.labelled)).
				WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patches++
					return c.Patch(ctx, obj, patch, opts...)
				}}).Build()

			res, err := (&NodeReconciler{Client: c}).reconcileNode(context.Background(), "n1", tt.tolerateAll)
			if err != nil {
				t.Fatal(err)
			}
			if (res.RequeueAfter > 0) != tt.tolerateAll {
				t.Errorf("RequeueAfter = %v, want a resync only with the flag on", res.RequeueAfter)
			}

			got := &corev1.Node{}
			if err := c.Get(context.Background(), client.ObjectKey{Name: "n1"}, got); err != nil {
				t.Fatal(err)
			}
			if isLabelled(got) != tt.wantLabel {
				t.Errorf("labelled = %v, want %v", isLabelled(got), tt.wantLabel)
			}
			if (patches > 0) != tt.wantPatch {
				t.Errorf("patches = %d, want patch = %v", patches, tt.wantPatch)
			}
		})
	}
}

func TestReadinessChangedPredicate(t *testing.T) {
	update := func(oldN, newN *corev1.Node) bool {
		return readinessChanged.Update(event.UpdateEvent{ObjectOld: oldN, ObjectNew: newN})
	}
	heartbeat := testNode(corev1.ConditionTrue, false)
	heartbeat.Status.Conditions[0].LastHeartbeatTime = metav1.Now()
	annotated := testNode(corev1.ConditionTrue, false)
	annotated.Annotations = map[string]string{"x": "y"}

	if update(testNode(corev1.ConditionTrue, false), heartbeat) || update(testNode(corev1.ConditionTrue, false), annotated) {
		t.Error("heartbeat or annotation update passed the predicate")
	}
	if update(testNode(corev1.ConditionFalse, false), testNode(corev1.ConditionUnknown, false)) {
		t.Error("False to Unknown passed the predicate")
	}
	if !update(testNode(corev1.ConditionTrue, false), testNode(corev1.ConditionFalse, false)) {
		t.Error("Ready change was filtered")
	}
	if !update(testNode(corev1.ConditionFalse, true), testNode(corev1.ConditionFalse, false)) {
		t.Error("label removal was filtered")
	}
}

func TestConfigurationChangeEnqueuesAndLabelsEveryNotReadyNode(t *testing.T) {
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = "" })

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: "weka-operator-system"},
		Spec: weka.WekaPolicySpec{Payload: weka.PolicyPayload{Configuration: &weka.ConfigurationPayload{
			Scheduling: &weka.SchedulingSpec{ClientsTolerateAll: ptr.To(true)},
		}}},
	}
	names := []string{"a", "b", "c"}
	objs := []client.Object{policy}
	for i, name := range names {
		n := testNode(corev1.ConditionFalse, false)
		if i == 2 {
			n = testNode(corev1.ConditionTrue, false)
		}
		n.Name = name
		objs = append(objs, n)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &NodeReconciler{Client: c}

	requests := r.allNodesOnConfigurationChange(context.Background(), policy)
	if len(requests) != len(names) {
		t.Fatalf("enqueued %d nodes, want %d", len(requests), len(names))
	}
	for _, req := range requests {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range names {
		got := &corev1.Node{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: name}, got); err != nil {
			t.Fatal(err)
		}
		if want := i != 2; isLabelled(got) != want {
			t.Errorf("node %s labelled = %v, want %v", name, isLabelled(got), want)
		}
	}

	if got := r.allNodesOnConfigurationChange(context.Background(), &weka.WekaPolicy{}); got != nil {
		t.Errorf("a non-configuration policy enqueued %d nodes", len(got))
	}
}

func TestConfigurationChangeEnqueuesEveryNodeWhenRefreshFails(t *testing.T) {
	config.Config.OperatorPodNamespace = ""
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a, b := testNode(corev1.ConditionFalse, false), testNode(corev1.ConditionFalse, false)
	a.Name, b.Name = "a", "b"
	r := &NodeReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, b).Build()}
	policy := &weka.WekaPolicy{Spec: weka.WekaPolicySpec{Payload: weka.PolicyPayload{Configuration: &weka.ConfigurationPayload{}}}}

	if got := r.allNodesOnConfigurationChange(context.Background(), policy); len(got) != 2 {
		t.Errorf("enqueued %d nodes with the refresh failing, want 2", len(got))
	}
}
