// This file contains functions related to node claims reconciliation for WekaContainer controller
package wekacontainer

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// CleanupClaimsOnNode removes this container's claims from the node annotation
// This should be called during container deletion (via finalizer)
func (r *containerReconcilerLoop) CleanupClaimsOnNode(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupClaimsOnNode")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		logger.Debug("Container has no node affinity, nothing to clean up")
		return nil
	}

	// Get the node
	node := &v1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		if apierrors.IsNotFound(err) {
			// Node is gone, nothing to clean up
			logger.Info("Node not found, nothing to clean up", "node", nodeName)
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	// Parse existing claims
	claims, err := allocator.ParseNodeClaims(node)
	if err != nil {
		logger.Warn("Failed to parse node claims during cleanup, skipping", "error", err)
		// If we can't parse, just skip cleanup - next allocation will rebuild
		return nil
	}

	// Remove our claims
	claimKey := allocator.ClaimKeyFromContainer(r.container)
	claims.RemoveClaims(claimKey)

	// Save updated claims
	if err := claims.SaveToNode(ctx, r.Client, node); err != nil {
		if apierrors.IsNotFound(err) {
			// Node was deleted while we were working, that's OK
			logger.Info("Node was deleted during cleanup", "node", nodeName)
			return nil
		}
		if apierrors.IsConflict(err) {
			// Another controller updated the node, that's OK
			// The claims will be cleaned up on next reconciliation or rebuild
			logger.Info("Node update conflict during cleanup, will be cleaned up later")
			return nil
		}
		return fmt.Errorf("failed to save claims after cleanup: %w", err)
	}

	logger.Info("Successfully cleaned up claims on node", "node", nodeName)
	return nil
}
