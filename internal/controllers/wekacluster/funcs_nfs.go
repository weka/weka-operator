package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) EnsureNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfs")
	defer end()

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err := wekaService.ConfigureNfs(ctx, services.NFSParams{
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
	}

	logger.SetStatus(codes.Ok, "NFS ensured")

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
	condition := meta.FindStatusCondition(r.cluster.Status.Conditions, "NfsIpRangesConfigured")
	if condition != nil && condition.Status == metav1.ConditionTrue && condition.Message == currentHash {
		return false // Already configured with current spec
	}

	return true // Needs configuration
}

// EnsureNfsIpRanges ensures the NFS interface group has the correct IP ranges.
// It fetches current state and reconciles to desired state.
func (r *wekaClusterReconcilerLoop) EnsureNfsIpRanges(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfsIpRanges")
	defer end()

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

	err := wekaService.EnsureNfsIpRanges(ctx, "MgmtInterfaceGroup", targetIpRanges)
	if err != nil {
		logger.SetError(err, "Failed to ensure NFS IP ranges")
		// Set condition to false on failure
		meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
			Type:    "NfsIpRangesConfigured",
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
		Type:    "NfsIpRangesConfigured",
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
