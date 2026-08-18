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
