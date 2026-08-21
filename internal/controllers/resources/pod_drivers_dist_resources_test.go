package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// makeDriversDistFactory builds a PodFactory for a drivers-dist container with the given
// spec fields layered on top (Mode is forced to drivers-dist).
func makeDriversDistFactory(spec weka.WekaContainerSpec) *PodFactory {
	spec.Mode = weka.WekaContainerModeDriversDist
	if spec.CpuPolicy == "" {
		spec.CpuPolicy = weka.CpuPolicyAuto
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drivers-dist"},
		Spec:       spec,
	}
	return NewPodFactory(container, &discovery.DiscoveryNodeInfo{}, &domain.FeatureFlags{})
}

// TestSetResources_DriversDistAdditionalMemory verifies AdditionalMemory adds on top of the
// drivers-dist 3000M baseline (decimal M + binary MiB, per the file's existing convention)
// without shifting the baseline when AdditionalMemory is unset.
func TestSetResources_DriversDistAdditionalMemory(t *testing.T) {
	baseline := resource.MustParse("3000M")

	cases := []struct {
		name             string
		additionalMemory int
		wantMemoryBytes  int64
	}{
		{"zero additional memory keeps exactly 3000M baseline", 0, baseline.Value()},
		{"additional memory adds on top of baseline", 2048, baseline.Value() + 2048*1024*1024},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := makeDriversDistFactory(weka.WekaContainerSpec{
				AdditionalMemory: tc.additionalMemory,
			})
			hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
			pod := makePod(weka.CpuPolicyAuto)

			if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
				t.Fatalf("setResources returned unexpected error: %v", err)
			}

			gotRequest := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
			if gotRequest.Value() != tc.wantMemoryBytes {
				t.Errorf("memory request = %s (%d bytes), want %d bytes", gotRequest.String(), gotRequest.Value(), tc.wantMemoryBytes)
			}
			gotLimit := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
			if gotLimit.Value() != tc.wantMemoryBytes {
				t.Errorf("memory limit = %s (%d bytes), want %d bytes (limit follows request when unset)", gotLimit.String(), gotLimit.Value(), tc.wantMemoryBytes)
			}
		})
	}
}

// TestSetResources_DriversDistResourcesOverride verifies spec.resources replaces the
// drivers-dist baseline cpu/memory sizing (500m/2000m cpu, 3000M memory) rather than adding
// to it.
func TestSetResources_DriversDistResourcesOverride(t *testing.T) {
	override := &weka.PodResourcesSpec{
		Requests: weka.PodResources{
			Cpu:    resource.MustParse("250m"),
			Memory: resource.MustParse("1234Mi"),
		},
		Limits: weka.PodResources{
			Cpu:    resource.MustParse("999m"),
			Memory: resource.MustParse("2222Mi"),
		},
	}

	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Resources: override,
	})
	hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
	pod := makePod(weka.CpuPolicyAuto)

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertQuantity(t, "cpu request", res.Requests[corev1.ResourceCPU], resource.MustParse("250m"))
	assertQuantity(t, "cpu limit", res.Limits[corev1.ResourceCPU], resource.MustParse("999m"))
	assertQuantity(t, "memory request", res.Requests[corev1.ResourceMemory], resource.MustParse("1234Mi"))
	assertQuantity(t, "memory limit", res.Limits[corev1.ResourceMemory], resource.MustParse("2222Mi"))
}

