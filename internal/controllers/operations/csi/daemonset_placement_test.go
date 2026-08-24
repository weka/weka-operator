package csi

import (
	"reflect"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
)

// An empty selector means "run everywhere" today. A retain-only term would narrow that to "retained
// nodes only", and a term with no matchExpressions is rejected by API validation outright — so the
// empty case must render no affinity at all.
func TestBuildCsiNodeAffinity_EmptySelectorKeepsRunEverywhere(t *testing.T) {
	for name, selector := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
	} {
		if got := buildCsiNodeAffinity(selector, "weka.io/csi-node-retain.default.clients"); got != nil {
			t.Errorf("%s selector: expected no affinity, got %+v", name, got)
		}
	}
}

func TestBuildCsiNodeAffinity_TwoTermsSelectorAndRetain(t *testing.T) {
	retain := "weka.io/csi-node-retain.default.clients"
	affinity := buildCsiNodeAffinity(map[string]string{
		"weka.io/supports-clients": "true",
		"kubernetes.io/os":         "linux",
	}, retain)

	if affinity == nil || affinity.NodeAffinity == nil ||
		affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("expected required node affinity, got %+v", affinity)
	}

	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 2 {
		t.Fatalf("expected 2 terms (selector OR retain), got %d", len(terms))
	}

	// Term 1 carries the user's selector literally, sorted by key.
	want := []corev1.NodeSelectorRequirement{
		{Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"linux"}},
		{Key: "weka.io/supports-clients", Operator: corev1.NodeSelectorOpIn, Values: []string{"true"}},
	}
	if !reflect.DeepEqual(terms[0].MatchExpressions, want) {
		t.Errorf("term 0 mismatch:\n got %+v\nwant %+v", terms[0].MatchExpressions, want)
	}

	// Term 2 is the retain claim alone — it must not also require the selector, or it could never
	// match a node that just lost the label.
	wantRetain := []corev1.NodeSelectorRequirement{
		{Key: retain, Operator: corev1.NodeSelectorOpIn, Values: []string{CsiNodeRetainLabelValue}},
	}
	if !reflect.DeepEqual(terms[1].MatchExpressions, wantRetain) {
		t.Errorf("term 1 mismatch:\n got %+v\nwant %+v", terms[1].MatchExpressions, wantRetain)
	}
}

// Go randomizes map iteration and this output is hashed to decide whether to roll the DaemonSet.
// Unsorted terms would flip the hash between reconciles and thrash every csi-node pod in the cluster.
func TestBuildCsiNodeAffinity_DeterministicAcrossIterations(t *testing.T) {
	selector := map[string]string{
		"weka.io/supports-clients": "true",
		"kubernetes.io/os":         "linux",
		"kubernetes.io/arch":       "amd64",
		"topology.kubernetes.io/z": "a",
		"node.weka.io/pool":        "clients",
	}

	first := buildCsiNodeAffinity(selector, "weka.io/csi-node-retain.default.clients")
	for i := 0; i < 200; i++ {
		if got := buildCsiNodeAffinity(selector, "weka.io/csi-node-retain.default.clients"); !reflect.DeepEqual(first, got) {
			t.Fatalf("affinity is not deterministic (iteration %d):\n got %+v\nwant %+v", i, got, first)
		}
	}
}

func testWekaClient(selector map[string]string) *weka.WekaClient {
	return &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Name: "clients", Namespace: "default"},
		Spec:       weka.WekaClientSpec{NodeSelector: selector},
	}
}

func TestGetCsiNodeDaemonSetHash_StableAcrossIterations(t *testing.T) {
	config.Config.Csi.WekafsImage = "test-csi-image"
	wekaClient := testWekaClient(map[string]string{
		"weka.io/supports-clients": "true",
		"kubernetes.io/os":         "linux",
		"kubernetes.io/arch":       "amd64",
	})

	first, err := GetCsiNodeDaemonSetHash("csi", wekaClient, "clients", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := GetCsiNodeDaemonSetHash("csi", wekaClient, "clients", "default")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != first {
			t.Fatalf("hash is not stable (iteration %d): got %q want %q", i, got, first)
		}
	}
}

func TestGetCsiNodeDaemonSetHash_ChangesWithSelector(t *testing.T) {
	config.Config.Csi.WekafsImage = "test-csi-image"

	withSelector, err := GetCsiNodeDaemonSetHash("csi", testWekaClient(map[string]string{"weka.io/supports-clients": "true"}), "clients", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	empty, err := GetCsiNodeDaemonSetHash("csi", testWekaClient(nil), "clients", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if withSelector == empty {
		t.Error("hash must distinguish a client with a node selector from one without")
	}
}

// Pod NodeSelector and nodeAffinity are ANDed. Leaving NodeSelector populated would make the retain
// term dead code and reproduce the deadlock exactly, so assert placement lives only in affinity.
func TestNewCsiNodeDaemonSet_PlacementOnlyInAffinity(t *testing.T) {
	config.Config.Csi.WekafsImage = "test-csi-image"
	wekaClient := testWekaClient(map[string]string{"weka.io/supports-clients": "true"})

	ds, err := NewCsiNodeDaemonSet(t.Context(), "csi", wekaClient, "clients", "default", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	podSpec := ds.Spec.Template.Spec
	if podSpec.NodeSelector != nil {
		t.Errorf("pod NodeSelector must be nil (it ANDs with affinity), got %+v", podSpec.NodeSelector)
	}
	if podSpec.Affinity == nil || podSpec.Affinity.NodeAffinity == nil {
		t.Fatalf("expected node affinity on the pod template, got %+v", podSpec.Affinity)
	}
	if terms := podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms; len(terms) != 2 {
		t.Errorf("expected 2 node selector terms, got %d", len(terms))
	}
}

// The key must be per-client: several WekaClients can select overlapping nodes, and a shared key
// would let one client's teardown strand another client's still-mounted plugin.
func TestGetCsiNodeRetainLabel_PerClientAndBounded(t *testing.T) {
	a := GetCsiNodeRetainLabel("default", "clients")
	b := GetCsiNodeRetainLabel("default", "other-clients")
	c := GetCsiNodeRetainLabel("other", "clients")

	if a == b || a == c || b == c {
		t.Errorf("keys must differ per client: %q %q %q", a, b, c)
	}
	if a != GetCsiNodeRetainLabel("default", "clients") {
		t.Error("key must be deterministic")
	}

	long := GetCsiNodeRetainLabel(strings.Repeat("n", 40), strings.Repeat("c", 40))
	name := long[strings.Index(long, "/")+1:]
	if len(name) > 63 {
		t.Errorf("label name segment must be <= 63 chars, got %d (%q)", len(name), name)
	}
	if GetCsiNodeRetainLabel(strings.Repeat("n", 40), strings.Repeat("c", 41)) == long {
		t.Error("truncated keys must stay unique across different clients")
	}
}
