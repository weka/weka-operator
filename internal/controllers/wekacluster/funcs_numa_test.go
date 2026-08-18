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
// configuration is resolved per-role and patched onto existing containers whose Numa field
// predates the feature (nil): a role-specific override (RoleNuma.S3) wins over the global Numa
// for the s3 container, while the drive container (no override set) falls back to the global Numa.
//
// The cluster uses clusterCapacity mode (Dynamic.ClusterCapacity set) specifically to steer
// NewUpdatableClusterSpec away from computing compute/drive hugepages, which for a non-
// clusterCapacity setup would otherwise require real drive-container/node fixtures
// (weka-full-drives annotation parsing) unrelated to NUMA.
func TestHandleSpecUpdates_PropagatesNumaToContainer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	region0 := 0
	region1 := 1
	globalNuma := &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDevicePlugin, Region: &region0}
	s3Override := &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDevicePlugin, Region: &region1}

	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: types.UID("test-uid")},
		Spec: weka.WekaClusterSpec{
			Dynamic:  &weka.WekaClusterTemplate{ClusterCapacity: "10TiB"},
			Numa:     globalNuma,
			RoleNuma: weka.RoleNumaSelector{S3: s3Override},
		},
	}

	s3Container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-s3-0", Namespace: "default"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeS3},
	}
	driveContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-drive-0", Namespace: "default"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive},
	}

	objects := []client.Object{cluster, s3Container, driveContainer}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		Build()

	loop := &wekaClusterReconcilerLoop{
		Manager:    &fakeManager{client: fakeClient},
		cluster:    cluster,
		containers: []*weka.WekaContainer{s3Container, driveContainer},
	}

	if err := loop.HandleSpecUpdates(context.Background()); err != nil {
		t.Fatalf("HandleSpecUpdates returned unexpected error: %v", err)
	}

	gotS3 := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(s3Container), gotS3); err != nil {
		t.Fatalf("failed to fetch s3 container: %v", err)
	}
	if gotS3.Spec.Numa == nil || gotS3.Spec.Numa.Region == nil || *gotS3.Spec.Numa.Region != region1 {
		t.Errorf("s3 container.Spec.Numa = %+v, want the role override (region %d)", gotS3.Spec.Numa, region1)
	}

	gotDrive := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(driveContainer), gotDrive); err != nil {
		t.Fatalf("failed to fetch drive container: %v", err)
	}
	if gotDrive.Spec.Numa == nil || gotDrive.Spec.Numa.Region == nil || *gotDrive.Spec.Numa.Region != region0 {
		t.Errorf("drive container.Spec.Numa = %+v, want the global fallback (region %d)", gotDrive.Spec.Numa, region0)
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
			Numa:    &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDevicePlugin, Region: &region0},
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

// TestUpdatableClusterSpec_HashChangesWithNuma pins the reason Numa and RoleNuma were added to
// UpdatableClusterSpec: HandleSpecUpdates skips the per-container patch entirely when
// container.Status.LastAppliedSpec already equals the spec hash, so a cluster-level Numa or
// RoleNuma change MUST change that hash or the propagation above would never actually run on a
// reconcile.
func TestUpdatableClusterSpec_HashChangesWithNuma(t *testing.T) {
	region1 := 1

	base := &UpdatableClusterSpec{}
	withNuma := &UpdatableClusterSpec{
		Numa: &weka.WekaNuma{Single: true, Region: &region1},
	}
	withRoleNuma := &UpdatableClusterSpec{
		RoleNuma: weka.RoleNumaSelector{Compute: &weka.WekaNuma{Single: true, Region: &region1}},
	}

	baseHash, err := util.HashStruct(base)
	if err != nil {
		t.Fatalf("HashStruct(base) returned unexpected error: %v", err)
	}
	withNumaHash, err := util.HashStruct(withNuma)
	if err != nil {
		t.Fatalf("HashStruct(withNuma) returned unexpected error: %v", err)
	}
	withRoleNumaHash, err := util.HashStruct(withRoleNuma)
	if err != nil {
		t.Fatalf("HashStruct(withRoleNuma) returned unexpected error: %v", err)
	}

	if baseHash == withNumaHash {
		t.Error("expected spec hash to change when cluster Numa changes, but it stayed the same")
	}
	if baseHash == withRoleNumaHash {
		t.Error("expected spec hash to change when cluster RoleNuma changes, but it stayed the same")
	}
	if withNumaHash == withRoleNumaHash {
		t.Error("expected Numa-only and RoleNuma-only changes to produce different hashes")
	}
}
