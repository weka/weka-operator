package wekaclient

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestUpdateContainerIfChanged_ExtraCores asserts that changing extraCores on a WekaClient, in
// either direction, propagates to the generated WekaContainer via updateContainerIfChanged.
// Unlike coresNum (which can only be increased once a container exists), extraCores must also
// propagate on decrease.
func TestUpdateContainerIfChanged_ExtraCores(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client", Namespace: "default"},
		Spec: weka.WekaClientSpec{
			CoresNumber: 2,
			ExtraCores:  1,
		},
	}

	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client-container", Namespace: "default"},
		Spec: weka.WekaContainerSpec{
			// updateContainerIfChanged dereferences SecretKeyRef unconditionally, so it must be non-nil
			// even before the first settle.
			WekaSecretRef: v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(container).
		Build()

	c := &clientReconcilerLoop{
		Client:     fakeClient,
		Recorder:   record.NewFakeRecorder(10),
		wekaClient: wekaClient,
	}

	ctx := context.Background()

	// Settle the container to match the initial wekaClient spec (extraCores=1).
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("initial settle failed: %v", err)
	}
	if container.Spec.ExtraCores != 1 {
		t.Fatalf("expected ExtraCores=1 after initial settle, got %d", container.Spec.ExtraCores)
	}

	// Increase extraCores: must propagate.
	wekaClient.Spec.ExtraCores = 3
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("increase failed: %v", err)
	}
	if container.Spec.ExtraCores != 3 {
		t.Fatalf("expected ExtraCores=3 after increase, got %d", container.Spec.ExtraCores)
	}

	// Decrease extraCores: must also propagate (unlike coresNum, decreasing is allowed).
	wekaClient.Spec.ExtraCores = 0
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("decrease failed: %v", err)
	}
	if container.Spec.ExtraCores != 0 {
		t.Fatalf("expected ExtraCores=0 after decrease, got %d", container.Spec.ExtraCores)
	}
}
