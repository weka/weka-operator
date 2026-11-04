package wekacontainer

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/weka/weka-operator/internal/services/discovery"
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
	}{
		{"NotReady avoided even when every taint is tolerated", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, nil)}, "b"},
		{"cordon avoided without tolerateCordon", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", true, cordon), node("b", true, nil)}, "b"},
		{"cordon accepted with tolerateCordon", weka.WekaContainerModeDriversBuilder, nil, true, nil,
			[]*v1.Node{node("a", true, cordon)}, "a"},
		{"untolerated taint avoided", weka.WekaContainerModeDriversDist, nil, false, nil,
			[]*v1.Node{node("a", true, gpuTaint), node("b", true, nil)}, "b"},
		{"nodes outside the affinity are neither picked nor avoided", weka.WekaContainerModeDriversBuilder, nil, false, zoneA,
			[]*v1.Node{node("a", true, otherZone), node("b", false, nil), node("c", true, nil)}, "c"},
		{"an empty affinity term admits no node", weka.WekaContainerModeDriversBuilder, tolerateAll, false, zoneAOrEmpty,
			[]*v1.Node{node("a", true, otherZone), node("b", false, nil)}, ""},
		{"a field affinity limits the candidates", weka.WekaContainerModeDriversBuilder, tolerateAll, false, onlyNodeB,
			[]*v1.Node{node("a", true, nil), node("b", false, nil)}, ""},
		{"no eligible node", weka.WekaContainerModeDriversBuilder, tolerateAll, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, cordon)}, ""},
		{"other modes avoid ineligible nodes too", weka.WekaContainerModeClient, nil, false, nil,
			[]*v1.Node{node("a", false, nil), node("b", true, nil)}, "b"},
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

			got, err := r.pickMatchingNode(context.Background())
			if tt.want == "" {
				var waitErr *lifecycle.WaitError
				if !errors.As(err, &waitErr) {
					t.Fatalf("pickMatchingNode = %v, %v; want a wait error", got, err)
				}
				return
			}
			if err != nil || got.Name != tt.want {
				t.Fatalf("pickMatchingNode = %v, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestConstrainPickedPlacement(t *testing.T) {
	const (
		float = "float" // only the not-ready requirement
		pin   = "pin"   // only metadata.name In [n1]
		none  = "none"  // untouched
	)
	tests := []struct {
		name        string
		mode        string
		tolerateAll bool
		want        string
	}{
		{"builder, flag on", weka.WekaContainerModeDriversBuilder, true, float},
		{"dist, flag on", weka.WekaContainerModeDriversDist, true, float},
		{"client, flag on", weka.WekaContainerModeClient, true, float},
		{"adhoc op, flag on", weka.WekaContainerModeAdhocOp, true, float},
		{"adhoc op with container, flag on", weka.WekaContainerModeAdhocOpWC, true, float},
		{"builder, flag off", weka.WekaContainerModeDriversBuilder, false, pin},
		{"dist, flag off", weka.WekaContainerModeDriversDist, false, pin},
		{"client, flag off", weka.WekaContainerModeClient, false, none},
		{"adhoc op, flag off", weka.WekaContainerModeAdhocOp, false, pin},
		{"adhoc op with container, flag off", weka.WekaContainerModeAdhocOpWC, false, pin},
		// Backends float and rely on the scheduler and anti-affinity to spread.
		{"drive, flag on", weka.WekaContainerModeDrive, true, none},
		{"compute, flag on", weka.WekaContainerModeCompute, true, none},
		{"drive, flag off", weka.WekaContainerModeDrive, false, none},
		{"compute, flag off", weka.WekaContainerModeCompute, false, none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &v1.Pod{Spec: v1.PodSpec{Affinity: &v1.Affinity{}}}
			r := &containerReconcilerLoop{
				container:  &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: tt.mode}},
				scheduling: services.SchedulingSettings{ClientsTolerateAll: tt.tolerateAll},
			}
			r.constrainPickedPlacement(pod, "n1")

			if tt.want == none {
				if pod.Spec.Affinity.NodeAffinity != nil {
					t.Errorf("affinity = %v, want untouched", pod.Spec.Affinity.NodeAffinity)
				}
				return
			}
			terms := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			if len(terms) != 1 {
				t.Fatalf("terms = %v, want one", terms)
			}
			if tt.want == float {
				if len(terms[0].MatchExpressions) != 1 || len(terms[0].MatchFields) != 0 ||
					terms[0].MatchExpressions[0].Key != "weka.io/not-ready" || terms[0].MatchExpressions[0].Operator != v1.NodeSelectorOpDoesNotExist {
					t.Errorf("terms = %v, want only the not-ready requirement", terms)
				}
				return
			}
			if len(terms[0].MatchExpressions) != 0 || len(terms[0].MatchFields) != 1 ||
				terms[0].MatchFields[0].Key != "metadata.name" || terms[0].MatchFields[0].Operator != v1.NodeSelectorOpIn || terms[0].MatchFields[0].Values[0] != "n1" {
				t.Errorf("terms = %v, want only metadata.name In [n1]", terms)
			}
		})
	}
}

