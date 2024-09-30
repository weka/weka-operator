package discovery

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/testutil"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetOwnedContainers(t *testing.T) {
	ctx := context.Background()
	manager, err := testutil.TestingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	mode := "mode"
	owner := types.UID("test-uid")
	state := map[string]map[types.NamespacedName]client.Object{
		"*v1alpha1.WekaContainer": {
			types.NamespacedName{
				Name:      "not-owned",
				Namespace: "test-namespace",
			}: makeContainer(types.UID(""), ""),
			types.NamespacedName{
				Name:      "is-owned",
				Namespace: "test-namespace",
			}: makeContainer(owner, mode),
		},
	}
	manager.SetState(state)
	client := manager.GetClient()

	allContainers := &wekav1alpha1.WekaContainerList{}
	err = client.List(ctx, allContainers)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(allContainers.Items) == 0 {
		t.Fatalf("No containers found")
	}

	namespace := "test-namespace"

	containers, err := GetOwnedContainers(ctx, client, owner, namespace, mode)
	if err != nil {
		t.Errorf("GetOwnedContainers() error = %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("GetOwnedContainers() = %v, want %v", len(containers), 1)
	}
}

func makeContainer(ownerReference types.UID, mode string) *wekav1alpha1.WekaContainer {
	container := &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-container",
			Namespace: "test-namespace",
		},
	}
	if mode != "" {
		container.Labels = map[string]string{"weka.io/mode": mode}
	}
	container.OwnerReferences = []metav1.OwnerReference{
		{
			UID: ownerReference,
		},
	}
	return container
}
