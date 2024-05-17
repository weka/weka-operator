package factory

import (
	"testing"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestNewWekaContainerForWekaCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := wekav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("error adding to scheme: %s", err)
	}

	subject := NewWekaContainerFactory(scheme)
	if subject == nil {
		t.Fatal("factory is nil")
	}

	cluster := &wekav1alpha1.WekaCluster{}
	ownedResources := domain.OwnedResources{}
	template := domain.ClusterTemplate{}
	topology := domain.Topology{}
	roles := []string{"drive", "compute", "s3"}
	i := 0

	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			container, err := subject.NewWekaContainerForWekaCluster(cluster, ownedResources, template, topology, role, i)
			if err != nil {
				t.Fatalf("error creating container: %s", err)
			}
			if container == nil {
				t.Fatal("container is nil")
			}
		})
	}
}