// pickTestLoop has three Ready nodes a, b and c whose current boot ID is "boot"; discovered maps a node to the
// boot ID recorded in its discovery annotation (nodes not listed have none).
func pickTestLoop(t *testing.T, mode string, scheduling services.SchedulingSettings, discovered map[string]string) *containerReconcilerLoop {
	t.Helper()
	selector := map[string]string{"pool": "x"}
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var objs []client.Object
	for _, name := range []string{"a", "b", "c"} {
		n := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector},
			Status: v1.NodeStatus{
				Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}},
				NodeInfo:   v1.NodeSystemInfo{BootID: "boot"},
			},
		}
		if bootID, ok := discovered[name]; ok {
			n.Annotations = map[string]string{discovery.DiscoveryAnnotation: fmt.Sprintf(`{"boot_id":%q,"schema":%d}`, bootID, discovery.DiscoveryTargetSchema)}
		}
		objs = append(objs, n)
	}
	return &containerReconcilerLoop{
		KubeService: kubernetes.NewKubeService(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()),
		container:   &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: mode, NodeSelector: selector}},
		scheduling:  scheduling,
	}
}

// Every picked container prefers nodes with a usable discovery result, so a retry does not start discovery on
// another node. Drivers builder and dist and ad-hoc helpers take a random one among them, so builders spread and
// a pinned pod can try another node after Pending; any other container takes the first.
func TestPickMatchingNodeSelection(t *testing.T) {
	on := services.SchedulingSettings{ClientsTolerateAll: true}
	off := services.SchedulingSettings{}
	cached := map[string]string{"b": "boot", "c": "boot"}
	tests := []struct {
		name       string
		mode       string
		scheduling services.SchedulingSettings
		discovered map[string]string
		want       []string
	}{
		{"builder, no discovery results: random over all", weka.WekaContainerModeDriversBuilder, off, nil, []string{"a", "b", "c"}},
		{"builder: random among discovered", weka.WekaContainerModeDriversBuilder, off, cached, []string{"b", "c"}},
		{"dist floating: random among discovered", weka.WekaContainerModeDriversDist, on, cached, []string{"b", "c"}},
		{"stale boot ID counts as undiscovered", weka.WekaContainerModeDriversBuilder, off, map[string]string{"b": "boot", "c": "old-boot"}, []string{"b"}},
		{"adhoc helper: random among discovered", weka.WekaContainerModeAdhocOpWC, off, cached, []string{"b", "c"}},
		{"client: first discovered", weka.WekaContainerModeClient, on, cached, []string{"b"}},
		{"client, no discovery results: first node", weka.WekaContainerModeClient, off, nil, []string{"a"}},
		{"drive: first discovered", weka.WekaContainerModeDrive, off, cached, []string{"b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := pickTestLoop(t, tt.mode, tt.scheduling, tt.discovered)
			seen := map[string]bool{}
			for i := 0; i < 200; i++ {
				r.pickedNode = nil
				got, err := r.pickMatchingNode(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				seen[got.Name] = true
			}
			if len(seen) != len(tt.want) {
				t.Fatalf("picked %v, want exactly %v", seen, tt.want)
			}
			for _, name := range tt.want {
				if !seen[name] {
					t.Errorf("picked %v, want exactly %v", seen, tt.want)
				}
			}
		})
	}
}

func TestPickMatchingNodeIsStableWithinAPass(t *testing.T) {
	r := pickTestLoop(t, weka.WekaContainerModeAdhocOp, services.SchedulingSettings{}, nil)
	first, err := r.pickMatchingNode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if got, _ := r.pickMatchingNode(context.Background()); got.Name != first.Name {
			t.Fatalf("pick %d = %s, want %s: the claim sizing and the pod would use different nodes", i, got.Name, first.Name)
		}
	}
}
