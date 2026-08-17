package factory

import (
	"reflect"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

func numaTestCluster(numa *weka.WekaClusterNuma) *weka.WekaCluster {
	return &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
			UID:  types.UID("test-uid"),
		},
		Spec: weka.WekaClusterSpec{
			Numa: numa,
		},
	}
}

// TestNewWekaContainerForWekaCluster_Numa verifies that cluster-level NUMA
// configuration is resolved per-role onto the container spec for backend/protocol
// roles, and that roles outside that set (envoy) never get a Numa value even when
// Region.All would otherwise resolve one for them.
func TestNewWekaContainerForWekaCluster_Numa(t *testing.T) {
	region1 := 1
	region0 := 0

	cases := []struct {
		name     string
		numa     *weka.WekaClusterNuma
		role     string
		wantNuma *weka.WekaNuma
	}{
		{
			name: "compute role gets explicit region",
			numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{
					All:     &region0,
					Compute: &region1,
				},
			},
			role: "compute",
			wantNuma: &weka.WekaNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &region1,
			},
		},
		{
			name: "drive role falls back to All",
			numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{
					All:     &region0,
					Compute: &region1,
				},
			},
			role: "drive",
			wantNuma: &weka.WekaNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &region0,
			},
		},
		{
			name:     "cluster numa nil -> container numa nil",
			numa:     nil,
			role:     "compute",
			wantNuma: nil,
		},
		{
			name: "envoy role never gets numa, even though Region.All would resolve one",
			numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{
					All: &region0,
				},
			},
			role:     "envoy",
			wantNuma: nil,
		},
		{
			name: "telemetry role never gets numa, even though Region.All would resolve one",
			numa: &weka.WekaClusterNuma{
				Single: true,
				Method: weka.WekaNumaMethodDevicePlugin,
				Region: &weka.WekaClusterNumaRegion{
					All: &region0,
				},
			},
			role:     "telemetry",
			wantNuma: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cluster := numaTestCluster(tc.numa)

			container, err := NewWekaContainerForWekaCluster(cluster, allocator.ClusterTemplate{}, allocator.ContainerHugepages{}, tc.role, "0")
			if err != nil {
				t.Fatalf("NewWekaContainerForWekaCluster returned unexpected error: %v", err)
			}

			if !reflect.DeepEqual(container.Spec.Numa, tc.wantNuma) {
				t.Errorf("container.Spec.Numa = %+v, want %+v", container.Spec.Numa, tc.wantNuma)
			}
		})
	}
}
