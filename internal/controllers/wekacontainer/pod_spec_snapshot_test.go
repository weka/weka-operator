package wekacontainer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/discovery"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func snapshotOf(t *testing.T, c *weka.WekaContainer) string {
	t.Helper()
	b, err := json.Marshal(snapshotPodSpec(c))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPodSpecSnapshotMarshal_WritesEveryKey(t *testing.T) {
	config.Config.PodConfigVersion = "3"
	s := snapshotOf(t, &weka.WekaContainer{})
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"numCores", "extraCores", "hugepages", "hugepagesOffset", "numDrives",
		"additionalMemory", "dpdkBaseMemoryMb", "resources", "tracesConfiguration",
		"podConfigVersion", "podConfigCodeVersion"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing from stamp %s", k, s)
		}
	}
	if string(m["podConfigVersion"]) != `"3"` || string(m["podConfigCodeVersion"]) != `"`+consts.PodConfigCodeVersion+`"` {
		t.Errorf("operator values wrong: %s", s)
	}
}

func TestDiffPodSpecSnapshot_NoChange(t *testing.T) {
	c := &weka.WekaContainer{Spec: weka.WekaContainerSpec{NumCores: 2, Hugepages: 1400}}
	diff, err := diffPodSpecSnapshot(snapshotOf(t, c), snapshotPodSpec(c), true)
	if err != nil || len(diff) != 0 {
		t.Fatalf("diff=%v err=%v", diff, err)
	}
}

func TestDiffPodSpecSnapshot_ReportsChanges(t *testing.T) {
	c := &weka.WekaContainer{Spec: weka.WekaContainerSpec{NumCores: 2, Hugepages: 1400}}
	old := snapshotOf(t, c)
	c.Spec.NumCores, c.Spec.Hugepages = 4, 2800
	diff, err := diffPodSpecSnapshot(old, snapshotPodSpec(c), true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hugepages 1400→2800", "numCores 2→4"}
	if len(diff) != 2 || diff[0] != want[0] || diff[1] != want[1] {
		t.Fatalf("diff=%v want %v", diff, want)
	}
}

func TestDiffPodSpecSnapshot_ZeroToNonZero(t *testing.T) {
	c := &weka.WekaContainer{}
	old := snapshotOf(t, c)
	c.Spec.ExtraCores = 1
	c.Spec.TracesConfiguration = &weka.TracesConfiguration{MaxCapacityPerIoNode: 50}
	diff, err := diffPodSpecSnapshot(old, snapshotPodSpec(c), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 2 {
		t.Fatalf("expected extraCores and tracesConfiguration flagged, got %v", diff)
	}
}

func TestDiffPodSpecSnapshot_MissingKeySkipped(t *testing.T) {
	c := &weka.WekaContainer{Spec: weka.WekaContainerSpec{NumCores: 4}}
	diff, err := diffPodSpecSnapshot(`{"numCores":4}`, snapshotPodSpec(c), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 0 {
		t.Fatalf("keys missing from an older stamp must be skipped, got %v", diff)
	}
}

func TestDiffPodSpecSnapshot_UnknownKeyIgnored(t *testing.T) {
	c := &weka.WekaContainer{}
	s := snapshotOf(t, c)
	s = s[:len(s)-1] + `,"retiredKey":7}`
	diff, err := diffPodSpecSnapshot(s, snapshotPodSpec(c), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 0 {
		t.Fatalf("got %v", diff)
	}
}

func TestDiffPodSpecSnapshot_CodeVersionOnlyWhenEnabled(t *testing.T) {
	c := &weka.WekaContainer{}
	cur := snapshotPodSpec(c)
	old := *cur
	old.PodConfigCodeVersion = "0"
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if diff, err := diffPodSpecSnapshot(s, cur, false); err != nil || len(diff) != 0 {
		t.Fatalf("code version compared while disabled: %v %v", diff, err)
	}
	if diff, err := diffPodSpecSnapshot(s, cur, true); err != nil || len(diff) != 1 {
		t.Fatalf("code version not compared while enabled: %v %v", diff, err)
	}
}

func TestDiffPodSpecSnapshot_ObjectComparedByContent(t *testing.T) {
	// same content, different key order → equal
	c := &weka.WekaContainer{Spec: weka.WekaContainerSpec{TracesConfiguration: &weka.TracesConfiguration{MaxCapacityPerIoNode: 10, EnsureFreeSpace: 5}}}
	s := snapshotOf(t, c)
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.Marshal(m) // map marshal sorts keys
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := diffPodSpecSnapshot(string(reordered), snapshotPodSpec(c), true); err != nil || len(diff) != 0 {
		t.Fatalf("got %v %v", diff, err)
	}
}

func TestSnapshotPodSpec_StampAfterPodCreateMatchesStoredSpec(t *testing.T) {
	stored := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default", UID: "uid"},
		Spec: weka.WekaContainerSpec{
			Image:     "quay.io/weka.io/weka-in-container:4.5.0.100",
			Mode:      weka.WekaContainerModeCompute,
			NumCores:  2,
			CpuPolicy: weka.CpuPolicyShared,
		},
	}
	created := stored.DeepCopy()
	if _, err := resources.NewPodFactory(created, &discovery.DiscoveryNodeInfo{}, nil).Create(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	diff, err := diffPodSpecSnapshot(snapshotOf(t, created), snapshotPodSpec(stored), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 0 {
		t.Fatalf("pod creation must not make the stored spec look changed, got %v", diff)
	}
}
