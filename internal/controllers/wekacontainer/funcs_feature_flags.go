package wekacontainer

import (
	"context"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// GetFeatureFlags returns feature flags for the container's image.
// Uses the high-level accessor which handles cache and fetching automatically.
// Returns WaitError if ad-hoc container is still running (caller should retry).
func (r *containerReconcilerLoop) GetFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	return operations.GetFeatureFlagsForContainer(
		ctx,
		r.Manager,
		r.RestClient,
		r.container,
	)
}
