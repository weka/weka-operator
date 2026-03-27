package resources

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// TestGetHugePagesDetails verifies --memory calculation for SSD proxy containers
// under each feature-flag state, and for non-SSDProxy and legacy containers.
//
// Config constants (from config.Consts, initialised before tests):
//   SsdProxyDpdkMemoryMiB      = 2048
//
// SSDProxy offset default = 200 (used when SsdProxyHugepagesOffsetMiB not configured).
//
// New SSDProxy container spec (built by buildProxyContainerSpec):
//   Hugepages       = hugepagesMiB + 2048 + 200
//   HugepagesOffset = 200
//
// Expected --memory:
//   ff=nil or flag=false  →  Hugepages - (200+2048)  = hugepagesMiB
//   flag=true             →  Hugepages - 200         = hugepagesMiB + 2048
func TestGetHugePagesDetails(t *testing.T) {
	const (
		hugepagesMiB = 4000
		offsetMiB    = 200  // SsdProxyHugepagesOffsetMiB default
	)
	dpdk := config.Consts.SsdProxyDpdkMemoryMiB // 2048

	// newHugepages mirrors what buildProxyContainerSpec sets.
	newHugepages := hugepagesMiB + dpdk + offsetMiB // 6248

	newSSDProxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "ssdproxy"},
		Spec: weka.WekaContainerSpec{
			Mode:            weka.WekaContainerModeSSDProxy,
			Hugepages:       newHugepages,
			HugepagesOffset: offsetMiB,
			HugepagesSize:   "2Mi",
		},
	}

	cases := []struct {
		name       string
		container  *weka.WekaContainer
		ff         *domain.FeatureFlags
		wantMemory string
	}{
		{
			// ff=nil → backward-compatible: DPDK excluded from --memory.
			name:       "new SSDProxy, ff=nil",
			container:  newSSDProxy,
			ff:         nil,
			wantMemory: "4000MiB",
		},
		{
			// flag explicitly false: same as nil.
			name:       "new SSDProxy, SsdProxyIncludesDpdkMemory=false",
			container:  newSSDProxy,
			ff:         &domain.FeatureFlags{SsdProxyIncludesDpdkMemory: false},
			wantMemory: "4000MiB",
		},
		{
			// flag=true: ssdproxy accounts for DPDK through weka;
			// all 2048 MiB move into --memory, only 200 MiB buffer stays out.
			name:       "new SSDProxy, SsdProxyIncludesDpdkMemory=true",
			container:  newSSDProxy,
			ff:         &domain.FeatureFlags{SsdProxyIncludesDpdkMemory: true},
			wantMemory: "6048MiB", // 4000 + 2048
		},
		{
			// Legacy container (HugepagesOffset=0, no dpdk in Hugepages spec).
			// GetHugePagesOffset falls back to 200; dpdk is still subtracted.
			// memory = (hugepagesMiB+200) - (200+2048) = hugepagesMiB - 2048
			name: "legacy SSDProxy (HugepagesOffset=0), ff=nil",
			container: &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "ssdproxy-legacy"},
				Spec: weka.WekaContainerSpec{
					Mode:            weka.WekaContainerModeSSDProxy,
					Hugepages:       hugepagesMiB + offsetMiB, // 4200 (no dpdk)
					HugepagesOffset: 0,
					HugepagesSize:   "2Mi",
				},
			},
			ff:         nil,
			wantMemory: "1952MiB", // 4200 - (200+2048)
		},
		{
			// Non-SSDProxy: DPDK flag has no effect; only base offset subtracted.
			name: "compute container, flag=true has no effect",
			container: &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "compute"},
				Spec: weka.WekaContainerSpec{
					Mode:          weka.WekaContainerModeCompute,
					Hugepages:     4000,
					HugepagesSize: "2Mi",
				},
			},
			ff:         &domain.FeatureFlags{SsdProxyIncludesDpdkMemory: true},
			wantMemory: "3800MiB", // 4000 - 200 (default offset)
		},
		{
			// 1Gi hugepages: GiB formatting, no offset arithmetic.
			name: "1Gi hugepages",
			container: &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "compute-1gi"},
				Spec: weka.WekaContainerSpec{
					Mode:          weka.WekaContainerModeCompute,
					Hugepages:     4000,
					HugepagesSize: "1Gi",
				},
			},
			ff:         nil,
			wantMemory: "4GiB",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetHugePagesDetails(tc.container, tc.ff)
			if got.WekaMemoryString != tc.wantMemory {
				t.Errorf("WekaMemoryString = %q, want %q", got.WekaMemoryString, tc.wantMemory)
			}
		})
	}
}

// TestGetHugePagesOffset verifies the offset fallback for each container mode.
func TestGetHugePagesOffset(t *testing.T) {
	cases := []struct {
		name      string
		container *weka.WekaContainer
		want      int
	}{
		{
			name: "explicit HugepagesOffset overrides all defaults",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:            weka.WekaContainerModeSSDProxy,
				HugepagesOffset: 512,
			}},
			want: 512,
		},
		{
			// SsdProxyHugepagesOffsetMiB is not set in test env → explicit 200 fallback.
			name: "SSDProxy with no HugepagesOffset uses 200 default",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode: weka.WekaContainerModeSSDProxy,
			}},
			want: 200,
		},
		{
			name: "Drive mode without drive-sharing: 200 × NumDrives",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:      weka.WekaContainerModeDrive,
				NumDrives: 5,
			}},
			want: 1000,
		},
		{
			name: "Drive mode with drive-sharing: 200 × NumCores",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode:          weka.WekaContainerModeDrive,
				NumCores:      8,
				DriveCapacity: 100, // non-zero → UsesDriveSharing()
			}},
			want: 1600,
		},
		{
			name: "Compute mode falls back to 200",
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{
				Mode: weka.WekaContainerModeCompute,
			}},
			want: 200,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetHugePagesOffset(tc.container)
			if got != tc.want {
				t.Errorf("GetHugePagesOffset = %d, want %d", got, tc.want)
			}
		})
	}
}
