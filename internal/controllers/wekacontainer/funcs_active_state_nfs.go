package wekacontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/services"
)

const nfsInterfaceGroupName = "MgmtInterfaceGroup"

// getTargetNfsInterfaces determines which interfaces should be used for NFS.
// Priority order:
//  1. NFSConfig.Interfaces from WekaCluster spec (explicit cluster-level config)
//  2. EthDevice from container spec (single interface)
//  3. EthDevices from container spec (multiple interfaces)
//
// Note: DeviceSubnets/Selectors are used for data interface auto-discovery, but NFS requires
// explicit interface names. If only auto-discovery is configured without explicit interfaces,
// an error is returned.
func (r *containerReconcilerLoop) getTargetNfsInterfaces(ctx context.Context) ([]string, error) {
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return nil, err
	}

	// Priority 1: Use cluster-level NFS interfaces if specified
	if cluster != nil && cluster.Spec.NFSConfig != nil && len(cluster.Spec.NFSConfig.Interfaces) > 0 {
		return cluster.Spec.NFSConfig.Interfaces, nil
	}

	// Priority 2: Use EthDevice if set (single interface)
	if r.container.Spec.Network.EthDevice != "" {
		return []string{r.container.Spec.Network.EthDevice}, nil
	}

	// Priority 3: Use EthDevices if set (multiple interfaces)
	if len(r.container.Spec.Network.EthDevices) > 0 {
		targetInterfaces := r.container.Spec.Network.EthDevices

		// Validate single interface constraint for NFS
		if len(targetInterfaces) > 1 {
			return nil, fmt.Errorf(
				"NFS configuration validation failed: multiple interfaces specified (%d). "+
					"NFS supports only a single interface per host. "+
					"Interfaces: %v",
				len(targetInterfaces),
				targetInterfaces,
			)
		}

		return targetInterfaces, nil
	}

	// No explicit interfaces available - check if auto-discovery is configured
	// (which doesn't work for NFS)
	if len(r.container.Spec.Network.DeviceSubnets) > 0 {
		return nil, errors.New("NFS interface group configuration with DeviceSubnets is not supported; use EthDevice, EthDevices, or cluster-level NFSConfig.Interfaces instead")
	}
	if len(r.container.Spec.Network.Selectors) > 0 {
		return nil, errors.New("NFS interface group configuration with network Selectors is not supported; use EthDevice, EthDevices, or cluster-level NFSConfig.Interfaces instead")
	}

	return nil, errors.New("no network interfaces configured for NFS; configure EthDevice, EthDevices, or cluster-level NFSConfig.Interfaces")
}

// calculateInterfacesHash creates a deterministic hash of interfaces for change detection
func calculateInterfacesHash(interfaces []string) string {
	if len(interfaces) == 0 {
		return "empty"
	}

	// Sort to ensure consistent hash regardless of order
	sorted := make([]string, len(interfaces))
	copy(sorted, interfaces)
	sort.Strings(sorted)

	// Calculate SHA256 hash
	hash := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes for shorter hash
}

// ShouldEnsureNfsInterfaceGroupPorts returns true if NFS interface group ports need to be configured.
// It checks the condition hash against the current target interfaces hash.
func (r *containerReconcilerLoop) ShouldEnsureNfsInterfaceGroupPorts(targetInterfaces []string) bool {
	currentHash := calculateInterfacesHash(targetInterfaces)

	// Check if condition exists and hash matches
	cond := meta.FindStatusCondition(r.container.Status.Conditions, condition.CondNfsInterfaceGroupsConfigured)
	if cond != nil && cond.Status == metav1.ConditionTrue && cond.Message == currentHash {
		return false // Already configured with current spec
	}

	return true // Needs configuration
}

// validateNfsNetworkConfiguration validates that NFS containers have at most one network interface configured.
func (r *containerReconcilerLoop) validateNfsNetworkConfiguration() error {
	if !r.container.IsNfsContainer() {
		return nil
	}

	ethDeviceCount := 0
	if r.container.Spec.Network.EthDevice != "" {
		ethDeviceCount = 1
	}
	ethDeviceCount += len(r.container.Spec.Network.EthDevices)

	if ethDeviceCount > 1 {
		return fmt.Errorf(
			"NFS container mode only supports a single network interface per host; "+
				"container %s has %d interfaces configured (ethDevice: %q, ethDevices: %v)",
			r.container.Name,
			ethDeviceCount,
			r.container.Spec.Network.EthDevice,
			r.container.Spec.Network.EthDevices,
		)
	}

	return nil
}

func (r *containerReconcilerLoop) EnsureNfsInterfaceGroupPorts(ctx context.Context) error {
	// Validate NFS network configuration before proceeding
	if err := r.validateNfsNetworkConfiguration(); err != nil {
		return err
	}

	targetInterfaces, err := r.getTargetNfsInterfaces(ctx)
	if err != nil {
		return err
	}

	if !r.ShouldEnsureNfsInterfaceGroupPorts(targetInterfaces) {
		return nil // Already configured
	}

	currentHash := calculateInterfacesHash(targetInterfaces)

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureNfsInterfaceGroupPorts", "hash", currentHash)
	defer end()

	wekaService := services.NewWekaService(r.ExecService, r.container)
	err = wekaService.EnsureNfsInterfaceGroupPorts(ctx, nfsInterfaceGroupName, *r.container.Status.ClusterContainerID, targetInterfaces)
	if err != nil {
		return err
	}

	// Update condition with the hash as the message for change detection
	meta.SetStatusCondition(&r.container.Status.Conditions, metav1.Condition{
		Type:    condition.CondNfsInterfaceGroupsConfigured,
		Status:  metav1.ConditionTrue,
		Reason:  "Configured",
		Message: currentHash,
	})

	if updateErr := r.Client.Status().Update(ctx, r.container); updateErr != nil {
		logger.Error(updateErr, "Failed to update container status with NFS interface group condition")
		return updateErr
	}

	return nil
}
