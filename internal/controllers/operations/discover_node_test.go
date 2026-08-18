package operations

import (
	"context"
	"errors"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/go-steps-engine/lifecycle"
)

func newDiscoverNodeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newDiscoverNodeTestOp(scheme *runtime.Scheme, existing ...*weka.WekaContainer) *DiscoverNodeOperation {
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, c := range existing {
		builder = builder.WithObjects(c)
	}
	kclient := builder.Build()

	owner := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-client",
			Namespace: "default",
			UID:       "test-client-uid",
		},
	}
	return &DiscoverNodeOperation{
		client:         kclient,
		scheme:         scheme,
		nodeName:       "test-node",
		image:          "quay.io/weka.io/weka-in-container:4.5.0",
		pullSecret:     "pull-secret",
		serviceAccount: "weka-sa",
		tolerations:    []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
		ownerRef:       owner,
		node: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		},
	}
}

func (o *DiscoverNodeOperation) desiredTestContainer() *weka.WekaContainer {
	controller := true
	return &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.getContainerName(),
			Namespace: o.ownerRef.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "weka.weka.io/v1alpha1",
				Kind:       "WekaClient",
				Name:       o.ownerRef.GetName(),
				UID:        o.ownerRef.GetUID(),
				Controller: &controller,
			}},
		},
		Spec: weka.WekaContainerSpec{
			Mode:               weka.WekaContainerModeDiscovery,
			NodeAffinity:       weka.NodeName(o.node.Name),
			Image:              o.image,
			ImagePullSecret:    o.pullSecret,
			Tolerations:        o.tolerations,
			ServiceAccountName: o.serviceAccount,
		},
	}
}

func TestIsContainerSpecChanged(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)

	cases := []struct {
		name   string
		mutate func(c *weka.WekaContainer)
		want   bool
	}{
		{"identical", func(c *weka.WekaContainer) {}, false},
		{"image drift", func(c *weka.WekaContainer) { c.Spec.Image = "quay.io/weka.io/weka-in-container:4.6.0" }, true},
		{"pull secret drift", func(c *weka.WekaContainer) { c.Spec.ImagePullSecret = "other-secret" }, true},
		{"service account drift", func(c *weka.WekaContainer) { c.Spec.ServiceAccountName = "other-sa" }, true},
		{"tolerations drift", func(c *weka.WekaContainer) { c.Spec.Tolerations = nil }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := newDiscoverNodeTestOp(scheme)
			container := op.desiredTestContainer()
			tc.mutate(container)
			op.container = container
			if got := op.isContainerSpecChanged(); got != tc.want {
				t.Errorf("isContainerSpecChanged() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("nil vs empty tolerations is not drift", func(t *testing.T) {
		op := newDiscoverNodeTestOp(scheme)
		op.tolerations = []corev1.Toleration{}
		container := op.desiredTestContainer()
		container.Spec.Tolerations = nil
		op.container = container
		if op.isContainerSpecChanged() {
			t.Error("nil vs empty tolerations must not count as drift")
		}
	})
}

func TestEnsureContainers_ExistingMatching(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)
	op := newDiscoverNodeTestOp(scheme)
	container := op.desiredTestContainer()
	op = newDiscoverNodeTestOp(scheme, container)
	op.container = container

	if err := op.EnsureContainers(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if op.container == nil {
		t.Fatal("matching container must be kept")
	}
	got := &weka.WekaContainer{}
	if err := op.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: container.Name}, got); err != nil {
		t.Fatalf("matching container must not be deleted: %v", err)
	}
}

func TestEnsureContainers_SpecChangedDeletesAndWaits(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)
	op := newDiscoverNodeTestOp(scheme)
	container := op.desiredTestContainer()
	container.Spec.Image = "quay.io/weka.io/weka-in-container:old"
	op = newDiscoverNodeTestOp(scheme, container)
	op.container = container

	err := op.EnsureContainers(context.Background())
	waitErr := &lifecycle.WaitError{}
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError, got %v", err)
	}
	got := &weka.WekaContainer{}
	getErr := op.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: container.Name}, got)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("drifted container must be deleted, got %v", getErr)
	}
}

func TestEnsureContainers_ForeignOwnerNotTouched(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)
	op := newDiscoverNodeTestOp(scheme)
	container := op.desiredTestContainer()
	container.Spec.Image = "quay.io/weka.io/weka-in-container:old"
	container.OwnerReferences[0].Name = "other-owner"
	container.OwnerReferences[0].UID = "other-owner-uid"
	op = newDiscoverNodeTestOp(scheme, container)
	op.container = container

	if err := op.EnsureContainers(context.Background()); err != nil {
		t.Fatalf("expected nil error for foreign-owned container, got %v", err)
	}
	got := &weka.WekaContainer{}
	if err := op.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: container.Name}, got); err != nil {
		t.Fatalf("foreign-owned container must not be deleted: %v", err)
	}
}

func TestEnsureContainers_TerminatingWaitsWithoutDelete(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)
	op := newDiscoverNodeTestOp(scheme)
	container := op.desiredTestContainer()
	container.Spec.Image = "quay.io/weka.io/weka-in-container:old"
	now := metav1.Now()
	container.DeletionTimestamp = &now
	container.Finalizers = []string{"weka.io/test"}
	op.container = container

	err := op.EnsureContainers(context.Background())
	waitErr := &lifecycle.WaitError{}
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError for terminating container, got %v", err)
	}
	if op.container == nil {
		t.Fatal("terminating container must not be re-deleted (DeleteContainers resets o.container)")
	}
}

func TestEnsureContainers_CreatesWhenMissing(t *testing.T) {
	scheme := newDiscoverNodeTestScheme(t)
	op := newDiscoverNodeTestOp(scheme)

	if err := op.EnsureContainers(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if op.container == nil {
		t.Fatal("container must be created")
	}
	if op.container.Spec.Image != op.image {
		t.Errorf("created with image %q, want %q", op.container.Spec.Image, op.image)
	}
	if op.container.Spec.ServiceAccountName != op.serviceAccount {
		t.Errorf("created with serviceAccount %q, want %q", op.container.Spec.ServiceAccountName, op.serviceAccount)
	}
}
