package wekacluster

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/operations"
	util2 "github.com/weka/weka-operator/pkg/util"
)

// getAnnotationPrePull returns the annotation key for pre-pull tracking based on mode/role
// Format: "weka.io/prepull-{mode}"
// Examples: "weka.io/prepull-drive", "weka.io/prepull-compute", "weka.io/prepull-client"
func getAnnotationPrePull(mode string) string {
	return fmt.Sprintf("weka.io/prepull-%s", mode)
}

func (r *wekaClusterReconcilerLoop) handleImagePrePull(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleImagePrePull")
	defer end()

	roles := []string{"all", "drive", "compute", "s3", "nfs"}

	commonNodeSelector := r.cluster.Spec.NodeSelector

	g, ctx := errgroup.WithContext(ctx)

	// We should create separate pre-pull daemonsets for each role to ensure
	// that nodes with different node selectors are covered.
	for _, role := range roles {
		// NOTE: we only need to run separate DS per role if node selectors differ
		if role != "all" {
			perRoleNodeSelector := r.cluster.GetNodeSelectorForRole(role)

			if util2.AreMapsEqual(commonNodeSelector, perRoleNodeSelector) {
				// Node selector is the same as common one, no need for separate DS
				logger.Debug("Node selector for role matches common selector, no separate pre-pull needed", "role", role)
				continue
			}
		}

		// Check if pre-pull needed
		if !r.needsPrePullForRole(role) {
			logger.Info("Pre-pull already completed for role, skipping", "role", role)
			return nil
		}
		// Launch goroutine for role
		g.Go(func(role string) func() error {
			return func() error {
				err := r.handleImagePrePullForRole(ctx, role)
				if err != nil {
					logger.Error(err, "Failed to handle image pre-pull for role", "role", role)
					return err
				}
				return nil
			}
		}(role))
	}

	if err := g.Wait(); err != nil {
		return err
	}

	logger.Info("Completed image pre-pull handling for all roles")

	return nil
}

func (r *wekaClusterReconcilerLoop) handleImagePrePullForRole(ctx context.Context, role string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleImagePrePullForRole", "role", role)
	defer end()

	cluster := r.cluster

	logger.Debug("Starting image pre-pull before upgrade",
		"targetImage", cluster.Spec.Image,
		"currentImage", cluster.Status.LastAppliedImage,
	)

	nodeSelector := cluster.GetNodeSelectorForRole(role)
	// Get tolerations - use ExpandTolerations from k8sutil
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations)

	payload := &operations.PrePullImagePayload{
		TargetImage:     cluster.Spec.Image,
		NodeSelector:    nodeSelector,
		Tolerations:     tolerations,
		ImagePullSecret: cluster.Spec.ImagePullSecret,
		OwnerKind:       cluster.Kind,
		OwnerMeta:       cluster,
		Role:            role,
	}

	// Create and execute the pre-pull operation
	prePullOp := operations.NewPrePullImageOperation(r.Manager, payload)
	err := operations.ExecuteOperation(ctx, prePullOp)
	if err != nil {
		logger.Error(err, "Pre-pull operation failed")
		return err
	}

	logger.Info("Image pre-pull operation completed", "result", prePullOp.GetJsonResult())

	// Set annotation
	err = r.setPrePullAnnotationForRole(ctx, role)
	if err != nil {
		logger.Error(err, "Failed to set pre-pull annotation, will retry pre-pull on next reconciliation")
		// Don't return error - pre-pull succeeded, annotation failure is not critical
	}

	return nil
}

// needsPrePullForRole checks if pre-pull is needed
func (r *wekaClusterReconcilerLoop) needsPrePullForRole(role string) bool {
	cluster := r.cluster
	annotationKey := getAnnotationPrePull(role)

	annotations := cluster.GetAnnotations()
	if annotations == nil {
		return true
	}

	prePulledImage, exists := annotations[annotationKey]
	if !exists {
		return true
	}

	return prePulledImage != cluster.Spec.Image
}

// setPrePullAnnotationForRole sets annotation after successful pre-pull
func (r *wekaClusterReconcilerLoop) setPrePullAnnotationForRole(ctx context.Context, role string) error {
	annotationKey := getAnnotationPrePull(role)

	// Get fresh copy to avoid conflicts
	cluster := &weka.WekaCluster{}
	err := r.getClient().Get(ctx, client.ObjectKey{
		Namespace: r.cluster.Namespace,
		Name:      r.cluster.Name,
	}, cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get cluster")
	}

	patch := client.MergeFrom(cluster.DeepCopy())

	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations[annotationKey] = cluster.Spec.Image

	return r.getClient().Patch(ctx, cluster, patch)
}

// clearAllPrePullAnnotations removes annotations after successful upgrade
func (r *wekaClusterReconcilerLoop) clearAllPrePullAnnotations(ctx context.Context) error {
	// Check local object first to avoid unnecessary API calls
	if r.cluster.Annotations == nil {
		return nil
	}

	// Check if any pre-pull annotations exist locally
	hasAnnotations := false
	for _, role := range []string{"drive", "compute", "s3", "nfs"} {
		annotationKey := getAnnotationPrePull(role)
		if _, exists := r.cluster.Annotations[annotationKey]; exists {
			hasAnnotations = true
			break
		}
	}

	if !hasAnnotations {
		return nil
	}

	// Annotations exist locally, fetch fresh copy to clear them
	cluster := &weka.WekaCluster{}
	err := r.getClient().Get(ctx, client.ObjectKey{
		Namespace: r.cluster.Namespace,
		Name:      r.cluster.Name,
	}, cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get cluster")
	}

	if cluster.Annotations == nil {
		return nil
	}

	patch := client.MergeFrom(cluster.DeepCopy())

	// Remove all pre-pull annotations
	changed := false
	for _, role := range []string{"drive", "compute", "s3", "nfs"} {
		annotationKey := getAnnotationPrePull(role)
		if _, exists := cluster.Annotations[annotationKey]; exists {
			delete(cluster.Annotations, annotationKey)
			changed = true
		}
	}

	if !changed {
		return nil
	}

	return r.getClient().Patch(ctx, cluster, patch)
}