// TestSetResources_DriversDistHugepagesOverride verifies spec.resources.requests.hugepages is
// what lets drivers-dist (which has no hugepages sizing of its own) request hugepages: it
// lands on both request and limit as hugepages-2Mi, and drives the pod's hugepages volume
// medium.
func TestSetResources_DriversDistHugepagesOverride(t *testing.T) {
	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Image: "test-image",
		Resources: &weka.PodResourcesSpec{
			Requests: weka.PodResources{Hugepages2Mi: resource.MustParse("2Gi")},
		},
	})

	pod, err := factory.Create(context.Background(), nil)
	if err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	wantQty := resource.MustParse("2048Mi")
	assertQuantity(t, "hugepages request", res.Requests["hugepages-2Mi"], wantQty)
	assertQuantity(t, "hugepages limit", res.Limits["hugepages-2Mi"], wantQty)

	var gotMedium corev1.StorageMedium
	found := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == "hugepages" {
			found = true
			if v.EmptyDir != nil {
				gotMedium = v.EmptyDir.Medium
			}
		}
	}
	if !found {
		t.Fatalf("pod has no hugepages volume")
	}
	if gotMedium != "HugePages-2Mi" {
		t.Errorf("hugepages volume medium = %q, want %q", gotMedium, "HugePages-2Mi")
	}
}

// TestSetResources_DriversDistResourcesOverrideHugepagesOnly verifies a spec.resources that
// sets only hugepages leaves the computed cpu/memory baseline intact.
func TestSetResources_DriversDistResourcesOverrideHugepagesOnly(t *testing.T) {
	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Resources: &weka.PodResourcesSpec{
			Requests: weka.PodResources{Hugepages2Mi: resource.MustParse("2Gi")},
		},
	})
	hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
	pod := makePod(weka.CpuPolicyAuto)

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertQuantity(t, "cpu request", res.Requests[corev1.ResourceCPU], resource.MustParse("500m"))
	assertQuantity(t, "cpu limit", res.Limits[corev1.ResourceCPU], resource.MustParse("2000m"))
	assertQuantity(t, "memory request", res.Requests[corev1.ResourceMemory], resource.MustParse("3000M"))
	assertQuantity(t, "memory limit", res.Limits[corev1.ResourceMemory], resource.MustParse("3000M"))
}

// TestSetResources_DriversDistEmptyResourcesFallsBackToDefaults verifies that an empty (but
// non-nil) spec.resources does not wipe or zero anything: every entry left unset falls back to
// the computed value, exactly as if spec.resources were nil.
func TestSetResources_DriversDistEmptyResourcesFallsBackToDefaults(t *testing.T) {
	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Resources: &weka.PodResourcesSpec{},
	})
	hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
	pod := makePod(weka.CpuPolicyAuto)

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertQuantity(t, "cpu request", res.Requests[corev1.ResourceCPU], resource.MustParse("500m"))
	assertQuantity(t, "cpu limit", res.Limits[corev1.ResourceCPU], resource.MustParse("2000m"))
	assertQuantity(t, "memory request", res.Requests[corev1.ResourceMemory], resource.MustParse("3000M"))
	assertQuantity(t, "memory limit", res.Limits[corev1.ResourceMemory], resource.MustParse("3000M"))
	assertQuantity(t, "hugepages request", res.Requests["hugepages-2Mi"], resource.MustParse("0"))
	assertQuantity(t, "hugepages limit", res.Limits["hugepages-2Mi"], resource.MustParse("0"))
}

// TestSetResources_DriversDistResourcesPartialCpuOnly verifies that setting only
// requests.cpu overrides just that entry: the cpu limit, memory, and hugepages stay at their
// computed values.
func TestSetResources_DriversDistResourcesPartialCpuOnly(t *testing.T) {
	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Resources: &weka.PodResourcesSpec{
			Requests: weka.PodResources{Cpu: resource.MustParse("250m")},
		},
	})
	hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
	pod := makePod(weka.CpuPolicyAuto)

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	// Naming one side of the cpu pair moves both: leaving the limit at the computed 2000m would
	// be harmless here, but the same rule with a request ABOVE the computed limit inverts the
	// pair and gets the pod rejected. Memory and hugepages, untouched by the override, stay computed.
	assertQuantity(t, "cpu request", res.Requests[corev1.ResourceCPU], resource.MustParse("250m"))
	assertQuantity(t, "cpu limit", res.Limits[corev1.ResourceCPU], resource.MustParse("250m"))
	assertQuantity(t, "memory request", res.Requests[corev1.ResourceMemory], resource.MustParse("3000M"))
	assertQuantity(t, "memory limit", res.Limits[corev1.ResourceMemory], resource.MustParse("3000M"))
	assertQuantity(t, "hugepages request", res.Requests["hugepages-2Mi"], resource.MustParse("0"))
	assertQuantity(t, "hugepages limit", res.Limits["hugepages-2Mi"], resource.MustParse("0"))
	assertRequestsWithinLimits(t, res)
}

