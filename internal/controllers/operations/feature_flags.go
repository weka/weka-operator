// Package operations provides high-level accessors for feature flags.
// These functions encapsulate cache checking and fetching logic so controllers
// don't need to manage cache directly.
package operations

import (
	"context"
	"errors"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

// GetFeatureFlagsForImage returns feature flags for the given image.
// It checks the cache first. If not cached, it creates an ad-hoc container
// to fetch the flags (returning WaitError while the container is running).
//
// This is the high-level accessor for cluster-level use where no running
// container is available to exec into.
//
// Parameters:
//   - ctx: context for the operation
//   - k8sClient: Kubernetes client for creating ad-hoc containers
//   - scheme: runtime scheme for owner references
//   - params: parameters for the ad-hoc container (image, scheduling, etc.)
//
// Returns:
//   - Feature flags if available
//   - WaitError if ad-hoc container is still running (caller should retry)
//   - Other error if something failed
func GetFeatureFlagsForImage(
	ctx context.Context,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	params AdhocContainerParams,
) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlagsForImage", "image", params.Image)
	defer end()

	// Check cache first
	flags, err := services.GetFeatureFlags(ctx, params.Image)
	if err == nil {
		logger.Debug("Feature flags from cache")
		return flags, nil
	}

	// If it's not a "not cached" error, propagate it
	if !errors.Is(err, services.ErrFeatureFlagsNotCached) {
		return nil, err
	}

	logger.Info("Feature flags not cached, fetching via ad-hoc container")

	// Run operation to fetch feature flags via ad-hoc container
	op := NewGetFeatureFlagsFromImage(k8sClient, scheme, params)
	err = AsRunFunc(op)(ctx)
	if err != nil {
		// Propagate all errors including WaitError
		return nil, err
	}

	// Now should be cached - get from cache
	return services.GetFeatureFlags(ctx, params.Image)
}

// GetFeatureFlagsForContainer returns feature flags for a container's image.
// It checks the cache first. If not cached, it tries to:
//  1. Exec into the current container to read flags
//  2. Find and exec into another active container with the same image
//  3. Create an ad-hoc container as fallback
//
// This is the high-level accessor for container-level use where we can
// try to exec into existing containers before falling back to ad-hoc.
//
// Parameters:
//   - ctx: context for the operation
//   - mgr: controller manager for client access
//   - restClient: REST client for exec operations
//   - container: the container to get feature flags for
//
// Returns:
//   - Feature flags if available
//   - WaitError if ad-hoc container is still running (caller should retry)
//   - Other error if something failed
func GetFeatureFlagsForContainer(
	ctx context.Context,
	mgr ctrl.Manager,
	restClient rest.Interface,
	container *weka.WekaContainer,
) (*domain.FeatureFlags, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetFeatureFlagsForContainer", "container", container.Name)
	defer end()

	// Determine the image to use
	image := container.Status.LastAppliedImage
	if image == "" {
		image = container.Spec.Image
	}

	// Check cache first
	flags, err := services.GetFeatureFlags(ctx, image)
	if err == nil {
		logger.Debug("Feature flags from cache", "image", image)
		return flags, nil
	}

	// If it's not a "not cached" error, propagate it
	if !errors.Is(err, services.ErrFeatureFlagsNotCached) {
		return nil, err
	}

	logger.Info("Feature flags not cached, fetching via operation", "image", image)

	// Run operation which will:
	// 1. Try to exec the current container
	// 2. Try to find and exec an active container with the same image
	// 3. Create an ad-hoc container if needed
	op := NewGetFeatureFlagsOperation(mgr, restClient, container)
	err = AsRunFunc(op)(ctx)
	if err != nil {
		// Propagate all errors including WaitError
		return nil, err
	}

	// Now should be cached - get from cache
	return services.GetFeatureFlags(ctx, image)
}
