package services

import (
	"encoding/json"
	"testing"
)

// wekactlInterfaceGroupJSON was captured verbatim from `weka nfs interface-group --name
// MgmtInterfaceGroup --json` on a live 6.0.0.6363 cluster (feature flag wekactl_as_default=true).
// Container 13 is mid-deletion, hence UNREACHABLE.
const wekactlInterfaceGroupJSON = `[
    {
        "allow_manage_gids": true,
        "gateway": "255.255.255.255",
        "ips": [],
        "name": "MgmtInterfaceGroup",
        "netmask": 32,
        "ports": [
            {"container_uid": "b4b129f6-88fa-7050-d2bd-6d7f7d5c7e9f", "container": 12, "port": "enp99s0f0np0", "status": "OK"},
            {"container_uid": "8026f2bb-10f3-9e82-2882-97d0f26db15a", "container": 15, "port": "enp99s0f0np0", "status": "OK"},
            {"container_uid": "fee78013-db99-4a9b-844e-5d3c5c2f67bc", "container": 13, "port": "enp99s0f0np0", "status": "UNREACHABLE"},
            {"container_uid": "eef2d3e6-195a-81fb-db0d-2a96872dcbfe", "container": 14, "port": "enp99s0f0np0", "status": "OK"}
        ],
        "status": "OK",
        "tenant_ids": [],
        "type": "NFS",
        "uid": "dbab1da8-db5c-bc93-0f63-1460c1f08450"
    }
]`

// legacyInterfaceGroupJSON is the pre-wekactl python CLI shape, which identifies the owner with a
// "HostId<N>" string instead of a numeric container.
const legacyInterfaceGroupJSON = `[
    {
        "name": "MgmtInterfaceGroup",
        "ports": [
            {"host_uid": "b4b129f6-88fa-7050-d2bd-6d7f7d5c7e9f", "host_id": "HostId<12>", "port": "enp99s0f0np0", "status": "OK"},
            {"host_uid": "fee78013-db99-4a9b-844e-5d3c5c2f67bc", "host_id": "HostId<13>", "port": "enp99s0f0np0", "status": "UNREACHABLE"}
        ],
        "status": "OK",
        "type": "NFS"
    }
]`

func parseGroup(t *testing.T, payload string) NfsInterfaceGroup {
	t.Helper()
	var groups []NfsInterfaceGroup
	if err := json.Unmarshal([]byte(payload), &groups); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 interface group, got %d", len(groups))
	}
	return groups[0]
}

// TestContainerPortsAcrossCliGenerations pins the port-identity field of both CLI generations.
// A mismatch here does not fail loudly in production: it makes ContainerPorts return nothing, so
// RemoveFromNfs removes nothing yet still reports success, and container deletion then wedges
// forever on "HostId<N> is still part of an interface group" from the deactivate step.
func TestContainerPortsAcrossCliGenerations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"wekactl", wekactlInterfaceGroupJSON},
		{"legacy python cli", legacyInterfaceGroupJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := parseGroup(t, tc.payload)

			ports, err := group.ContainerPorts(13)
			if err != nil {
				t.Fatalf("ContainerPorts(13): %v", err)
			}
			if len(ports) != 1 || ports[0] != "enp99s0f0np0" {
				t.Fatalf("container 13 must own enp99s0f0np0, got %v", ports)
			}

			// The port must be attributed to exactly one container, or removing it from 13 would
			// also be attempted for its neighbours.
			other, err := group.ContainerPorts(12)
			if err != nil {
				t.Fatalf("ContainerPorts(12): %v", err)
			}
			if len(other) != 1 {
				t.Fatalf("container 12 must own exactly one port, got %v", other)
			}

			absent, err := group.ContainerPorts(99)
			if err != nil {
				t.Fatalf("ContainerPorts(99): %v", err)
			}
			if len(absent) != 0 {
				t.Fatalf("container 99 owns no ports, got %v", absent)
			}
		})
	}
}

// TestContainerPortsRejectsUnknownSchema locks in loud failure on a third schema. Returning an empty
// slice instead would reproduce the original wedge: nothing to remove, success reported, deletion
// stuck behind a deactivate that keeps failing.
func TestContainerPortsRejectsUnknownSchema(t *testing.T) {
	group := parseGroup(t, `[{"name": "MgmtInterfaceGroup", "ports": [{"owner": 13, "port": "enp99s0f0np0"}]}]`)

	if _, err := group.ContainerPorts(13); err == nil {
		t.Fatal("a port with no recognizable owner field must fail, not read as unowned")
	}
}

// TestContainerZeroIsDistinguishableFromAbsent guards the pointer on NfsInterfaceGroupPort.Container.
// With a plain int, container 0 and "field missing" are the same value, so an unparseable payload
// would silently claim to own container 0's ports.
func TestContainerZeroIsDistinguishableFromAbsent(t *testing.T) {
	group := parseGroup(t, `[{"name": "g", "ports": [{"container": 0, "port": "eth0"}]}]`)
	ports, err := group.ContainerPorts(0)
	if err != nil {
		t.Fatalf("ContainerPorts(0): %v", err)
	}
	if len(ports) != 1 {
		t.Fatalf("container 0 must own eth0, got %v", ports)
	}

	missing := parseGroup(t, `[{"name": "g", "ports": [{"port": "eth0"}]}]`)
	if _, err := missing.ContainerPorts(0); err == nil {
		t.Fatal("an absent container field must not be read as container 0")
	}
}
