package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// TestSetResources_NumaDraMethod verifies that setResources wires pod.spec.resourceClaims and the
// weka container's resources.claims when Numa.Method is "dra", instead of requesting the
// weka.io/numa-region-<N> extended resource used by the device-plugin method. The claim object
// itself is created elsewhere (wekacontainer reconciler); this only checks the pod-side wiring.
func TestSetResources_NumaDraMethod(t *testing.T) {
	region3 := 3

	cases := []struct {
		name          string
		numa          *weka.WekaNuma
		wantClaimWire bool
	}{
		{
			name: "single true, region set, method dra -> pod wired to claim, no numa-region resource",
			numa: &weka.WekaNuma{
				Single: true,
				Region: &region3,
				Method: weka.WekaNumaMethodDra,
			},
			wantClaimWire: true,
		},
		{
			name: "single false, method dra -> ignored",
			numa: &weka.WekaNuma{
				Single: false,
				Region: &region3,
				Method: weka.WekaNumaMethodDra,
			},
			wantClaimWire: false,
		},
		{
			name: "single true, region nil, method dra -> ignored",
			numa: &weka.WekaNuma{
				Single: true,
				Method: weka.WekaNumaMethodDra,
			},
			wantClaimWire: false,
		},
	}

	hgDetails := minimalHgDetails()

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			nodeInfo := &discovery.DiscoveryNodeInfo{}
			factory := makeFactory(2, weka.CpuPolicyDedicatedHT, nodeInfo)
			factory.container.Spec.Numa = tc.numa
			pod := makePod(weka.CpuPolicyDedicatedHT)

			if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
				t.Fatalf("setResources returned unexpected error: %v", err)
			}

			// No weka.io/numa-region-* resource should ever be requested for the dra method,
			// regardless of whether the claim wiring applies.
			for name := range pod.Spec.Containers[0].Resources.Requests {
				if len(name) > len(consts.WekaNumaRegionResourcePrefix) &&
					string(name[:len(consts.WekaNumaRegionResourcePrefix)]) == consts.WekaNumaRegionResourcePrefix {
					t.Errorf("unexpected numa-region extended resource requested for dra method: %s", name)
				}
			}

			if !tc.wantClaimWire {
				if len(pod.Spec.ResourceClaims) != 0 {
					t.Errorf("expected no pod.Spec.ResourceClaims, got: %+v", pod.Spec.ResourceClaims)
				}
				if len(pod.Spec.Containers[0].Resources.Claims) != 0 {
					t.Errorf("expected no container resource claims, got: %+v", pod.Spec.Containers[0].Resources.Claims)
				}
				return
			}

			wantClaimName := NumaClaimNameForContainer(factory.container.Name)

			if len(pod.Spec.ResourceClaims) != 1 {
				t.Fatalf("expected exactly one pod.Spec.ResourceClaims entry, got: %+v", pod.Spec.ResourceClaims)
			}
			podClaim := pod.Spec.ResourceClaims[0]
			if podClaim.Name != consts.WekaNumaClaimName {
				t.Errorf("pod.Spec.ResourceClaims[0].Name = %q, want %q", podClaim.Name, consts.WekaNumaClaimName)
			}
			if podClaim.ResourceClaimName == nil || *podClaim.ResourceClaimName != wantClaimName {
				t.Errorf("pod.Spec.ResourceClaims[0].ResourceClaimName = %v, want %q", podClaim.ResourceClaimName, wantClaimName)
			}

			if len(pod.Spec.Containers[0].Resources.Claims) != 1 {
				t.Fatalf("expected exactly one container resource claim, got: %+v", pod.Spec.Containers[0].Resources.Claims)
			}
			containerClaim := pod.Spec.Containers[0].Resources.Claims[0]
			if containerClaim.Name != consts.WekaNumaClaimName {
				t.Errorf("container resources.claims[0].Name = %q, want %q", containerClaim.Name, consts.WekaNumaClaimName)
			}

			// No leftover device-plugin extended resource for the same region.
			numaResourceName := corev1.ResourceName(consts.WekaNumaRegionResourcePrefix + "3")
			if _, ok := pod.Spec.Containers[0].Resources.Requests[numaResourceName]; ok {
				t.Errorf("did not expect %s in requests for dra method", numaResourceName)
			}
		})
	}
}

// TestSetResources_NumaUnknownMethod verifies that setResources rejects a Numa.Method it doesn't
// recognize instead of silently doing nothing (e.g. a typo, or a future method rolled out to the
// CRD before the operator knows how to wire it).
func TestSetResources_NumaUnknownMethod(t *testing.T) {
	region1 := 1
	nodeInfo := &discovery.DiscoveryNodeInfo{}
	factory := makeFactory(2, weka.CpuPolicyDedicatedHT, nodeInfo)
	factory.container.Spec.Numa = &weka.WekaNuma{
		Single: true,
		Region: &region1,
		Method: weka.WekaNumaMethod("bogus-method"),
	}
	pod := makePod(weka.CpuPolicyDedicatedHT)

	err := factory.setResources(context.Background(), pod, minimalHgDetails())
	if err == nil {
		t.Fatal("expected setResources to reject an unknown numa method, got nil")
	}
}

// TestSetResources_NumaDraMethod_Idempotent verifies that calling setResources twice against the
// same pod (setResources can run more than once while a caller re-derives the pod spec) does not
// duplicate the resourceClaims/resources.claims entries — they must be assigned, not appended to.
func TestSetResources_NumaDraMethod_Idempotent(t *testing.T) {
	region3 := 3
	nodeInfo := &discovery.DiscoveryNodeInfo{}
	factory := makeFactory(2, weka.CpuPolicyDedicatedHT, nodeInfo)
	factory.container.Spec.Numa = &weka.WekaNuma{
		Single: true,
		Region: &region3,
		Method: weka.WekaNumaMethodDra,
	}
	pod := makePod(weka.CpuPolicyDedicatedHT)
	hgDetails := minimalHgDetails()

	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources (first call) returned unexpected error: %v", err)
	}
	if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
		t.Fatalf("setResources (second call) returned unexpected error: %v", err)
	}

	if len(pod.Spec.ResourceClaims) != 1 {
		t.Errorf("expected exactly one pod.Spec.ResourceClaims entry after two calls, got %d: %+v", len(pod.Spec.ResourceClaims), pod.Spec.ResourceClaims)
	}
	if len(pod.Spec.Containers[0].Resources.Claims) != 1 {
		t.Errorf("expected exactly one container resource claim after two calls, got %d: %+v", len(pod.Spec.Containers[0].Resources.Claims), pod.Spec.Containers[0].Resources.Claims)
	}
}
