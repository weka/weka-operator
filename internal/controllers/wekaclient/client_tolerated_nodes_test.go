package wekaclient

import (
	"context"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
)

func TestSetToleratedNodesClientsTolerateAll(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	origNamespace := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	setScheduling := func(spec *weka.SchedulingSpec) {
		policy := &weka.WekaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: config.Config.OperatorPodNamespace},
			Spec:       weka.WekaPolicySpec{Payload: weka.PolicyPayload{Configuration: &weka.ConfigurationPayload{Scheduling: spec}}},
		}
		if err := services.RefreshSettings(ctx, fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = services.RefreshSettings(ctx, fake.NewClientBuilder().WithScheme(scheme).Build())
		config.Config.OperatorPodNamespace = origNamespace
	})

	nodes := []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "plain"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cordoned"}, Spec: v1.NodeSpec{Unschedulable: true,
			Taints: []v1.Taint{{Key: "node.kubernetes.io/unschedulable", Effect: v1.TaintEffectNoSchedule}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "noexecute"}, Spec: v1.NodeSpec{
			Taints: []v1.Taint{{Key: "example.com/hold", Value: "true", Effect: v1.TaintEffectNoExecute}}}},
	}
	yes, no := true, false
	cases := []struct {
		name string
		spec *weka.SchedulingSpec
		want []string
	}{
		{"defaults", &weka.SchedulingSpec{}, []string{"plain"}},
		{"tolerateCordon", &weka.SchedulingSpec{TolerateCordon: &yes}, []string{"plain", "cordoned"}},
		{"clientsTolerateAll", &weka.SchedulingSpec{ClientsTolerateAll: &yes, TolerateCordon: &no}, []string{"plain", "cordoned", "noexecute"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setScheduling(tc.spec)
			c := &clientReconcilerLoop{wekaClient: &weka.WekaClient{}, nodes: nodes}
			if err := c.setToleratedNodes(ctx); err != nil {
				t.Fatal(err)
			}
			if len(c.toleratedNodes) != len(tc.want) {
				t.Fatalf("tolerated %v, want %v", c.toleratedNodes, tc.want)
			}
			for _, n := range tc.want {
				if _, ok := c.toleratedNodes[n]; !ok {
					t.Errorf("node %s not tolerated, want %v", n, tc.want)
				}
			}
		})
	}
}