func assertQuantity(t *testing.T, label string, got, want resource.Quantity) {
	t.Helper()
	if got.Cmp(want) != 0 {
		t.Errorf("%s = %s, want %s", label, got.String(), want.String())
	}
}

// assertRequestsWithinLimits checks the invariant kubelet enforces: every request must be <= its
// limit. A pod violating it is rejected outright, so an override must never be able to produce one.
func assertRequestsWithinLimits(t *testing.T, res corev1.ResourceRequirements) {
	t.Helper()
	for name, request := range res.Requests {
		limit, ok := res.Limits[name]
		if !ok {
			continue
		}
		if request.Cmp(limit) > 0 {
			t.Errorf("%s request %s exceeds limit %s", name, request.String(), limit.String())
		}
	}
}

// TestSetResources_PartialOverridePairsRequestAndLimit covers naming only one side of a
// request/limit pair. The unnamed side must follow the named one rather than stay at the
// computed value, which would otherwise invert the pair against the 3000M drivers baseline.
func TestSetResources_PartialOverridePairsRequestAndLimit(t *testing.T) {
	cases := []struct {
		name      string
		resources *weka.PodResourcesSpec
		wantMem   string
		wantCPU   string
	}{
		{
			name:      "requests.memory above the baseline limit",
			resources: &weka.PodResourcesSpec{Requests: weka.PodResources{Memory: resource.MustParse("8Gi")}},
			wantMem:   "8Gi",
		},
		{
			name:      "limits.memory only",
			resources: &weka.PodResourcesSpec{Limits: weka.PodResources{Memory: resource.MustParse("8Gi")}},
			wantMem:   "8Gi",
		},
		{
			name:      "requests.cpu above the baseline limit",
			resources: &weka.PodResourcesSpec{Requests: weka.PodResources{Cpu: resource.MustParse("4")}},
			wantCPU:   "4",
		},
		{
			name:      "limits.cpu only",
			resources: &weka.PodResourcesSpec{Limits: weka.PodResources{Cpu: resource.MustParse("4")}},
			wantCPU:   "4",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := makeDriversDistFactory(weka.WekaContainerSpec{Resources: tc.resources})
			hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
			pod := makePod(weka.CpuPolicyAuto)

			if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
				t.Fatalf("setResources returned unexpected error: %v", err)
			}

			res := pod.Spec.Containers[0].Resources
			assertRequestsWithinLimits(t, res)

			if tc.wantMem != "" {
				want := resource.MustParse(tc.wantMem)
				gotReq, gotLim := res.Requests[corev1.ResourceMemory], res.Limits[corev1.ResourceMemory]
				if gotReq.Cmp(want) != 0 || gotLim.Cmp(want) != 0 {
					t.Errorf("memory = %s/%s, want %s on both sides", gotReq.String(), gotLim.String(), tc.wantMem)
				}
			}
			if tc.wantCPU != "" {
				want := resource.MustParse(tc.wantCPU)
				gotReq, gotLim := res.Requests[corev1.ResourceCPU], res.Limits[corev1.ResourceCPU]
				if gotReq.Cmp(want) != 0 || gotLim.Cmp(want) != 0 {
					t.Errorf("cpu = %s/%s, want %s on both sides", gotReq.String(), gotLim.String(), tc.wantCPU)
				}
			}
		})
	}
}

