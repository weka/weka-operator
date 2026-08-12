package validation

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateSimpleTolerations(t *testing.T) {
	fld := field.NewPath("spec", "tolerations")
	cases := []struct {
		name     string
		in       []string
		wantErrs int
		wantHint bool // error detail mentions rawTolerations
	}{
		{"empty", nil, 0, false},
		{"empty-string entry expands to valid tolerate-all", []string{""}, 0, false},
		{"bare key", []string{"gpu"}, 0, false},
		{"prefixed key", []string{"scitix.ai/nodecheck"}, 0, false},
		{"key with colon (the OP-361 incident)", []string{"scitix.ai/nodecheck:NoSchedule"}, 1, true},
		{"key with equals", []string{"gpu=true"}, 1, true},
		{"plain invalid chars", []string{"bad key!"}, 1, false},
		{"one good one bad", []string{"gpu", "a:b"}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateSimpleTolerations(fld, tc.in)
			if len(errs) != tc.wantErrs {
				t.Fatalf("got %d errors (%v), want %d", len(errs), errs, tc.wantErrs)
			}
			if tc.wantHint && !strings.Contains(errs[0].Detail, "rawTolerations") {
				t.Fatalf("expected rawTolerations hint in %q", errs[0].Detail)
			}
		})
	}
}

func TestValidateRawTolerations(t *testing.T) {
	fld := field.NewPath("spec", "rawTolerations")
	sec := int64(30)
	cases := []struct {
		name     string
		in       []corev1.Toleration
		wantErrs int
	}{
		{"empty", nil, 0},
		{"full valid", []corev1.Toleration{{Key: "scitix.ai/nodecheck", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}, 0},
		{"empty key with Exists (tolerate everything)", []corev1.Toleration{{Operator: corev1.TolerationOpExists}}, 0},
		{"empty operator means Equal", []corev1.Toleration{{Key: "k", Value: "v"}}, 0},
		{"bad key", []corev1.Toleration{{Key: "a:b", Operator: corev1.TolerationOpExists}}, 1},
		{"bad effect enum", []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Effect: "NoSchedul"}}, 1},
		{"bad operator enum", []corev1.Toleration{{Key: "k", Operator: "Sometimes"}}, 1},
		{"Exists with value", []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Value: "v"}}, 1},
		{"Exists with invalid value reports only must-be-empty", []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Value: "bad value!"}}, 1},
		{"bad value syntax", []corev1.Toleration{{Key: "k", Value: "bad value!"}}, 1},
		{"tolerationSeconds without NoExecute", []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: &sec}}, 1},
		{"tolerationSeconds with NoExecute ok", []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &sec}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validateRawTolerations(fld, tc.in); len(errs) != tc.wantErrs {
				t.Fatalf("got %d errors (%v), want %d", len(errs), errs, tc.wantErrs)
			}
		})
	}
}

func TestValidateLabelMap(t *testing.T) {
	fld := field.NewPath("spec", "nodeSelector")
	cases := []struct {
		name     string
		in       map[string]string
		wantErrs int
	}{
		{"nil", nil, 0},
		{"valid", map[string]string{"weka.io/supports-backends": "true"}, 0},
		{"bad key", map[string]string{"bad key!": "x"}, 1},
		{"bad value", map[string]string{"k": "no spaces allowed"}, 1},
		{"value too long", map[string]string{"k": strings.Repeat("a", 64)}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validateLabelMap(fld, tc.in); len(errs) != tc.wantErrs {
				t.Fatalf("got %d errors (%v), want %d", len(errs), errs, tc.wantErrs)
			}
		})
	}
}

func TestValidateAnnotationMap(t *testing.T) {
	fld := field.NewPath("spec", "roleAnnotations", "compute")
	if errs := validateAnnotationMap(fld, map[string]string{"example.com/scrape": "true"}); len(errs) != 0 {
		t.Fatalf("valid annotations rejected: %v", errs)
	}
	if errs := validateAnnotationMap(fld, map[string]string{"bad key!": "x"}); len(errs) != 1 {
		t.Fatalf("bad annotation key not rejected")
	}
}

func TestValidateTopologyKey(t *testing.T) {
	fld := field.NewPath("spec", "failureDomain", "label")
	if errs := validateTopologyKey(fld, "topology.kubernetes.io/zone"); len(errs) != 0 {
		t.Fatalf("valid topologyKey rejected: %v", errs)
	}
	if errs := validateTopologyKey(fld, "bad key!"); len(errs) != 1 {
		t.Fatalf("bad topologyKey not rejected")
	}
	if errs := validateTopologyKey(fld, ""); len(errs) != 0 {
		t.Fatalf("empty topologyKey should be skipped (field optional): %v", errs)
	}
}

