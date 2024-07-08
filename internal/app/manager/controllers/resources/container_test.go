package resources

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewContainerFactory(t *testing.T) {
	container := &wekav1alpha1.WekaContainer{
		Spec: wekav1alpha1.WekaContainerSpec{
			CpuPolicy: wekav1alpha1.CpuPolicyAuto,
		},
	}
	factory := NewContainerFactory(container)
	if factory == nil {
		t.Errorf("NewContainerFactory() returned nil")
	}
}

func TestCreate(t *testing.T) {
	container := testingContainer()
	factory := NewContainerFactory(container)

	ctx := context.Background()
	pod, err := factory.Create(ctx)
	if err != nil {
		t.Errorf("FormCluster() returned error: %v", err)
		return
	}

	if pod == nil {
		t.Errorf("FormCluster() returned nil")
		return
	}

	if pod.Name != "weka-container" {
		t.Errorf("FormCluster() returned pod with name %s", pod.Name)
		return
	}
}

func testingContainer() *wekav1alpha1.WekaContainer {
	return &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "weka-container",
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			CpuPolicy: wekav1alpha1.CpuPolicyManual, // CpuPolicyAuto panics
			CoreIds:   []int{0, 1},
		},
	}
}
