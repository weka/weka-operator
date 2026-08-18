package wekacontainer

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
)

func numaDraTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}
	if err := resourcev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add resource.k8s.io/v1 scheme: %v", err)
	}
	return scheme
}

// TestNeedsNumaDraClaim verifies the predicate that gates the DRA ensure step: only fires for
// Single+Region set and Method explicitly "dra" — device-plugin and empty-method containers, and
// containers without a full Single+Region pair, must never take the DRA path.
func TestNeedsNumaDraClaim(t *testing.T) {
	region1 := 1

	cases := []struct {
		name string
		numa *weka.WekaNuma
		want bool
	}{
		{"no numa set", nil, false},
		{"dra method, single+region set", &weka.WekaNuma{Single: true, Region: &region1, Method: weka.WekaNumaMethodDra}, true},
		{"dra method, single false", &weka.WekaNuma{Single: false, Region: &region1, Method: weka.WekaNumaMethodDra}, false},
		{"dra method, region nil", &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDra}, false},
		{"device-plugin method, single+region set", &weka.WekaNuma{Single: true, Region: &region1, Method: weka.WekaNumaMethodDevicePlugin}, false},
		{"empty method, single+region set", &weka.WekaNuma{Single: true, Region: &region1}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := &containerReconcilerLoop{
				container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{Numa: tc.numa}},
			}
			if got := r.needsNumaDraClaim(); got != tc.want {
				t.Errorf("needsNumaDraClaim() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckNumaDeviceClassInstalled verifies the dra.cpu DeviceClass is only ever checked for
// (never created — it's owned by dra-driver-cpu, not the operator) and that a missing class
// produces a clear "not installed" error rather than a silent no-op or a generic API error.
func TestCheckNumaDeviceClassInstalled(t *testing.T) {
	scheme := numaDraTestScheme(t)

	t.Run("missing DeviceClass returns a clear not-installed error", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme}

		err := r.checkNumaDeviceClassInstalled(context.Background())
		if err == nil {
			t.Fatal("expected an error when the dra.cpu DeviceClass is missing, got nil")
		}
		if !strings.Contains(err.Error(), "dra-driver-cpu") || !strings.Contains(err.Error(), consts.WekaDraDeviceClassName) {
			t.Errorf("expected error to mention dra-driver-cpu and %s, got: %v", consts.WekaDraDeviceClassName, err)
		}
	})

	t.Run("no-op when DeviceClass already exists", func(t *testing.T) {
		existing := &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: consts.WekaDraDeviceClassName},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme}

		if err := r.checkNumaDeviceClassInstalled(context.Background()); err != nil {
			t.Fatalf("checkNumaDeviceClassInstalled returned unexpected error: %v", err)
		}
	})
}

