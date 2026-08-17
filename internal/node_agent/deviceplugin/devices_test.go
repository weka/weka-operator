package deviceplugin

import (
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestResourceName(t *testing.T) {
	if got, want := ResourceName(0), "weka.io/numa-region-0"; got != want {
		t.Errorf("ResourceName(0) = %q, want %q", got, want)
	}
	if got, want := ResourceName(3), "weka.io/numa-region-3"; got != want {
		t.Errorf("ResourceName(3) = %q, want %q", got, want)
	}
}

func TestSocketName(t *testing.T) {
	if got, want := SocketName(2), "weka-numa-region-2.sock"; got != want {
		t.Errorf("SocketName(2) = %q, want %q", got, want)
	}
}

func TestGenerateDevices(t *testing.T) {
	devices := GenerateDevices(1)

	if len(devices) != DevicesPerRegion {
		t.Fatalf("GenerateDevices(1) returned %d devices, want %d", len(devices), DevicesPerRegion)
	}

	seen := make(map[string]bool, len(devices))
	for i, d := range devices {
		wantID := DeviceID(1, i)
		if d.ID != wantID {
			t.Errorf("device %d ID = %q, want %q", i, d.ID, wantID)
		}
		if d.Health != pluginapi.Healthy {
			t.Errorf("device %d Health = %q, want %q", i, d.Health, pluginapi.Healthy)
		}
		if seen[d.ID] {
			t.Errorf("duplicate device ID %q", d.ID)
		}
		seen[d.ID] = true
		if d.Topology == nil || len(d.Topology.Nodes) != 1 || d.Topology.Nodes[0].ID != 1 {
			t.Errorf("device %d Topology = %v, want single NUMA node with ID 1", i, d.Topology)
		}
	}
}
