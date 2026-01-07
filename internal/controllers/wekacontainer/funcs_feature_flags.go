package wekacontainer

import (
	"context"
	"errors"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// GetFeatureFlags returns feature flags for the container's image.
// Checks cache first, runs operation to fetch if not cached.
// Returns flags or error (including WaitError if ad-hoc container still running).
// Callers should propagate errors up.
func (r *containerReconcilerLoop) GetFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlags")
	defer end()

	image := r.container.Status.LastAppliedImage
	if image == "" {
		image = r.container.Spec.Image
	}

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

	logger.Info("Feature flags not cached, fetching via operation", "image", image)

	// Create and run GetFeatureFlagsOperation which will:
	// 1. Try to exec the current container
	// 2. Try to find and exec an active container with the same image
	// 3. Create an ad-hoc container if needed
	op := operations.NewGetFeatureFlagsOperation(
		r.Manager,
		r.RestClient,
		r.container,
	)

	// Run the operation using the AsRunFunc helper
	err = operations.AsRunFunc(op)(ctx)
	if err != nil {
		// Propagate all errors including WaitError
		return nil, err
	}

	// Now should be cached - get from cache
	return services.FeatureFlagsCache.GetFeatureFlags(ctx, image)
}

// EnsureFeatureFlags ensures that feature flags for the container's image are cached.
// This is a convenience wrapper around GetFeatureFlags for use as a reconciliation step.
func (r *containerReconcilerLoop) EnsureFeatureFlags(ctx context.Context) error {
	_, err := r.GetFeatureFlags(ctx)
	return err
}
