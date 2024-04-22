package factories

import (
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestNewForCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wekav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Error adding to scheme: %v", err)
	}

	factory := NewWekaContainerFactory(scheme)

	cluster := &wekav1alpha1.WekaCluster{
		Spec: wekav1alpha1.WekaClusterSpec{
			DriveAppendSetupCommand:   "drive",
			ComputeAppendSetupCommand: "compute",
		},
	}
	ownedResources := domain.OwnedResources{}
	template := domain.ClusterTemplate{}
	topology := domain.Topology{}
	role := ""
	i := 0

	container, err := factory.NewForCluster(cluster, ownedResources, template, topology, role, i)
	if err != nil {
		t.Fatalf("Error creating container: %v", err)
	}
	if container == nil {
		t.Fatalf("Container is nil")
	}
}
