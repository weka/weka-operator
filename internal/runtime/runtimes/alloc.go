package runtimes

import (
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/network"
	v1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// allocation is the pre-allocation state passed into resolveAllocations, and the state after
// merging operator allocations into it, per mode-specific rules.
type allocation struct {
	Ports         config.Ports
	FailureDomain *string
	MachineID     string
	NetDevice     string
	Drives        []string
}

// resolveAllocations merges the operator-written resources.json allocation into the request.
// Rules:
//  1. Client allocates and persists its ports before waiting (caller concern, not here).
//  2. Client accepts eligible VF network-device and nonempty machine-identifier overrides.
//  3. Client does not apply allocation ports, backend failure domain, or requested drives.
//  4. Non-client modes preserve nonzero requested ports and fill unresolved ports from allocations.
//  5. Envoy does not take its Weka port from allocations.
//  6. Telemetry takes neither port from allocations.
//  7. Omitted failure domain preserves input; present empty clears it.
//  8. Empty machine identifier does not overwrite input.
//  9. Requested drives belong to resolved state, not configuration.
//  10. SSD proxy and driver distribution never wait for operator resources (caller concern, not here).
func resolveAllocations(mode string, req *allocation, alloc *v1alpha1.ContainerAllocations) allocation {
	res := allocation{
		Ports:         req.Ports,
		FailureDomain: req.FailureDomain,
		MachineID:     req.MachineID,
		NetDevice:     req.NetDevice,
	}
	if alloc == nil {
		return res
	}

	if alloc.MachineIdentifier != "" {
		res.MachineID = alloc.MachineIdentifier
	}

	if mode == "client" {
		if network.ShouldAllocateVFPerIoNode(req.NetDevice) && len(alloc.NetDevices) > 0 {
			res.NetDevice = alloc.NetDevices[0]
		}
		return res
	}

	if res.Ports.Weka == 0 && mode != "envoy" && mode != "telemetry" {
		res.Ports.Weka = alloc.WekaPort
	}
	if res.Ports.Agent == 0 && mode != "telemetry" {
		res.Ports.Agent = alloc.AgentPort
	}
	if alloc.FailureDomain != nil {
		if *alloc.FailureDomain == "" {
			res.FailureDomain = nil
		} else {
			res.FailureDomain = alloc.FailureDomain
		}
	}
	res.Drives = alloc.Drives
	return res
}
