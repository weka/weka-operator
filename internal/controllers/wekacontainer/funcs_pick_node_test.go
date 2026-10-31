package wekacontainer

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func TestPickMatchingNodeEligibility(t *testing.T) {
	selector := map[string]string{"pool": "build"}
	node := func(name string, ready bool, mutate func(*v1.Node)) *v1.Node {
		status := v1.ConditionTrue
		if !ready {
			status = v1.ConditionFalse
		}
		n := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
			Status:     v1.NodeStatus{Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: status}}},
		}
		if mutate != nil {
			mutate(n)
		}
		return n
	}
	cordon := func(n *v1.Node) {
		n.Spec.Unschedulable = true
		n.Spec.Taints = []v1.Taint{resources.CordonTaint()}
	}
	gpuTaint := func(n *v1.Node) {
		n.Spec.Taints = []v1.Taint{{Key: "dedicated", Value: "gpu", Effect: v1.TaintEffectNoSchedule}}
	}
	tolerateAll := []v1.Toleration{{Operator: v1.TolerationOpExists}}

	otherZone := func(n *v1.Node) { n.Labels = map[string]string{"pool": "build", "zone": "b"} }
	zoneA := &v1.Affinity{NodeAffinity: &v1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
		NodeSelectorTerms: []v1.NodeSelectorTerm{{MatchExpressions: []v1.NodeSelectorRequirement{
			{Key: "zone", Operator: v1.NodeSelectorOpNotIn, Values: []string{"b"}},
		}}},
	}}}

	zoneAOrEmpty := &v1.Affinity{NodeAffinity: &v1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
		NodeSelectorTerms: []v1.NodeSelectorTerm{zoneA.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0], {}},
	}}}

	onlyNodeB := &v1.Affinity{NodeAffinity: &v1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
		NodeSelectorTerms: []v1.NodeSelectorTerm{{MatchFields: []v1.NodeSelectorRequirement{
			{Key: "metadata.name", Operator: v1.NodeSelectorOpIn, Values: []string{"b"}},
		}}},
	}}}

	tests := []struct {
		name           string
		mode           string
		tolerations    []v1.Toleration
		tolerateCordon bool
		affinity       *v1.Affinity
		nodes          []*v1.Node
		want           string // "" expects a wait error
		wantAvoid      []string
	}{
		{"NotReady avoided even when every taint is tolerated", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, nil)}, "b", []string{"a"}},
		{"cordon avoided without tolerateCordon", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", true, cordon), node("b", true, nil)}, "b", []string{"a"}},
		{"cordon accepted with tolerateCordon", weka.WekaContainerModeDriversBuilder, nil, true, nil,
			[]*v1.Node{node("a", true, cordon)}, "a", nil},
		{"untolerated taint avoided", weka.WekaContainerModeDriversDist, nil, false, nil,
			[]*v1.Node{node("a", true, gpuTaint), node("b", true, nil)}, "b", []string{"a"}},
		{"nodes outside the affinity are neither picked nor avoided", weka.WekaContainerModeDriversBuilder, nil, false, zoneA,
			[]*v1.Node{node("a", true, otherZone), node("b", false, nil), node("c", true, nil)}, "c", []string{"b"}},
		{"an empty affinity term admits no node", weka.WekaContainerModeDriversBuilder, tolerateAll, false, zoneAOrEmpty,
			[]*v1.Node{node("a", true, otherZone), node("b", false, nil)}, "", nil},
		{"a field affinity limits the candidates", weka.WekaContainerModeDriversBuilder, tolerateAll, false, onlyNodeB,
			[]*v1.Node{node("a", true, nil), node("b", false, nil)}, "", nil},
		{"no eligible node", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, cordon)}, "", nil},
		{"other modes avoid ineligible nodes too", weka.WekaContainerModeClient, nil, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, nil)}, "b", []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			objs := make([]client.Object, 0, len(tt.nodes))
			for _, n := range tt.nodes {
				objs = append(objs, n)
			}
			r := &containerReconcilerLoop{
				KubeService: kubernetes.NewKubeService(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()),
				container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
					Mode: tt.mode, NodeSelector: selector, Tolerations: tt.tolerations, Affinity: tt.affinity,
				}},
				scheduling: services.SchedulingSettings{TolerateCordon: tt.tolerateCordon},
			}

			got, avoid, err := r.pickMatchingNode(context.Background())
			if tt.want == "" {
				var waitErr *lifecycle.WaitError
				if !errors.As(err, &waitErr) {
					t.Fatalf("pickMatchingNode = %v, %v; want a wait error", got, err)
				}
				return
			}
			if err != nil || got.Name != tt.want || !slices.Equal(avoid, tt.wantAvoid) {
				t.Fatalf("pickMatchingNode = %v, %v, %v; want %s, %v", got, avoid, err, tt.want, tt.wantAvoid)
			}
		})
	}
}

func TestExcludeNodesExcludesByNameInEveryTerm(t *testing.T) {
	term := func() v1.NodeSelectorTerm {
		return v1.NodeSelectorTerm{MatchExpressions: []v1.NodeSelectorRequirement{
			{Key: "kubernetes.io/os", Operator: v1.NodeSelectorOpIn, Values: []string{"linux"}},
		}}
	}
	pod := &v1.Pod{Spec: v1.PodSpec{Affinity: &v1.Affinity{NodeAffinity: &v1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{NodeSelectorTerms: []v1.NodeSelectorTerm{term(), term(), {}}},
	}}}}

	excludeNodes(pod, []string{"a", "b"})

	want := []v1.NodeSelectorRequirement{
		{Key: "metadata.name", Operator: v1.NodeSelectorOpNotIn, Values: []string{"a"}},
		{Key: "metadata.name", Operator: v1.NodeSelectorOpNotIn, Values: []string{"b"}},
	}
	terms := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms[2].MatchFields) != 0 {
		t.Fatalf("empty term = %v; want it left empty (matches no node)", terms[2])
	}
	for i, term := range terms[:2] {
		if len(term.MatchExpressions) != 1 || !reflect.DeepEqual(term.MatchFields, want) {
			t.Fatalf("term %d = %v / %v; want the os requirement kept and fields %v", i, term.MatchExpressions, term.MatchFields, want)
		}
	}
}
