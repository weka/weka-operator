package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// validateNfsConfig validates NFS configuration to ensure Weka constraints are met.
// Weka only allows one NFS interface per host in an interface group.
func validateNfsConfig(cluster *weka.WekaCluster) error {
	if cluster.Spec.NFSConfig == nil {
		return nil
	}

	if len(cluster.Spec.NFSConfig.Interfaces) > 1 {
		return fmt.Errorf(
			"NFSConfig.interfaces must contain at most 1 interface; got %d interfaces: %v. "+
				"NFS interface groups in Weka only support a single interface per host",
			len(cluster.Spec.NFSConfig.Interfaces),
			cluster.Spec.NFSConfig.Interfaces,
		)
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureNfs(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureNfs")
	defer logger.End()

	// Validate NFS configuration before proceeding
	if err := validateNfsConfig(r.cluster); err != nil {
		logger.SetError(err, "NFS configuration validation failed")
		return err
	}

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err := wekaService.ConfigureNfs(ctx, &services.NFSParams{
		ConfigFilesystem: ".config_fs",
		MountdPort:       config.Config.Nfs.MountdPort,
		LockmanagerPort:  config.Config.Nfs.LockmanagerPort,
		NotifyPort:       config.Config.Nfs.NotifyPort,
	})

	if err != nil {
		var nfsIgExists *services.NfsInterfaceGroupExists
		if !errors.As(err, &nfsIgExists) {
			return err
		}

		logger.Info("Tolerating pre-existing NFS interface group", "error", err)
	}

	logger.SetStatus(codes.Ok, "NFS ensured")

	return nil
}

// ShouldDestroyNfs returns true when the operator-managed NFS interface group should be
// removed: the spec no longer requests NFS containers, no NFS containers remain, and NFS was
// previously configured by the operator. This cleans up the interface group (and its floating
// IPs) that would otherwise be left behind after NFS is torn down while the cluster stays up.
func (r *wekaClusterReconcilerLoop) ShouldDestroyNfs() bool {
	if !r.cluster.Spec.GetOverrides().AllowNfsInterfaceGroupDestroy {
		return false
	}

	// if the spec still requests NFS containers, keep the interface group
	nums := allocator.GetWekaContainerNumbers(r.cluster.Spec.Dynamic)
	if nums.Nfs > 0 {
		return false
	}

	// if any NFS container still exists, wait for it to be removed before removing the group
	containers := discovery.SelectContainersByRole(r.containers, weka.WekaContainerModeNfs)
	if len(containers) > 0 {
		return false
	}

	// if NFS was never configured by the operator, there is nothing to destroy
	if !meta.IsStatusConditionTrue(r.cluster.Status.Conditions, condition.ConfNfsConfigured) {
		return false
	}

	return true
}

// DestroyNfs removes the operator-managed NFS interface group and clears the NfsConfigured
// and NfsIpRangesConfigured conditions so the interface group (and its floating IP ranges) is
// recreated and reconfigured if NFS is re-enabled later.
func (r *wekaClusterReconcilerLoop) DestroyNfs(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "destroyNfs")
	defer logger.End()

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		// no container to run the CLI against yet; retry on a later reconcile
		return lifecycle.NewWaitError(fmt.Errorf("no active container available to destroy NFS interface group"))
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	logger.Info("Destroying NFS interface group", "interfaceGroup", services.NfsInterfaceGroupName)
	if err := wekaService.DeleteNfsInterfaceGroup(ctx, services.NfsInterfaceGroupName); err != nil {
		return errors.Wrap(err, "failed to delete NFS interface group")
	}

	// invalidate the NFS configured condition so we don't attempt to destroy again
	changed := meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.ConfNfsConfigured,
		Status: metav1.ConditionFalse,
		Reason: "DestroyNfs",
	})
	// removing the interface group also drops its floating IP ranges; clear the IP-ranges
	// condition so ShouldConfigureNfsIpRanges re-runs and reapplies them on re-enable (even if
	// the spec IpRanges are unchanged and would otherwise match the stale hash)
	if meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondNfsIpRangesConfigured,
		Status: metav1.ConditionFalse,
		Reason: "DestroyNfs",
	}) {
		changed = true
	}
	if changed {
		if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "NFS interface group destroyed")

	return nil
}

// ShouldConfigureNfsIpRanges returns true if NFS IP ranges need to be configured.
// It checks the condition hash against the current spec hash.
func (r *wekaClusterReconcilerLoop) ShouldConfigureNfsIpRanges() bool {
	// Get target IP ranges from cluster spec
	targetIpRanges := []string{}
	if r.cluster.Spec.NFSConfig != nil {
		targetIpRanges = r.cluster.Spec.NFSConfig.IpRanges
	}

	// Calculate hash of target IP ranges
	currentHash := calculateIpRangesHash(targetIpRanges)

	// Check if condition exists and hash matches
	ipRangesCond := meta.FindStatusCondition(r.cluster.Status.Conditions, condition.CondNfsIpRangesConfigured)
	if ipRangesCond != nil && ipRangesCond.Status == metav1.ConditionTrue && ipRangesCond.Message == currentHash {
		return false // Already configured with current spec
	}

	return true // Needs configuration
}

// EnsureNfsIpRanges ensures the NFS interface group has the correct IP ranges.
// It fetches current state and reconciles to desired state.
func (r *wekaClusterReconcilerLoop) EnsureNfsIpRanges(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureNfsIpRanges")
	defer logger.End()

	// Get target IP ranges from cluster spec
	targetIpRanges := []string{}
	if r.cluster.Spec.NFSConfig != nil {
		targetIpRanges = r.cluster.Spec.NFSConfig.IpRanges
	}

	// Calculate hash of target IP ranges
	currentHash := calculateIpRangesHash(targetIpRanges)

	// Configure IP ranges
	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err := wekaService.EnsureNfsIpRanges(ctx, services.NfsInterfaceGroupName, targetIpRanges)
	if err != nil {
		logger.SetError(err, "Failed to ensure NFS IP ranges")
		// Set condition to false on failure
		meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
			Type:    condition.CondNfsIpRangesConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  "ConfigurationFailed",
			Message: err.Error(),
		})
		if updateErr := r.getClient().Status().Update(ctx, r.cluster); updateErr != nil {
			logger.Error(updateErr, "Failed to update cluster status with error condition")
		}
		return err
	}

	// Update condition with the hash as the message
	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondNfsIpRangesConfigured,
		Status:  metav1.ConditionTrue,
		Reason:  "Configured",
		Message: currentHash,
	})

	// Persist the status update
	if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
		logger.SetError(err, "Failed to update cluster status with IP ranges hash")
		return err
	}

	logger.Info("NFS IP ranges configured successfully", "hash", currentHash)
	logger.SetStatus(codes.Ok, "NFS IP ranges ensured")

	return nil
}

// calculateIpRangesHash creates a deterministic hash of IP ranges for change detection
func calculateIpRangesHash(ipRanges []string) string {
	if len(ipRanges) == 0 {
		return "empty"
	}

	// Sort to ensure consistent hash regardless of order
	sorted := make([]string, len(ipRanges))
	copy(sorted, ipRanges)
	sort.Strings(sorted)

	// Calculate SHA256 hash
	hash := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(hash[:])
}
