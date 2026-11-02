package kubernetes

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInRequiresTheKey(t *testing.T) {
	term := v1.NodeSelectorTerm{MatchExpressions: []v1.NodeSelectorRequirement{
		{Key: "node-role.kubernetes.io/worker", Operator: v1.NodeSelectorOpIn, Values: []string{""}},
	}}
	labeled := &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"node-role.kubernetes.io/worker": ""}}}
	if !matchesNodeSelectorTerm(labeled, term) {
		t.Error("labeled node: want match")
	}
	if matchesNodeSelectorTerm(&v1.Node{}, term) {
		t.Error("node without the key: want no match")
	}
}
