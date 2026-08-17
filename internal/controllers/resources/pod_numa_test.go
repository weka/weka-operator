package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// TestSetResources_NumaRegionResource verifies that setResources requests the
// weka.io/numa-region-<N> extended resource only when the container is confined to a
// single NUMA region via the device-plugin method, and leaves it absent otherwise. The
// "no numa set" case is an explicit regression guard: containers created before this
// feature (or without numa configured) must not gain any weka.io/numa-region-* resource.
func TestSetResources_NumaRegionResource(t *testing.T) {
	region1 := 1

	cases := []struct {
		name         string
		numa         *weka.WekaNuma
		wantResource string // "" means no weka.io/numa-region-* resource should be present
	}{
		{
			name:         "no numa set -> no numa-region resource",
			numa:         nil,
			wantResource: "",
		},
		{
			name: "single true, region set, method empty (defaults to device-plugin) -> resource requested",
			numa: &weka.WekaNuma{
				Single: true,
				Region: &region1,
			},
			wantResource: "weka.io/numa-region-1",
		},
		{
			name: "single true, region set, method explicitly device-plugin -> resource requested",
			numa: &weka.WekaNuma{
				Single: true,
				Region: &region1,
				Method: weka.WekaNumaMethodDevicePlugin,
			},
			wantResource: "weka.io/numa-region-1",
		},
		{
			name: "single false -> no numa-region resource",
			numa: &weka.WekaNuma{
				Single: false,
				Region: &region1,
			},
			wantResource: "",
		},
		{
			name: "single true, region nil -> no numa-region resource",
			numa: &weka.WekaNuma{
				Single: true,
			},
			wantResource: "",
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

			// Assert no weka.io/numa-region-* resource leaked in when none is expected,
			// regardless of region index, so this doubles as the "unrelated resource shape
			// unaffected" regression check.
			var gotNumaResource string
			for name := range pod.Spec.Containers[0].Resources.Requests {
				if len(name) > len(consts.WekaNumaRegionResourcePrefix) &&
					string(name[:len(consts.WekaNumaRegionResourcePrefix)]) == consts.WekaNumaRegionResourcePrefix {
					gotNumaResource = string(name)
				}
			}

			if gotNumaResource != tc.wantResource {
				t.Errorf("numa-region resource in Requests = %q, want %q", gotNumaResource, tc.wantResource)
			}

			if tc.wantResource == "" {
				return
			}

			reqQty, ok := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceName(tc.wantResource)]
			if !ok {
				t.Fatalf("expected Requests[%s] to be set", tc.wantResource)
			}
			if reqQty.Value() != 1 {
				t.Errorf("Requests[%s] = %d, want 1", tc.wantResource, reqQty.Value())
			}

			limitQty, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceName(tc.wantResource)]
			if !ok {
				t.Fatalf("expected Limits[%s] to be set", tc.wantResource)
			}
			if limitQty.Value() != 1 {
				t.Errorf("Limits[%s] = %d, want 1", tc.wantResource, limitQty.Value())
			}
		})
	}
}
