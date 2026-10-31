package resources

import (
	"testing"

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
	if !IsEmptyNodeSelectorTerm(terms[1]) {
		t.Fatalf("term 1 = %v; want it left empty (matches no node)", terms[1])
	}
}
