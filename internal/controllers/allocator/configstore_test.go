package allocator

import (
	"context"
	"os"
	"testing"

	"github.com/weka/weka-operator/internal/testutil"
	"github.com/weka/weka-operator/pkg/util"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNewConfigMapStore(t *testing.T) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()

	manager, err := newTestingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	client := manager.GetClient()

	store, err := NewConfigMapStore(ctx, client)
	if err != nil {
		t.Fatalf("NewConfigMapStore() error = %v, want nil", err)
	}
	if store == nil {
		t.Fatalf("NewConfigMapStore() = nil, want not nil")
	}
	if store.(*ConfigMapStore).configMap == nil {
		t.Errorf("NewConfigMapStore().configMap = nil, want not nil")
	}
	if store.(*ConfigMapStore).allocations == nil {
		t.Errorf("NewConfigMapStore().allocations = nil, want not nil")
	}
}

func newTestingManager() (testutil.Manager, error) {
	manager, err := testutil.TestingManager()
	if err != nil {
		return nil, err
	}
	manager.SetState(map[string]map[types.NamespacedName]client.Object{
		"*v1.ConfigMap": {
			types.NamespacedName{
				Name:      "weka-operator-allocmap",
				Namespace: util.DevModeNamespace,
			}: newAllocMap(),
		},
	})

	return manager, nil
}

func newAllocMap() *v1.ConfigMap {
	allocations := &Allocations{}
	yamlData, err := yaml.Marshal(allocations)
	if err != nil {
		return nil
	}
	compressedYamlData, err := util.CompressBytes(yamlData)
	if err != nil {
		return nil
	}
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-operator-allocmap",
			Namespace: util.DevModeNamespace,
		},
		BinaryData: map[string][]byte{
			"allocmap.yaml": compressedYamlData,
		},
	}
}
