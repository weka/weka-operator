package wekacluster

import (
	"context"
	"errors"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/util"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// GetFeatureFlags returns feature flags for the cluster's image.
// Checks cache first, runs operation to fetch if not cached.
// Returns flags or error (including WaitError if ad-hoc container still running).
// Callers should propagate errors up.
func (r *wekaClusterReconcilerLoop) GetFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlags")
	defer end()

	image := r.cluster.Spec.Image

	// Check cache first
	flags, err := services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
	if err == nil {
		logger.Debug("Feature flags from cache", "image", image)
		return flags, nil
	}

	// Not cached - check if it's actually a "not cached" error vs real error
	if !errors.Is(err, services.ErrFeatureFlagsNotCached) {
		return nil, err
	}

	logger.Info("Feature flags not cached, fetching via ad-hoc container", "image", image)

	// Run operation to fetch feature flags via ad-hoc container
	op := operations.NewGetFeatureFlagsFromImage(
		r.Manager.GetClient(),
		r.Manager.GetScheme(),
		operations.AdhocContainerParams{
			Image:              image,
			Labels:             r.cluster.GetLabels(),
			NodeSelector:       r.cluster.Spec.NodeSelector,
			ImagePullSecret:    r.cluster.Spec.ImagePullSecret,
			Tolerations:        util.ExpandTolerations([]corev1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
			ServiceAccountName: r.cluster.Spec.ServiceAccountName,
		},
	)
	err = operations.AsRunFunc(op)(ctx)
	if err != nil {
		// Propagate all errors including WaitError
		return nil, err
	}

	// Now should be cached - get from cache
	return services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
}
