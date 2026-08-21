package wekaclient

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// TestUpdateContainerIfChanged_Resources asserts that changing WekaClient.Spec.Resources
// propagates to an existing WekaContainer's Spec.Resources, and that re-settling with an
// unchanged spec is a no-op (guards against resource.Quantity being invisible to HashStruct's
// gob encoding, which would otherwise mask a real change or fire spuriously every reconcile).
func TestUpdateContainerIfChanged_Resources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client", Namespace: "default"},
		Spec: weka.WekaClientSpec{
			CoresNumber: 2,
			Resources: &weka.PodResourcesSpec{
				Requests: weka.PodResources{Memory: resource.MustParse("4Gi")},
			},
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

	// Settle the container to match the initial wekaClient spec (memory request 4Gi).
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("initial settle failed: %v", err)
	}
	if container.Spec.Resources == nil || container.Spec.Resources.Requests.Memory.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Fatalf("expected memory request 4Gi after initial settle, got %+v", container.Spec.Resources)
	}

	// Change the memory request: must propagate, even though HashStruct alone can't see it.
	wekaClient.Spec.Resources = &weka.PodResourcesSpec{
		Requests: weka.PodResources{Memory: resource.MustParse("64Gi")},
	}
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if container.Spec.Resources.Requests.Memory.Cmp(resource.MustParse("64Gi")) != 0 {
		t.Fatalf("expected memory request 64Gi after update, got %v", container.Spec.Resources.Requests.Memory.String())
	}

	// Re-settle with the identical spec: must be a no-op (no error, value unchanged).
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("no-op re-settle failed: %v", err)
	}
	if container.Spec.Resources.Requests.Memory.Cmp(resource.MustParse("64Gi")) != 0 {
		t.Fatalf("expected memory request to remain 64Gi after no-op re-settle, got %v", container.Spec.Resources.Requests.Memory.String())
	}
}

// TestUpdateContainerIfChanged_ResourcesNilVsEmptyNoChurn asserts that switching the client's
// spec.resources between nil and an explicit empty struct never touches the container: both
// normalize to the same digest, so a client that starts specifying "resources: {}" doesn't
// perpetually re-patch a container that settled with nil.
func TestUpdateContainerIfChanged_ResourcesNilVsEmptyNoChurn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	wekaClient := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client", Namespace: "default"},
		Spec:       weka.WekaClientSpec{CoresNumber: 2},
	}

	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-client-container", Namespace: "default"},
		Spec: weka.WekaContainerSpec{
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

	// Settle with nil resources: container stays nil.
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("initial settle (nil resources) failed: %v", err)
	}
	if container.Spec.Resources != nil {
		t.Fatalf("expected Resources to stay nil, got %+v", container.Spec.Resources)
	}

	// Client now specifies an explicit empty struct: must not churn the container.
	wekaClient.Spec.Resources = &weka.PodResourcesSpec{}
	if err := c.updateContainerIfChanged(ctx, container, NewUpdatableClientSpec(wekaClient)); err != nil {
		t.Fatalf("settle with empty-struct resources failed: %v", err)
	}
	if container.Spec.Resources != nil {
		t.Fatalf("expected Resources to remain nil when client resources is an empty struct, got %+v", container.Spec.Resources)
	}
}

// TestPodResourcesDigest_NilAndEmptyAreEqual asserts nil and an all-zero PodResourcesSpec
// produce the identical digest, so a container created before spec.resources existed doesn't
// diff forever against a client whose spec.resources is an explicit empty struct. Also asserts
// the digest is stable (deterministic across repeated calls) and non-empty once any quantity
// is set.
func TestPodResourcesDigest_NilAndEmptyAreEqual(t *testing.T) {
	nilDigest := podResourcesDigest(nil)
	if nilDigest != "" {
		t.Fatalf("expected nil digest to be empty, got %q", nilDigest)
	}
	emptyDigest := podResourcesDigest(&weka.PodResourcesSpec{})
	if nilDigest != emptyDigest {
		t.Fatalf("nil digest %q != empty-struct digest %q", nilDigest, emptyDigest)
	}

	nonZero := &weka.PodResourcesSpec{
		Requests: weka.PodResources{Memory: resource.MustParse("4Gi")},
	}
	nonZeroDigest := podResourcesDigest(nonZero)
	if nonZeroDigest == "" || nonZeroDigest == nilDigest {
		t.Fatalf("expected a non-zero resources spec to produce a distinct, non-empty digest, got %q", nonZeroDigest)
	}
	if again := podResourcesDigest(nonZero); again != nonZeroDigest {
		t.Fatalf("expected podResourcesDigest to be stable across calls: %q != %q", again, nonZeroDigest)
	}
}
