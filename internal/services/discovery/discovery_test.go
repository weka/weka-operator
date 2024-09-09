package discovery

import (
	"context"
	"testing"

	"github.com/weka/weka-operator/internal/testutil"
	"k8s.io/apimachinery/pkg/types"
)

func TestGetOwnedContainers(t *testing.T) {
	ctx := context.Background()
	manager, err := testutil.TestingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}
	client := manager.GetClient()
	owner := types.UID("test-uid")
	namespace := "test-namespace"
	mode := "mode"

	containers, err := GetOwnedContainers(ctx, client, owner, namespace, mode)
	if err != nil {
		t.Errorf("GetOwnedContainers() error = %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("GetOwnedContainers() = %v, want %v", len(containers), 0)
	}
}
