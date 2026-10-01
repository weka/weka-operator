package runtimes

import (
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
	v1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func strp(s string) *string { return &s }

func TestResolveAllocations(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		req   allocation
		alloc *v1alpha1.ContainerAllocations
		want  allocation
	}{
		{
			name:  "nil allocation passes request through unchanged",
			mode:  "compute",
			req:   allocation{Ports: config.Ports{Weka: 1, Agent: 2}, FailureDomain: strp("fd"), MachineID: "m", NetDevice: "eth0"},
			alloc: nil,
			want:  allocation{Ports: config.Ports{Weka: 1, Agent: 2}, FailureDomain: strp("fd"), MachineID: "m", NetDevice: "eth0"},
		},
		{
			// rule 2 + 3: client takes VF net-device + machine-id override, nothing else.
			name: "client accepts eligible VF net-device and machine-id, not ports/failuredomain/drives",
			mode: "client",
			req:  allocation{Ports: config.Ports{Weka: 0, Agent: 0}, FailureDomain: strp("fd"), MachineID: "orig", NetDevice: "vf_0"},
			alloc: &v1alpha1.ContainerAllocations{
				WekaPort: 100, AgentPort: 200, FailureDomain: strp("other"),
				MachineIdentifier: "new-machine", NetDevices: []string{"eth1_0"}, Drives: []string{"/dev/sda"},
			},
			want: allocation{Ports: config.Ports{Weka: 0, Agent: 0}, FailureDomain: strp("fd"), MachineID: "new-machine", NetDevice: "eth1_0"},
		},
		{
			// rule 2: client net-device ineligible for VF -> not overridden.
			name:  "client ignores allocation net-device when not VF-eligible",
			mode:  "client",
			req:   allocation{NetDevice: "not-a-vf-device", MachineID: "orig"},
			alloc: &v1alpha1.ContainerAllocations{NetDevices: []string{"vf0"}},
			want:  allocation{NetDevice: "not-a-vf-device", MachineID: "orig"},
		},
		{
			// rule 4: non-client preserves nonzero requested ports, fills the rest.
			name:  "backend preserves nonzero requested ports and fills unresolved ones",
			mode:  "compute",
			req:   allocation{Ports: config.Ports{Weka: 555, Agent: 0}},
			alloc: &v1alpha1.ContainerAllocations{WekaPort: 100, AgentPort: 200},
			want:  allocation{Ports: config.Ports{Weka: 555, Agent: 200}, Drives: nil},
		},
		{
			// rule 5: envoy never takes its weka port from allocations.
			name:  "envoy does not take weka port from allocations",
			mode:  "envoy",
			req:   allocation{Ports: config.Ports{Weka: 0, Agent: 0}},
			alloc: &v1alpha1.ContainerAllocations{WekaPort: 100, AgentPort: 200},
			want:  allocation{Ports: config.Ports{Weka: 0, Agent: 200}},
		},
		{
			// rule 6: telemetry takes neither port from allocations.
			name:  "telemetry does not take either port from allocations",
			mode:  "telemetry",
			req:   allocation{Ports: config.Ports{Weka: 0, Agent: 0}},
			alloc: &v1alpha1.ContainerAllocations{WekaPort: 100, AgentPort: 200},
			want:  allocation{Ports: config.Ports{Weka: 0, Agent: 0}},
		},
		{
			// rule 7: allocation failure domain omitted (nil) preserves input.
			name:  "omitted allocation failure domain preserves input",
			mode:  "compute",
			req:   allocation{FailureDomain: strp("fd")},
			alloc: &v1alpha1.ContainerAllocations{FailureDomain: nil},
			want:  allocation{FailureDomain: strp("fd")},
		},
		{
			// rule 7: allocation failure domain present but empty clears it.
			name:  "present but empty allocation failure domain clears input",
			mode:  "compute",
			req:   allocation{FailureDomain: strp("fd")},
			alloc: &v1alpha1.ContainerAllocations{FailureDomain: strp("")},
			want:  allocation{FailureDomain: nil},
		},
		{
			// rule 8: empty allocation machine identifier does not overwrite input.
			name:  "empty allocation machine identifier does not overwrite input",
			mode:  "compute",
			req:   allocation{MachineID: "orig"},
			alloc: &v1alpha1.ContainerAllocations{MachineIdentifier: ""},
			want:  allocation{MachineID: "orig"},
		},
		{
			// rule 9: requested drives land in the result, not the request.
			name:  "drives resolved from allocation for backend modes",
			mode:  "drive",
			req:   allocation{},
			alloc: &v1alpha1.ContainerAllocations{Drives: []string{"/dev/sda", "/dev/sdb"}},
			want:  allocation{Drives: []string{"/dev/sda", "/dev/sdb"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAllocations(tt.mode, &tt.req, tt.alloc)
			if got.Ports != tt.want.Ports {
				t.Errorf("Ports = %+v, want %+v", got.Ports, tt.want.Ports)
			}
			if !strEqual(got.FailureDomain, tt.want.FailureDomain) {
				t.Errorf("FailureDomain = %v, want %v", got.FailureDomain, tt.want.FailureDomain)
			}
			if got.MachineID != tt.want.MachineID {
				t.Errorf("MachineID = %q, want %q", got.MachineID, tt.want.MachineID)
			}
			if got.NetDevice != tt.want.NetDevice {
				t.Errorf("NetDevice = %q, want %q", got.NetDevice, tt.want.NetDevice)
			}
			if !slicesEqual(got.Drives, tt.want.Drives) {
				t.Errorf("Drives = %v, want %v", got.Drives, tt.want.Drives)
			}
		})
	}
}

func strEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
