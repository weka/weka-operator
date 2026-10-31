package services

import (
	"encoding/json"
	"testing"
)

// processJSON6x is trimmed from `wekaauthcli cluster process --json` on a 6.0.1 cluster (hostnames
// and addresses replaced).
const processJSON6x = `[
    {"container": 0, "container_name": "drivex0", "hostname": "node-0", "ips": ["fd00:db8::1"], "mode": "backend", "process": 0, "roles": ["MANAGEMENT"], "slot": 0, "status": "UP", "sw_version": "6.0.1"},
    {"container": 0, "container_name": "drivex0", "hostname": "node-0", "ips": ["fd00:db8::1"], "mode": "backend", "process": 1, "roles": ["DRIVES"], "slot": 1, "status": "UP", "sw_version": "6.0.1"},
    {"container": 1, "container_name": "computex1", "hostname": "node-1", "ips": ["fd00:db8::2"], "mode": "backend", "process": 21, "roles": ["COMPUTE"], "slot": 1, "status": "UP", "sw_version": "6.0.1"}
]`

// legacyProcessJSON is trimmed from the same command on a 5.1.31 cluster: ids as "NodeId<N>"/"HostId<N>"
// strings, container fields at the top level and again under "nodeInfo".
const legacyProcessJSON = `[
    {"node_id": "NodeId<0>", "host_id": "HostId<0>", "container_name": "drivex0", "slot": 0, "status": "UP", "roles": ["MANAGEMENT"], "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000, "hostname": "node-0", "mode": "backend", "nodeInfo": {"host_id": "HostId<0>", "container_name": "drivex0", "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000, "hostname": "node-0"}},
    {"node_id": "NodeId<1>", "host_id": "HostId<0>", "container_name": "drivex0", "slot": 1, "status": "UP", "roles": ["DRIVES"], "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000, "hostname": "node-0", "mode": "backend", "nodeInfo": {"host_id": "HostId<0>", "container_name": "drivex0", "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000, "hostname": "node-0"}},
    {"node_id": "NodeId<20>", "host_id": "HostId<1>", "container_name": "drivex1", "slot": 0, "status": "UP", "roles": ["MANAGEMENT"], "mgmt_ips": ["10.0.0.2"], "mgmt_port": 15000, "hostname": "node-1", "mode": "backend", "nodeInfo": {"host_id": "HostId<1>", "container_name": "drivex1", "mgmt_ips": ["10.0.0.2"], "mgmt_port": 15000, "hostname": "node-1"}}
]`

func TestProcessAcrossCliGenerations(t *testing.T) {
	for _, tc := range []struct {
		name          string
		payload       string
		processId     int
		containerId   int
		containerName string
		slot          int
		managementIp  string
	}{
		// 6.x reports no management endpoint per process.
		{"6.x", processJSON6x, 21, 1, "computex1", 1, ""},
		{"legacy", legacyProcessJSON, 20, 1, "drivex1", 0, "10.0.0.2"},
	} {
		var processes []Process
		if err := json.Unmarshal([]byte(tc.payload), &processes); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		last := processes[len(processes)-1]
		if id, err := last.ProcessId(); err != nil || id != tc.processId {
			t.Errorf("%s: ProcessId() = %d, %v; want %d", tc.name, id, err, tc.processId)
		}
		if id, err := last.ContainerId(); err != nil || id != tc.containerId {
			t.Errorf("%s: ContainerId() = %d, %v; want %d", tc.name, id, err, tc.containerId)
		}
		if got := last.GetContainerName(); got != tc.containerName {
			t.Errorf("%s: GetContainerName() = %q; want %q", tc.name, got, tc.containerName)
		}
		if last.Slot != tc.slot {
			t.Errorf("%s: Slot = %d; want %d", tc.name, last.Slot, tc.slot)
		}
		if ip, _ := last.ManagementEndpoint(); ip != tc.managementIp {
			t.Errorf("%s: ManagementEndpoint() ip = %q; want %q", tc.name, ip, tc.managementIp)
		}
		// The second process of container 0 is in slot 1; "nodeInfo" carries no slot.
		if processes[1].Slot != 1 {
			t.Errorf("%s: second process Slot = %d; want 1", tc.name, processes[1].Slot)
		}
	}

	var processes []Process
	if err := json.Unmarshal([]byte(processJSON6x), &processes); err != nil {
		t.Fatal(err)
	}
	if id, err := processes[0].ProcessId(); err != nil || id != 0 {
		t.Errorf("ProcessId() of process 0 = %d, %v", id, err)
	}

	var unknown Process
	if _, err := unknown.ProcessId(); err == nil {
		t.Error("ProcessId() on an unrecognized schema must error")
	}
	if _, err := unknown.ContainerId(); err == nil {
		t.Error("ContainerId() on an unrecognized schema must error")
	}
}

// clusterContainerJSON6x is trimmed from `wekaauthcli cluster container --json` on a 6.0.1 cluster.
const clusterContainerJSON6x = `[
    {"id": 0, "hostname": "node-0", "ips": ["fd00:db8::1"], "mode": "backend", "name": "drivex0", "port": 15000, "status": "UP"},
    {"id": 19, "hostname": "node-7", "ips": ["fd00:db8::8"], "mode": "client", "name": "clientx19", "port": 46000, "status": "UP"}
]`

func TestClusterContainerEndpoint6x(t *testing.T) {
	var containers []WekaClusterContainer
	if err := json.Unmarshal([]byte(clusterContainerJSON6x), &containers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c := containers[0]; c.Id == nil || *c.Id != 0 || c.Port != 15000 || c.Ips[0] != "fd00:db8::1" {
		t.Errorf("backend container = %+v", c)
	}
	if c := containers[1]; c.Id == nil || *c.Id != 19 || c.Port != 46000 {
		t.Errorf("client container = %+v", c)
	}
}
