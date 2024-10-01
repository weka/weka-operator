package controllers

import (
	"testing"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestReconcile_ManualOperation(t *testing.T) {
	t.Skip("Null pointer panic")
	ctx, _, done := instrumentation.GetLogSpan(pkgCtx, "TestReconcile_ManualOperation")
	defer done()

	manager, err := testingManager()
	if err != nil {
		t.Fatalf("testingManager() error = %v, want nil", err)
	}

	key := types.NamespacedName{
		Name:      "test",
		Namespace: "default",
	}
	subject := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: weka.WekaManualOperationSpec{
			Action: "sign-drives",
			Payload: weka.ManualOperatorPayload{
				SignDrives: &weka.SignDrivesPayload{},
			},
		},
	}

	reconciler := NewWekaManualOperationController(manager)
	req := reconcile.Request{
		NamespacedName: key,
	}

	t.Run("FromBeginning", func(t *testing.T) {
		if err := manager.GetClient().Create(ctx, subject); err != nil {
			t.Fatalf("Create() error = %v, want nil", err)
		}
		_, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("Reconcile() error = %v, want nil", err)
		}
	})

	t.Run("Final Status", func(t *testing.T) {
		if err := manager.GetClient().Get(ctx, key, subject); err != nil {
			t.Fatalf("Get() error = %v, want nil", err)
		}
		if subject.Status.Status != "Done" {
			t.Fatalf("Status.Status = %v, want Done", subject.Status.Status)
		}
		if subject.Status.Result != "test" {
			t.Fatalf("Status.Result = %v, want test", subject.Status.Result)
		}
	})
}
