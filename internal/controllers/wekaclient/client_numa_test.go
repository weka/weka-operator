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

// TestUpdateContainerIfChanged_Numa asserts that a WekaClient's Numa field propagates to its
// generated WekaContainer via updateContainerIfChanged, both when it is first set and when it
// is cleared back to nil, mirroring TestUpdateContainerIfChanged_ExtraCores.
func TestUpdateContainerIfChanged_Numa(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	region1 := 1
	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client", Namespace: "default"},
		Spec: weka.WekaClientSpec{
			CoresNumber: 2,
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

	// No numa set: container must stay nil after settle.
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("initial settle failed: %v", err)
	}
	if container.Spec.Numa != nil {
		t.Fatalf("expected Numa=nil after initial settle, got %+v", container.Spec.Numa)
	}

	// Set numa: must propagate.
	wekaClient.Spec.Numa = &weka.WekaNuma{
		Single: true,
		Region: &region1,
		Method: weka.WekaNumaMethodDevicePlugin,
	}
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("set numa failed: %v", err)
	}
	if container.Spec.Numa == nil || !container.Spec.Numa.Single || container.Spec.Numa.Region == nil ||
		*container.Spec.Numa.Region != region1 || container.Spec.Numa.Method != weka.WekaNumaMethodDevicePlugin {
		t.Fatalf("expected Numa to propagate, got %+v", container.Spec.Numa)
	}

	// Clear numa: must also propagate back to nil.
	wekaClient.Spec.Numa = nil
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("clear numa failed: %v", err)
	}
	if container.Spec.Numa != nil {
		t.Fatalf("expected Numa=nil after clearing, got %+v", container.Spec.Numa)
	}
}