func TestValidateAffinity(t *testing.T) {
	fld := field.NewPath("spec", "affinity")
	good := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "weka.io/mode", Operator: corev1.NodeSelectorOpIn, Values: []string{"backend"}}},
			}},
		},
	}}
	if errs := validateAffinity(fld, good); len(errs) != 0 {
		t.Fatalf("valid affinity rejected: %v", errs)
	}
	badKey := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "bad key!", Operator: corev1.NodeSelectorOpExists}},
			}},
		},
	}}
	if errs := validateAffinity(fld, badKey); len(errs) == 0 {
		t.Fatalf("bad match-expression key not rejected")
	}
	badOp := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "k", Operator: "Maybe"}},
			}},
		},
	}}
	if errs := validateAffinity(fld, badOp); len(errs) == 0 {
		t.Fatalf("bad node-selector operator not rejected")
	}
	if errs := validateAffinity(fld, nil); len(errs) != 0 {
		t.Fatalf("nil affinity should pass: %v", errs)
	}

	podAffinityMissingTopologyKey := &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "weka"}},
		}},
	}}
	if errs := validateAffinity(fld, podAffinityMissingTopologyKey); len(errs) != 1 {
		t.Fatalf("got %d errors (%v), want 1 for missing topologyKey", len(errs), errs)
	}

	podAntiAffinityBadSelector := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"bad key!": "x"}},
			TopologyKey:   "kubernetes.io/hostname",
		}},
	}}
	if errs := validateAffinity(fld, podAntiAffinityBadSelector); len(errs) == 0 {
		t.Fatalf("bad podAntiAffinity labelSelector not rejected")
	}

	podAffinityValid := &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "weka"}},
			TopologyKey:   "kubernetes.io/hostname",
		}},
	}}
	if errs := validateAffinity(fld, podAffinityValid); len(errs) != 0 {
		t.Fatalf("valid podAffinity term rejected: %v", errs)
	}

	podAffinityPreferredMissingTopologyKey := &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 50,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "weka"}},
			},
		}},
	}}
	if errs := validateAffinity(fld, podAffinityPreferredMissingTopologyKey); len(errs) != 1 {
		t.Fatalf("got %d errors (%v), want 1 for preferred term missing topologyKey", len(errs), errs)
	} else if want := "spec.affinity.podAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey"; errs[0].Field != want {
		t.Fatalf("wrong field path: %s (want %s)", errs[0].Field, want)
	}

	// upstream value-count rules per operator
	requiredWithExpr := func(expr corev1.NodeSelectorRequirement) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{expr}}},
			},
		}}
	}
	valueCountCases := []struct {
		name string
		expr corev1.NodeSelectorRequirement
	}{
		{"In without values", corev1.NodeSelectorRequirement{Key: "k", Operator: corev1.NodeSelectorOpIn}},
		{"Exists with values", corev1.NodeSelectorRequirement{Key: "k", Operator: corev1.NodeSelectorOpExists, Values: []string{"v"}}},
		{"Gt with two values", corev1.NodeSelectorRequirement{Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"1", "2"}}},
		{"required In with invalid label value", corev1.NodeSelectorRequirement{Key: "k", Operator: corev1.NodeSelectorOpIn, Values: []string{"bad value!"}}},
	}
	for _, tc := range valueCountCases {
		if errs := validateAffinity(fld, requiredWithExpr(tc.expr)); len(errs) != 1 {
			t.Fatalf("%s: got %d errors (%v), want 1", tc.name, len(errs), errs)
		}
	}

	emptyRequiredTerms := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{},
	}}
	if errs := validateAffinity(fld, emptyRequiredTerms); len(errs) != 1 {
		t.Fatalf("empty required nodeSelectorTerms: got %v, want 1 error", errs)
	}

	// preferred terms: weight range enforced, invalid label values allowed
	badWeight := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
			Weight: 0,
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "k", Operator: corev1.NodeSelectorOpIn, Values: []string{"bad value!"}}},
			},
		}},
	}}
	if errs := validateAffinity(fld, badWeight); len(errs) != 1 {
		t.Fatalf("preferred term weight 0: got %v, want exactly 1 error (invalid values allowed in preferred)", errs)
	} else if want := "spec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].weight"; errs[0].Field != want {
		t.Fatalf("wrong field path: %s (want %s)", errs[0].Field, want)
	}

	badMatchFields := requiredWithExpr(corev1.NodeSelectorRequirement{Key: "k", Operator: corev1.NodeSelectorOpExists})
	badMatchFields.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields = []corev1.NodeSelectorRequirement{
		{Key: "metadata.namespace", Operator: corev1.NodeSelectorOpIn, Values: []string{"x"}},
	}
	if errs := validateAffinity(fld, badMatchFields); len(errs) != 1 {
		t.Fatalf("matchFields with bad key: got %v, want 1 error", errs)
	}
}

