package upgrade

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRollingUpgrade_ImageOnly(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	a := &v1alpha1.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
		Spec: v1alpha1.WekaContainerSpec{Image: "old"}, Status: v1alpha1.WekaContainerStatus{LastAppliedImage: "old"}}
	a.Spec.NodeAffinity = "n1"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(a).Build()

	u := NewUpgradeController(c, []*v1alpha1.WekaContainer{a}, "new")
	err := u.RollingUpgrade(context.Background())
	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("RollingUpgrade error = %v, want WaitError after patching the first container", err)
	}
	got := &v1alpha1.WekaContainer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), got); err != nil {
		t.Fatalf("Get container: %v", err)
	}
	if got.Spec.Image != "new" {
		t.Fatalf("image not patched: %q", got.Spec.Image)
	}

	id := 1
	got.Status.ClusterContainerID = &id
	if NewUpgradeController(c, []*v1alpha1.WekaContainer{got}, "new").AreUpgraded() {
		t.Fatal("container still on the old applied image counted as upgraded")
	}
	got.Status.LastAppliedImage = "new"
	if !NewUpgradeController(c, []*v1alpha1.WekaContainer{got}, "new").AreUpgraded() {
		t.Fatal("applied container not counted as upgraded")
	}
}
