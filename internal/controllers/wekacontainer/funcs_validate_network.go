package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	v1 "k8s.io/api/core/v1"
)

// validateNetworkConfig validates the network configuration for the container
func (r *containerReconcilerLoop) validateNetworkConfig(ctx context.Context) error {
	spec := r.container.Spec
	network := spec.Network

	// Check 1: Mutual exclusivity - selectors vs legacy fields
	if len(network.Selectors) > 0 {
		if network.EthDevice != "" || len(network.EthDevices) > 0 || len(network.DeviceSubnets) > 0 {
			err := fmt.Errorf("network selectors cannot be used together with ethDevice, ethDevices, or deviceSubnets; use one or the other")
			_ = r.RecordEvent(v1.EventTypeWarning, "InvalidNetworkConfig", err.Error()) //nolint:errcheck // error return value intentionally not checked
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}
	}

	// Check 2: Single selector with rdmaOnly is invalid
	if len(network.Selectors) == 1 && network.Selectors[0].RdmaOnly {
		err := fmt.Errorf("a single network selector with rdmaOnly is invalid; at least one non-rdmaOnly selector is required for data traffic")
		_ = r.RecordEvent(v1.EventTypeWarning, "InvalidNetworkConfig", err.Error()) //nolint:errcheck // error return value intentionally not checked
		return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
	}

	// Check 3: All selectors rdmaOnly without management selectors
	if len(network.Selectors) > 0 {
		allRdmaOnly := true
		for _, s := range network.Selectors {
			if !s.RdmaOnly {
				allRdmaOnly = false
				break
			}
		}
		if allRdmaOnly && len(network.ManagementIPsSelectors) == 0 {
			err := fmt.Errorf("all network selectors have rdmaOnly set but no managementIpsSelectors are configured; rdma-only NICs cannot serve as management IPs")
			_ = r.RecordEvent(v1.EventTypeWarning, "InvalidNetworkConfig", err.Error()) //nolint:errcheck // error return value intentionally not checked
			return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
		}
	}

	return nil
}
