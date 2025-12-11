package wekacontainer

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// EnsureFeatureFlags ensures that feature flags for the container's image are cached
// This step tries to:
//  1. Check if feature flags are already cached for the image
//  2. If not, run GetFeatureFlagsOperation which will:
//     a. Try to exec the current container
//     b. Try to find and exec an active container with the same image
//     c. Create an ad-hoc container if needed
func (r *containerReconcilerLoop) EnsureFeatureFlags(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureFeatureFlags")
	defer end()

	image := r.container.Status.LastAppliedImage
	if image == "" {
		image = r.container.Spec.Image
	}

	// Check if feature flags are already cached
	if services.FeatureFlagsCache.Has(ctx, image) {
		return nil
	}

	logger.Info("Feature flags not cached, running GetFeatureFlagsOperation", "image", image)

	// Create and run GetFeatureFlagsOperation
	op := operations.NewGetFeatureFlagsOperation(
		r.Manager,
		r.RestClient,
		r.container,
	)

	// Run the operation using the AsRunFunc helper
	err := operations.AsRunFunc(op)(ctx)
	if err != nil {
		logger.Error(err, "Failed to get feature flags")
		// Don't fail the whole reconciliation, feature flags are best-effort
		return nil
	}

	logger.Info("Successfully ensured feature flags", "image", image)
	return nil
}

func (r *containerReconcilerLoop) GetCachedFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetCachedFeatureFlags")
	defer end()

	image := r.container.Status.LastAppliedImage
	if image == "" {
		image = r.container.Spec.Image
	}

	// Read from cache
	flags, err := services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("error getting feature flags from cache: %w", err)
	}

	if flags == nil {
		return nil, fmt.Errorf("feature flags not cached for image: %s", image)
	}

	logger.Debug("Got feature flags from cache", "image", image, "flags", flags)

	return flags, nil
}
