package allocator

import (
	"context"
	"testing"

	"github.com/weka/weka-operator/internal/testutil"
)

func TestNewConfigMapStore(t *testing.T) {
	ctx := context.Background()
	manager, err := testutil.TestingManager()
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
