package factory

import (
	"reflect"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

func numaTestCluster(numa *weka.WekaNuma, roleNuma weka.RoleNumaSelector) *weka.WekaCluster {
	return &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
			UID:  types.UID("test-uid"),
		},
		Spec: weka.WekaClusterSpec{
			Numa:     numa,
			RoleNuma: roleNuma,
		},
	}
}

// TestNewWekaContainerForWekaCluster_Numa verifies that cluster-level NUMA
// configuration is resolved per-role onto the container spec for backend/protocol
// roles (role override wins, else falls back to the global Numa), and that roles
// outside that set (envoy, telemetry) never get a Numa value even when the global
// Numa would otherwise resolve one for them.
func TestNewWekaContainerForWekaCluster_Numa(t *testing.T) {
	region0 := 0
	region1 := 1

	globalNuma := &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDevicePlugin, Region: &region0}
	computeOverride := &weka.WekaNuma{Single: true, Method: weka.WekaNumaMethodDevicePlugin, Region: &region1}

	cases := []struct {
		name     string
		numa     *weka.WekaNuma
		roleNuma weka.RoleNumaSelector
		role     string
		wantNuma *weka.WekaNuma
	}{
		{
			name:     "compute role gets its role override",
			numa:     globalNuma,
			roleNuma: weka.RoleNumaSelector{Compute: computeOverride},
			role:     "compute",
			wantNuma: computeOverride,
		},
		{
			name:     "drive role falls back to the global Numa (no override set)",
			numa:     globalNuma,
			roleNuma: weka.RoleNumaSelector{Compute: computeOverride},
			role:     "drive",
			wantNuma: globalNuma,
		},
		{
			name:     "cluster numa nil, no override -> container numa nil",
			numa:     nil,
			roleNuma: weka.RoleNumaSelector{},
			role:     "compute",
			wantNuma: nil,
		},
		{
			name:     "envoy role never gets numa, even though the global Numa is set",
			numa:     globalNuma,
			roleNuma: weka.RoleNumaSelector{},
			role:     "envoy",
			wantNuma: nil,
		},
		{
			name:     "telemetry role never gets numa, even though the global Numa is set",
			numa:     globalNuma,
			roleNuma: weka.RoleNumaSelector{},
			role:     "telemetry",
			wantNuma: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cluster := numaTestCluster(tc.numa, tc.roleNuma)

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
