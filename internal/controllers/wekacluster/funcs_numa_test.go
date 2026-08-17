package wekacluster

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/pkg/util"
)

// TestHandleSpecUpdates_PropagatesNumaToContainer verifies that cluster-level NUMA
// configuration is resolved per-role and patched onto an existing container whose
// Numa field predates the feature (nil).
//
// The container mode is s3 and the cluster uses clusterCapacity mode (Dynamic.ClusterCapacity
// set) specifically to steer NewUpdatableClusterSpec away from computing compute/drive
// hugepages, which for an S3-only, non-clusterCapacity setup would otherwise require real
// drive-container/node fixtures (weka-full-drives annotation parsing) unrelated to NUMA.
func TestHandleSpecUpdates_PropagatesNumaToContainer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	region1 := 1
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: types.UID("test-uid")},
		Spec: weka.WekaClusterSpec{
			Dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "10TiB"},
			Numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{S3: &region1},
			},
		},
	}

	s3Container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-s3-0", Namespace: "default"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeS3},
	}

	objects := []client.Object{cluster, s3Container}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		Build()

	loop := &wekaClusterReconcilerLoop{
		Manager:    &fakeManager{client: fakeClient},
		cluster:    cluster,
		containers: []*weka.WekaContainer{s3Container},
	}

	if err := loop.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates returned unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(s3Container), got); err != nil {
		t.Fatalf("failed to fetch container: %v", err)
	}

	if got.Spec.Numa == nil {
		t.Fatal("expected container.Spec.Numa to be set, got nil")
	}
	if !got.Spec.Numa.Single || got.Spec.Numa.Method != weka.WekaNumaMethodDevicePlugin {
		t.Errorf("container.Spec.Numa = %+v, want Single=true Method=%q", got.Spec.Numa, weka.WekaNumaMethodDevicePlugin)
	}
	if got.Spec.Numa.Region == nil || *got.Spec.Numa.Region != region1 {
		t.Errorf("container.Spec.Numa.Region = %v, want %d", got.Spec.Numa.Region, region1)
	}
}

// TestHandleSpecUpdates_NumaDoesNotPropagateToEnvoy verifies that a role outside the
// backend/protocol set (envoy) never has Numa patched onto it, mirroring the factory's
// explicit role guard (see TestNewWekaContainerForWekaCluster_Numa).
func TestHandleSpecUpdates_NumaDoesNotPropagateToEnvoy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	region0 := 0
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: types.UID("test-uid")},
		Spec: weka.WekaClusterSpec{
			Dynamic: &weka.WekaClusterTemplate{ClusterCapacity: "10TiB"},
			Numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{All: &region0},
			},
		},
	}

	envoyContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-envoy-0", Namespace: "default"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeEnvoy},
	}

	objects := []client.Object{cluster, envoyContainer}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		Build()

	loop := &wekaClusterReconcilerLoop{
		Manager:    &fakeManager{client: fakeClient},
		cluster:    cluster,
		containers: []*weka.WekaContainer{envoyContainer},
	}

	if err := loop.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates returned unexpected error: %v", err)
	}

	got := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(envoyContainer), got); err != nil {
		t.Fatalf("failed to fetch container: %v", err)
	}

	if got.Spec.Numa != nil {
		t.Errorf("expected envoy container.Spec.Numa to remain nil, got %+v", got.Spec.Numa)
	}
}

// TestUpdatableClusterSpec_HashChangesWithNuma pins the reason Numa was added to
// UpdatableClusterSpec: HandleSpecUpdates skips the per-container patch entirely when
// container.Status.LastAppliedSpec already equals the spec hash, so a cluster-level Numa
// change MUST change that hash or the propagation above would never actually run on a
// reconcile.
func TestUpdatableClusterSpec_HashChangesWithNuma(t *testing.T) {
	region1 := 1

	base := &UpdatableClusterSpec{}
	withNuma := &UpdatableClusterSpec{
		Numa: &weka.WekaClusterNuma{
			Single: true,
			Region: &weka.WekaClusterNumaRegion{All: &region1},
		},
	}

	baseHash, err := util.HashStruct(base)
	if err != nil {
		t.Fatalf("HashStruct(base) returned unexpected error: %v", err)
	}
	withNumaHash, err := util.HashStruct(withNuma)
	if err != nil {
		t.Fatalf("HashStruct(withNuma) returned unexpected error: %v", err)
	}

	if baseHash == withNumaHash {
		t.Error("expected spec hash to change when cluster Numa changes, but it stayed the same")
	}
}