// TestEnsureNumaResourceClaimForCPUCount exercises the create/compare/recreate CRUD logic directly
// against an explicit CPU count, without needing to fake node discovery (numaClaimCPUCount, which
// resolves that count from the container's real target node, is intentionally left untested here —
// see its doc comment; it's a thin wrapper around capacityplanner.CPURequestCores, which owns the
// actual arithmetic and is tested in its own package).
func TestEnsureNumaResourceClaimForCPUCount(t *testing.T) {
	scheme := numaDraTestScheme(t)
	region2 := 2

	newContainer := func() *weka.WekaContainer {
		return &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-container", Namespace: "default", UID: "test-uid"},
			Spec: weka.WekaContainerSpec{
				Numa: &weka.WekaNuma{Single: true, Region: &region2, Method: weka.WekaNumaMethodDra},
			},
		}
	}

	t.Run("creates ResourceClaim with expected shape when missing", func(t *testing.T) {
		container := newContainer()
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme, container: container}

		if err := r.ensureNumaResourceClaimForCPUCount(context.Background(), 4); err != nil {
			t.Fatalf("ensureNumaResourceClaimForCPUCount returned unexpected error: %v", err)
		}

		wantName := resources.NumaClaimNameForContainer(container.Name)
		got := &resourcev1.ResourceClaim{}
		if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: wantName, Namespace: container.Namespace}, got); err != nil {
			t.Fatalf("expected ResourceClaim %s to be created: %v", wantName, err)
		}

		if len(got.Spec.Devices.Requests) != 1 {
			t.Fatalf("expected exactly one device request, got: %+v", got.Spec.Devices.Requests)
		}
		req := got.Spec.Devices.Requests[0]
		if req.Name != "cpus" {
			t.Errorf("request name = %q, want %q", req.Name, "cpus")
		}
		if req.Exactly == nil {
			t.Fatalf("expected Exactly to be set")
		}
		if req.Exactly.DeviceClassName != consts.WekaDraDeviceClassName {
			t.Errorf("DeviceClassName = %q, want %q", req.Exactly.DeviceClassName, consts.WekaDraDeviceClassName)
		}
		if req.Exactly.AllocationMode != resourcev1.DeviceAllocationModeExactCount {
			t.Errorf("AllocationMode = %q, want %q", req.Exactly.AllocationMode, resourcev1.DeviceAllocationModeExactCount)
		}
		if req.Exactly.Count != 1 {
			t.Errorf("Count = %d, want 1", req.Exactly.Count)
		}
		if len(req.Exactly.Selectors) != 1 || req.Exactly.Selectors[0].CEL == nil {
			t.Fatalf("expected exactly one CEL selector, got: %+v", req.Exactly.Selectors)
		}
		wantExpr := `device.attributes["dra.cpu"].numaNodeID == 2`
		if req.Exactly.Selectors[0].CEL.Expression != wantExpr {
			t.Errorf("CEL expression = %q, want %q", req.Exactly.Selectors[0].CEL.Expression, wantExpr)
		}

		// The CPU count is now carried directly on the typed Capacity field (GA resource.k8s.io/v1
		// has ExactDeviceRequest.Capacity — no annotation stand-in needed anymore).
		if req.Exactly.Capacity == nil {
			t.Fatalf("expected Exactly.Capacity to be set")
		}
		gotQty, ok := req.Exactly.Capacity.Requests[resourcev1.QualifiedName(consts.WekaDraCPUCapacity)]
		if !ok {
			t.Fatalf("expected capacity.requests[%s] to be set, got: %+v", consts.WekaDraCPUCapacity, req.Exactly.Capacity.Requests)
		}
		if !gotQty.Equal(resource.MustParse("4")) {
			t.Errorf("capacity.requests[%s] = %s, want 4", consts.WekaDraCPUCapacity, gotQty.String())
		}

		if len(got.OwnerReferences) != 1 {
			t.Fatalf("expected exactly one owner reference, got: %+v", got.OwnerReferences)
		}
		owner := got.OwnerReferences[0]
		if owner.Name != container.Name || owner.Kind != "WekaContainer" {
			t.Errorf("owner reference = %+v, want name=%s kind=WekaContainer", owner, container.Name)
		}
		if owner.Controller == nil || !*owner.Controller {
			t.Errorf("expected owner reference to be a controller reference")
		}
	})

	t.Run("no-op when a matching ResourceClaim already exists", func(t *testing.T) {
		container := newContainer()
		claim := buildNumaResourceClaim(container, region2, 4)
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container, claim).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme, container: container}

		if err := r.ensureNumaResourceClaimForCPUCount(context.Background(), 4); err != nil {
			t.Fatalf("ensureNumaResourceClaimForCPUCount returned unexpected error: %v", err)
		}

		got := &resourcev1.ResourceClaim{}
		if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: claim.Name, Namespace: claim.Namespace}, got); err != nil {
			t.Fatalf("expected the existing claim to still be present: %v", err)
		}
		if got.UID != claim.UID {
			t.Errorf("expected the matching claim to be left in place (same object), got a different UID")
		}
	})

	t.Run("stale cpu count triggers delete and recreate", func(t *testing.T) {
		container := newContainer()
		staleClaim := buildNumaResourceClaim(container, region2, 2) // old count
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container, staleClaim).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme, container: container}

		if err := r.ensureNumaResourceClaimForCPUCount(context.Background(), 4); err != nil {
			t.Fatalf("ensureNumaResourceClaimForCPUCount returned unexpected error: %v", err)
		}

		got := &resourcev1.ResourceClaim{}
		if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: staleClaim.Name, Namespace: staleClaim.Namespace}, got); err != nil {
			t.Fatalf("expected a recreated claim to be present: %v", err)
		}
		gotQty := got.Spec.Devices.Requests[0].Exactly.Capacity.Requests[resourcev1.QualifiedName(consts.WekaDraCPUCapacity)]
		if !gotQty.Equal(resource.MustParse("4")) {
			t.Errorf("capacity.requests[%s] after recreate = %s, want 4", consts.WekaDraCPUCapacity, gotQty.String())
		}
	})

	t.Run("old numa-region.weka.io shape triggers delete and recreate", func(t *testing.T) {
		container := newContainer()
		oldShapeClaim := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resources.NumaClaimNameForContainer(container.Name),
				Namespace: container.Namespace,
			},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name: "region",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: "numa-region.weka.io",
								AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
								Count:           1,
								Selectors: []resourcev1.DeviceSelector{
									{CEL: &resourcev1.CELDeviceSelector{Expression: `device.attributes["numa.weka.io"].region == 2`}},
								},
							},
						},
					},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container, oldShapeClaim).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme, container: container}

		if err := r.ensureNumaResourceClaimForCPUCount(context.Background(), 4); err != nil {
			t.Fatalf("ensureNumaResourceClaimForCPUCount returned unexpected error: %v", err)
		}

		got := &resourcev1.ResourceClaim{}
		if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: oldShapeClaim.Name, Namespace: oldShapeClaim.Namespace}, got); err != nil {
			t.Fatalf("expected a recreated claim to be present: %v", err)
		}
		if got.Spec.Devices.Requests[0].Name != "cpus" || got.Spec.Devices.Requests[0].Exactly.DeviceClassName != consts.WekaDraDeviceClassName {
			t.Errorf("expected the old-shape claim to be replaced with the new dra.cpu shape, got: %+v", got.Spec.Devices.Requests[0])
		}
	})

	t.Run("non-positive cpu count is rejected", func(t *testing.T) {
		container := newContainer()
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).Build()
		r := &containerReconcilerLoop{Client: fakeClient, Scheme: scheme, container: container}

		if err := r.ensureNumaResourceClaimForCPUCount(context.Background(), 0); err == nil {
			t.Fatal("expected an error for a computed CPU count of 0, got nil")
		}
	})
}

// TestClaimNeedsRecreate is a focused unit test on the pure drift-detection logic, independent of
// any k8s client.
func TestClaimNeedsRecreate(t *testing.T) {
	container := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}}
	matching := buildNumaResourceClaim(container, 2, 4)

	if claimNeedsRecreate(matching, 2, 4) {
		t.Error("expected an exact-match claim to not need recreation")
	}
	if !claimNeedsRecreate(matching, 3, 4) {
		t.Error("expected a region mismatch to need recreation")
	}
	if !claimNeedsRecreate(matching, 2, 8) {
		t.Error("expected a cpu-count mismatch to need recreation")
	}

	noCapacity := buildNumaResourceClaim(container, 2, 4)
	noCapacity.Spec.Devices.Requests[0].Exactly.Capacity = nil
	if !claimNeedsRecreate(noCapacity, 2, 4) {
		t.Error("expected a claim with no capacity requirements to need recreation")
	}

	emptySpec := &resourcev1.ResourceClaim{}
	if !claimNeedsRecreate(emptySpec, 2, 4) {
		t.Error("expected a claim with no device requests to need recreation")
	}
}
