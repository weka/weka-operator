package wekacluster

import (
	"context"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/services/discovery"
)

const (
	MaxManagementServiceEndpoints = 10
)

// EnsureManagementService creates or updates the management proxy (Envoy-based)
func (r *wekaClusterReconcilerLoop) EnsureManagementService(ctx context.Context) error {
	// Delegate to the Envoy proxy implementation
	return r.EnsureManagementProxy(ctx)
}

// selectActiveContainersForManagement selects up to 10 active containers for the management service
// Only Drive and Compute containers with the cluster's base port are selected
func (r *wekaClusterReconcilerLoop) selectActiveContainersForManagement() []*weka.WekaContainer {
	activeContainers := make([]*weka.WekaContainer, 0, MaxManagementServiceEndpoints)

	// Get the cluster's base port - only select containers with this port
	clusterBasePort := r.cluster.Status.Ports.BasePort
	if clusterBasePort == 0 {
		// No base port set yet, return empty
		return activeContainers
	}

	// Filter to only Drive and Compute containers
	eligibleModes := []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute}

	for _, container := range r.containers {
		// Only consider Drive and Compute containers
		isEligibleMode := false
		for _, mode := range eligibleModes {
			if container.Spec.Mode == mode {
				isEligibleMode = true
				break
			}
		}
		if !isEligibleMode {
			continue
		}

		// Validate container is operational (includes WekaPort check)
		if !discovery.IsContainerOperational(container) {
			continue
		}

		// Only select containers with the cluster's base port
		containerPort := container.GetPort()
		if containerPort != clusterBasePort {
			continue
		}

		// Prefer Running containers
		if container.Status.Status == weka.Running {
			activeContainers = append(activeContainers, container)
			if len(activeContainers) >= MaxManagementServiceEndpoints {
				break
			}
		}
	}

	// If we don't have enough Running containers, add other operational ones
	if len(activeContainers) < MaxManagementServiceEndpoints {
		for _, container := range r.containers {
			// Only consider Drive and Compute containers
			isEligibleMode := false
			for _, mode := range eligibleModes {
				if container.Spec.Mode == mode {
					isEligibleMode = true
					break
				}
			}
			if !isEligibleMode {
				continue
			}

			if !discovery.IsContainerOperational(container) {
				continue
			}

			// Only select containers with the cluster's base port
			containerPort := container.GetPort()
			if containerPort != clusterBasePort {
				continue
			}

			// Skip if already added
			alreadyAdded := false
			for _, added := range activeContainers {
				if added.Name == container.Name {
					alreadyAdded = true
					break
				}
			}
			if alreadyAdded {
				continue
			}

			activeContainers = append(activeContainers, container)
			if len(activeContainers) >= MaxManagementServiceEndpoints {
				break
			}
		}
	}

	return activeContainers
}
