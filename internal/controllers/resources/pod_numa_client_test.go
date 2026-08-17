package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// Client-mode container with numa region 0: covers the region-index-zero edge (a *int
// pointing at 0 must not be treated as unset) and the client resource path, which the
// compute-mode cases in pod_numa_test.go don't exercise.
func TestSetResources_NumaClientModeRegionZero(t *testing.T) {
	region0 := 0
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "client-container"},
		Spec: weka.WekaContainerSpec{
			Mode:      weka.WekaContainerModeClient,
			NumCores:  4,
			CpuPolicy: weka.CpuPolicyDedicatedHT,
			Hugepages: 1400,
			Numa: &weka.WekaNuma{
				Single: true,
				Region: &region0,
				Method: weka.WekaNumaMethodDevicePlugin,
			},
		},
	}
	factory := NewPodFactory(container, &discovery.DiscoveryNodeInfo{}, &domain.FeatureFlags{})
	pod := makePod(weka.CpuPolicyDedicatedHT)

	if err := factory.setResources(context.Background(), pod, minimalHgDetails()); err != nil {
		t.Fatalf("setResources: %v", err)
	}
	if _, ok := pod.Spec.Containers[0].Resources.Requests["weka.io/numa-region-0"]; !ok {
		t.Fatalf("expected weka.io/numa-region-0 in requests, got: %v", pod.Spec.Containers[0].Resources.Requests)
	}
	if _, ok := pod.Spec.Containers[0].Resources.Limits["weka.io/numa-region-0"]; !ok {
		t.Fatalf("expected weka.io/numa-region-0 in limits, got: %v", pod.Spec.Containers[0].Resources.Limits)
	}
}
