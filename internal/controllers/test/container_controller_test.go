package test

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/controllers/wekacontainer"
)

type ContainerTestCase struct {
	mode          string
	cpuPolicy     wekav1alpha1.CpuPolicy
	expectedError bool
}

//	func TestWekaContainerController(t *testing.T) {
//		testEnv, err := setupTestEnv(context.Background())
//		if err != nil {
//			t.Fatalf("failed to setup test environment: %v", err)
//		}
//		defer teardownTestEnv(testEnv)
//
//		tests := []ContainerTestCase{
//			{"drive", wekav1alpha1.CpuPolicyDedicated, false},
//			{"compute", wekav1alpha1.CpuPolicyDedicated, false},
//			//{"client", wekav1alpha1.CpuPolicyDedicated, false},
//			{"dist", wekav1alpha1.CpuPolicyDedicated, false},
//			{"drivers-loader", wekav1alpha1.CpuPolicyDedicated, false},
//			{"invalid", wekav1alpha1.CpuPolicyDedicated, true},
//			{"drive", wekav1alpha1.CpuPolicy("invalid"), true},
//		}
//
//		for _, test := range tests {
//			t.Run(fmt.Sprintf("mode=%s,cpupolicy=%s", test.mode, test.cpuPolicy), CanCreateContainer(testEnv, test))
//		}
//	}
func TestNewContainerController(t *testing.T) {
	if true {
		return
	}
	ctx := context.Background()
	testEnv, shutdown, err := setupTestEnv(ctx)
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
		return
	}
	defer shutdown(ctx)
	defer teardownTestEnv(testEnv)

	if testEnv.Manager == nil {
		t.Errorf("failed to create manager")
		return
	}

	subject := wekacontainer.NewContainerController(testEnv.Manager, testEnv.RestClient)
	if subject == nil {
		t.Errorf("NewContainerController() returned nil")
		return
	}

	if subject.Client == nil {
		t.Errorf("NewContainerController() returned controller with nil client")
	}

	if subject.Scheme == nil {
		t.Errorf("NewContainerController() returned controller with nil scheme")
	}
}