func TestValidateRawAffinity(t *testing.T) {
	fld := field.NewPath("spec", "podConfig", "affinity")
	if errs := validateRawAffinity(fld, &runtime.RawExtension{Raw: []byte(`{"nodeAffinity":{}}`)}); len(errs) != 0 {
		t.Fatalf("valid raw affinity rejected: %v", errs)
	}
	if errs := validateRawAffinity(fld, &runtime.RawExtension{Raw: []byte(`{"nodeAffinity": 42}`)}); len(errs) != 1 {
		t.Fatalf("non-affinity JSON not rejected")
	}
	if errs := validateRawAffinity(fld, nil); len(errs) != 0 {
		t.Fatalf("nil raw should pass: %v", errs)
	}
}

func TestValidateTopologySpreadConstraints(t *testing.T) {
	fld := field.NewPath("spec", "topologySpreadConstraints")
	// complete valid constraint; each case below breaks exactly one field
	base := func() corev1.TopologySpreadConstraint {
		return corev1.TopologySpreadConstraint{
			MaxSkew:           1,
			TopologyKey:       "kubernetes.io/hostname",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "weka"}},
		}
	}
	if errs := validateTopologySpreadConstraints(fld, nil); len(errs) != 0 {
		t.Fatalf("nil constraints should pass: %v", errs)
	}
	if errs := validateTopologySpreadConstraints(fld, []corev1.TopologySpreadConstraint{base()}); len(errs) != 0 {
		t.Fatalf("valid constraint rejected: %v", errs)
	}

	nonQualifiedKey := base()
	nonQualifiedKey.TopologyKey = "MY_ZONE KEY"
	if errs := validateTopologySpreadConstraints(fld, []corev1.TopologySpreadConstraint{nonQualifiedKey}); len(errs) != 0 {
		t.Fatalf("non-qualified topologyKey should pass (upstream requires only non-empty): %v", errs)
	}

	cases := []struct {
		name      string
		mutate    func(*corev1.TopologySpreadConstraint)
		wantField string
	}{
		{"empty topologyKey", func(c *corev1.TopologySpreadConstraint) { c.TopologyKey = "" }, "spec.topologySpreadConstraints[0].topologyKey"},
		{"zero maxSkew", func(c *corev1.TopologySpreadConstraint) { c.MaxSkew = 0 }, "spec.topologySpreadConstraints[0].maxSkew"},
		{"negative maxSkew", func(c *corev1.TopologySpreadConstraint) { c.MaxSkew = -1 }, "spec.topologySpreadConstraints[0].maxSkew"},
		{"empty whenUnsatisfiable", func(c *corev1.TopologySpreadConstraint) { c.WhenUnsatisfiable = "" }, "spec.topologySpreadConstraints[0].whenUnsatisfiable"},
		{"bad whenUnsatisfiable enum", func(c *corev1.TopologySpreadConstraint) { c.WhenUnsatisfiable = "Sometimes" }, "spec.topologySpreadConstraints[0].whenUnsatisfiable"},
		{"bad labelSelector", func(c *corev1.TopologySpreadConstraint) {
			c.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"bad key!": "x"}}
		}, "spec.topologySpreadConstraints[0].labelSelector"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			errs := validateTopologySpreadConstraints(fld, []corev1.TopologySpreadConstraint{c})
			if len(errs) != 1 {
				t.Fatalf("got %d errors (%v), want 1", len(errs), errs)
			}
			if !strings.HasPrefix(errs[0].Field, tc.wantField) {
				t.Fatalf("wrong field path: %s (want prefix %s)", errs[0].Field, tc.wantField)
			}
		})
	}
}

func TestValidateRawTopologySpreadConstraints(t *testing.T) {
	fld := field.NewPath("spec", "podConfig", "topologySpreadConstraints")
	if errs := validateRawTopologySpreadConstraints(fld, nil); len(errs) != 0 {
		t.Fatalf("nil raw should pass: %v", errs)
	}
	if errs := validateRawTopologySpreadConstraints(fld, &runtime.RawExtension{Raw: []byte(`[{"topologyKey":"kubernetes.io/hostname","maxSkew":1,"whenUnsatisfiable":"DoNotSchedule"}]`)}); len(errs) != 0 {
		t.Fatalf("valid raw constraints rejected: %v", errs)
	}
	if errs := validateRawTopologySpreadConstraints(fld, &runtime.RawExtension{Raw: []byte(`not json`)}); len(errs) != 1 {
		t.Fatalf("garbage JSON not rejected: %v", errs)
	}
	if errs := validateRawTopologySpreadConstraints(fld, &runtime.RawExtension{Raw: []byte(`[{"topologyKey":"","maxSkew":1,"whenUnsatisfiable":"DoNotSchedule"}]`)}); len(errs) != 1 {
		t.Fatalf("empty topologyKey via raw not rejected: %v", errs)
	}
	// missing maxSkew/whenUnsatisfiable unmarshal to zero values
	if errs := validateRawTopologySpreadConstraints(fld, &runtime.RawExtension{Raw: []byte(`[{"topologyKey":"kubernetes.io/hostname"}]`)}); len(errs) != 2 {
		t.Fatalf("missing maxSkew/whenUnsatisfiable via raw not rejected: %v", errs)
	}
}
