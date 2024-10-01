package controllers

import (
	"os"
	"testing"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcile_TombstoneController(t *testing.T) {
	os.Setenv("WEKA_OPERATOR_MAINTENANCE_SA_NAME", "test")
	ctx, _, done := instrumentation.GetLogSpan(pkgCtx, "TestReconcile")
	defer done()

	manager, err := testingManager()
	if err != nil {
		t.Fatalf("TestingManager() error = %v, want nil", err)
	}

	key := types.NamespacedName{
		Name:      "test",
		Namespace: "default",
	}
	subject := &wekav1alpha1.Tombstone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: wekav1alpha1.TombstoneSpec{
			CrType:          "test",
			CrId:            "test",
			NodeAffinity:    wekav1alpha1.NodeName("node-1"),
			PersistencePath: "test",
			ContainerName:   "test",
		},
	}
	reconciler := NewTombstoneController(manager, TombstoneConfig{})
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

	// Running GC creates the deletion job
	t.Run("Run GC", func(t *testing.T) {
		if err := reconciler.GC(ctx); err != nil {
			t.Fatalf("GC() error = %v, want nil", err)
		}

		if err := manager.GetClient().Get(ctx, key, subject); err != nil {
			t.Fatalf("GetClient().Get() error = %v, want nil", err)
		}
		if subject.DeletionTimestamp == nil {
			t.Errorf("Tombstone DeletionTimestamp = nil, want not nil")
		}

		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Errorf("Reconcile() error = %v, want nil", err)
		}
		if result.IsZero() {
			t.Errorf("Reconcile() result = %v, should requeue", result)
		}

		jobs := &v1.JobList{}
		if err := manager.GetClient().List(ctx, jobs); err != nil {
			t.Errorf("GetClient().List() error = %v, want not nil", err)
		}
		if len(jobs.Items) != 1 {
			t.Errorf("GetClient().List() want 1 job, got %d", len(jobs.Items))
		}
	})

	// Simulate run of the deletion job and re-run reconcile, this should remove the finalizer
	t.Run("Run Deletion Job", func(t *testing.T) {
		jobs := &v1.JobList{}
		if err := manager.GetClient().List(ctx, jobs); err != nil {
			t.Fatalf("GetClient().List() error = %v, want nil", err)
		}
		if len(jobs.Items) != 1 {
			t.Errorf("GetClient().List() want 1 job, got %d", len(jobs.Items))
		}

		job := jobs.Items[0]
		job.Status.Succeeded = 1
		if err := manager.GetClient().Status().Update(ctx, &job); err != nil {
			t.Fatalf("GetClient().Status().Update() error = %v, want nil", err)
		}

		_, err = reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("Reconcile() error = %v, want nil", err)
		}

		if err := manager.GetClient().Get(ctx, key, subject); err != nil {
			t.Fatalf("GetClient().Get() error = %v, want nil", err)
		}
		if controllerutil.ContainsFinalizer(subject, WekaFinalizer) {
			t.Errorf("Tombstone still has finalizer")
		}
	})
}
