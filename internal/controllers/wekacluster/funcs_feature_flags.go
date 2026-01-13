package wekacluster

import (
	"context"

	"github.com/weka/weka-k8s-api/util"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// GetFeatureFlags returns feature flags for the cluster's image.
// Uses the high-level accessor which handles cache and fetching automatically.
// Returns WaitError if ad-hoc container is still running (caller should retry).
func (r *wekaClusterReconcilerLoop) GetFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	return operations.GetFeatureFlagsForImage(
		ctx,
		r.Manager.GetClient(),
		r.Manager.GetScheme(),
		operations.AdhocContainerParams{
			Image:              r.cluster.Spec.Image,
			Labels:             r.cluster.GetLabels(),
			NodeSelector:       r.cluster.Spec.NodeSelector,
			ImagePullSecret:    r.cluster.Spec.ImagePullSecret,
			Tolerations:        util.ExpandTolerations([]corev1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
			ServiceAccountName: r.cluster.Spec.ServiceAccountName,
		},
	)
}
