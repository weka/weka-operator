package controllers

import (
	"context"
	"fmt"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ContainerTestCase struct {
	mode          string
	cpuPolicy     wekav1alpha1.CpuPolicy
	expectedError bool
}

func TestWekaContainerController(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	defer teardownTestEnv(testEnv)

	tests := []ContainerTestCase{
		{"drive", wekav1alpha1.CpuPolicyDedicated, false},
		{"compute", wekav1alpha1.CpuPolicyDedicated, false},
		{"client", wekav1alpha1.CpuPolicyDedicated, false},
		{"dist", wekav1alpha1.CpuPolicyDedicated, false},
		{"drivers-loader", wekav1alpha1.CpuPolicyDedicated, false},
		{"invalid", wekav1alpha1.CpuPolicyDedicated, true},
		{"drive", wekav1alpha1.CpuPolicy("invalid"), true},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("mode=%s,cpupolicy=%s", test.mode, test.cpuPolicy), CanCreateContainer(testEnv, test))
	}
}

func CanCreateContainer(testEnv *TestEnvironment, test ContainerTestCase) func(t *testing.T) {
	ctx := testEnv.Ctx
	return func(t *testing.T) {
		name := test.mode + "-" + string(test.cpuPolicy)
		key := client.ObjectKey{
			Namespace: "default",
			Name:      fmt.Sprintf("test-container-%s", name),
		}

		container := &wekav1alpha1.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: key.Namespace,
				Name:      fmt.Sprintf("test-container-%s", name),
			},
			Spec: wekav1alpha1.WekaContainerSpec{
				Mode:      test.mode,
				CpuPolicy: test.cpuPolicy,
			},
		}
		err := testEnv.Client.Create(ctx, container)
		if err == nil && test.expectedError {
			t.Fatalf("error creating container - expected: %v, got: %v", test.expectedError, err)
		}
		container = &wekav1alpha1.WekaContainer{}
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, container)
			if test.expectedError {
				return err != nil
			} else {
				return err == nil
			}
		})

		t.Run("ValidateConditions", ValidateConditions(container))

		if err := testEnv.Client.Delete(ctx, container); err != nil {
			if !test.expectedError {
				t.Fatalf("failed to delete container: %v", err)
			}
		}
		waitFor(ctx, func(ctx context.Context) bool {
			container := &wekav1alpha1.WekaContainer{}
			err := testEnv.Client.Get(ctx, key, container)
			return apierrors.IsNotFound(err)
		})
	}
}

func ValidateConditions(container *wekav1alpha1.WekaContainer) func(t *testing.T) {
	return func(t *testing.T) {
		conditions := container.Status.Conditions
		if len(conditions) != 0 {
			names := make([]string, 0, len(conditions))
			for _, c := range conditions {
				names = append(names, c.Type)
			}
			t.Errorf("expected 0 condition, got %v", names)
		}
	}
}

func TestNewContainerController(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
		return
	}
	defer teardownTestEnv(testEnv)

	if testEnv.Manager == nil {
		t.Errorf("failed to create manager")
		return
	}

	subject := NewContainerController(testEnv.Manager)
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
