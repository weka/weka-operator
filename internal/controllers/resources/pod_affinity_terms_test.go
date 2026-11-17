package resources

import (
	"context"
	"slices"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestRequireOnEveryTerm(t *testing.T) {
	req := corev1.NodeSelectorRequirement{Key: "role", Operator: corev1.NodeSelectorOpIn, Values: []string{"builder"}}
	zone := func(z string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
			{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{z}},
		}}
	}

	for _, tc := range []struct {
		name     string
		affinity *corev1.Affinity
		terms    int
	}{
		{"no node affinity", &corev1.Affinity{}, 1},
		{"preferred only", &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1, Preference: zone("a")}},
		}}, 1},
		{"two ORed terms", &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{zone("a"), zone("b")}},
		}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireOnEveryTerm(tc.affinity, req)

			terms := tc.affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			if len(terms) != tc.terms {
				t.Fatalf("got %d terms, want %d", len(terms), tc.terms)
			}
			for i, term := range terms {
				exprs := term.MatchExpressions
				if len(exprs) == 0 || exprs[len(exprs)-1].Key != req.Key {
					t.Fatalf("term %d = %v; want it to end with %v", i, exprs, req)
				}
			}
		})
	}
}

func TestRequireOnEveryTermKeepsEmptyTermsEmpty(t *testing.T) {
	req := corev1.NodeSelectorRequirement{Key: "role", Operator: corev1.NodeSelectorOpIn, Values: []string{"builder"}}
	affinity := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}},
			{},
		}},
	}}

	requireOnEveryTerm(affinity, req)

	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms[0].MatchExpressions) != 2 {
		t.Fatalf("term 0 = %v; want zone and role", terms[0].MatchExpressions)
	}
	if !isEmptyNodeSelectorTerm(terms[1]) {
		t.Fatalf("term 1 = %v; want it left empty (matches no node)", terms[1])
	}
}

// TestSetAffinitiesPinnedIgnoresNodeSelector covers the spec-pinned vs status-bound rule in setAffinities.
func TestSetAffinitiesPinnedIgnoresNodeSelector(t *testing.T) {
	selector := map[string]string{"role": "x"}
	for _, tc := range []struct {
		name       string
		mode       string
		node       weka.NodeName
		statusNode weka.NodeName
		wantKeys   []string
	}{
		{"pinned client", weka.WekaContainerModeClient, "n1", "", []string{"kubernetes.io/hostname"}},
		{"pinned backend", weka.WekaContainerModeCompute, "n1", "", []string{"kubernetes.io/hostname"}},
		{"unbound client", weka.WekaContainerModeClient, "", "", []string{"role"}},
		{"status-bound builder", weka.WekaContainerModeDriversBuilder, "", "n1", []string{"kubernetes.io/hostname", "role"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &PodFactory{container: &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{
					Mode: tc.mode, NodeAffinity: tc.node, NodeSelector: selector, NoAffinityConstraints: true,
				},
				Status: weka.WekaContainerStatus{NodeAffinity: tc.statusNode},
			}}
			pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{}, Containers: []corev1.Container{{}}}}
			if err := f.setAffinities(context.Background(), pod); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions {
				got = append(got, e.Key)
			}
			if !slices.Equal(got, tc.wantKeys) {
				t.Fatalf("required keys = %v, want %v", got, tc.wantKeys)
			}
		})
	}
}