// TestSetResources_ClientMemoryLimitFollowsRequest covers a client whose spec.resources names
// requests.memory but no limit: the zero limit Quantity renders as "0", which must not become
// the pod's memory limit.
func TestSetResources_ClientMemoryLimitFollowsRequest(t *testing.T) {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "client"},
		Spec: weka.WekaContainerSpec{
			Mode:      weka.WekaContainerModeClient,
			NumCores:  2,
			CpuPolicy: weka.CpuPolicyDedicated,
			Resources: &weka.PodResourcesSpec{
				Requests: weka.PodResources{Memory: resource.MustParse("4Gi")},
			},
		},
	}
	factory := NewPodFactory(container, &discovery.DiscoveryNodeInfo{}, &domain.FeatureFlags{})
	pod := makePod(weka.CpuPolicyDedicated)

	if err := factory.setResources(context.Background(), pod, GetHugePagesDetails(container, factory.featureFlags)); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertRequestsWithinLimits(t, res)

	want := resource.MustParse("4Gi")
	gotLim := res.Limits[corev1.ResourceMemory]
	if gotLim.Cmp(want) != 0 {
		t.Errorf("memory limit = %s, want %s (a zero limit Quantity must not reach the pod)", gotLim.String(), want.String())
	}
}

// TestSetResources_HugepagesOverrideOnOneGiContainer covers a 1Gi-paged container plus a
// hugepages-2Mi override. The override names one specific resource, so the pod must end up
// asking for BOTH page sizes rather than the override's amount landing on the 1Gi name.
func TestSetResources_HugepagesOverrideOnOneGiContainer(t *testing.T) {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "compute"},
		Spec: weka.WekaContainerSpec{
			Mode:          weka.WekaContainerModeCompute,
			NumCores:      2,
			CpuPolicy:     weka.CpuPolicyDedicated,
			Hugepages:     4096,
			HugepagesSize: "1Gi",
			Resources: &weka.PodResourcesSpec{
				Requests: weka.PodResources{Hugepages2Mi: resource.MustParse("2Gi")},
			},
		},
	}
	factory := NewPodFactory(container, &discovery.DiscoveryNodeInfo{}, &domain.FeatureFlags{})
	hgDetails := GetHugePagesDetails(container, factory.featureFlags)

	// The 1Gi sizing must be untouched by the 2Mi override.
	if hgDetails.HugePagesResourceName != corev1.ResourceName("hugepages-1Gi") {
		t.Errorf("hugepages resource name = %s, want hugepages-1Gi (2Mi override must not rename it)", hgDetails.HugePagesResourceName)
	}
	if hgDetails.HugePagesMb != 4096 {
		t.Errorf("HugePagesMb = %d, want 4096 (2Mi override must not replace the 1Gi amount)", hgDetails.HugePagesMb)
	}

	pod := makePod(weka.CpuPolicyDedicated)
	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertRequestsWithinLimits(t, res)
	assertQuantity(t, "hugepages-2Mi request", res.Requests["hugepages-2Mi"], resource.MustParse("2Gi"))
	assertQuantity(t, "hugepages-2Mi limit", res.Limits["hugepages-2Mi"], resource.MustParse("2Gi"))
	if _, ok := res.Requests["hugepages-1Gi"]; !ok {
		t.Errorf("pod lost its hugepages-1Gi request; both page sizes should be present")
	}
}

// TestSetResources_HugepagesLimitOnlyOverride covers naming only limits.hugepages-2Mi. kubelet
// requires a hugepages request and limit to be equal, so the limit must stand for both instead
// of being silently dropped.
func TestSetResources_HugepagesLimitOnlyOverride(t *testing.T) {
	factory := makeDriversDistFactory(weka.WekaContainerSpec{
		Resources: &weka.PodResourcesSpec{
			Limits: weka.PodResources{Hugepages2Mi: resource.MustParse("2Gi")},
		},
	})
	hgDetails := GetHugePagesDetails(factory.container, factory.featureFlags)
	pod := makePod(weka.CpuPolicyAuto)

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources returned unexpected error: %v", err)
	}

	res := pod.Spec.Containers[0].Resources
	assertRequestsWithinLimits(t, res)
	assertQuantity(t, "hugepages-2Mi request", res.Requests["hugepages-2Mi"], resource.MustParse("2Gi"))
	assertQuantity(t, "hugepages-2Mi limit", res.Limits["hugepages-2Mi"], resource.MustParse("2Gi"))
}
