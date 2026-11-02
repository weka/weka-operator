package wekacluster

import (
	"context"
	"time"

	"github.com/weka/weka-operator/pkg/weka-k8s-api/util"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// GetFeatureFlags returns feature flags for the cluster's image.
// Uses the high-level accessor which handles cache and fetching automatically.
// Returns WaitError if ad-hoc container is still running (caller should retry).
func (r *wekaClusterReconcilerLoop) GetFeatureFlags(ctx context.Context) (*domain.FeatureFlags, error) {
	return operations.GetFeatureFlagsForImage(
		ctx,
		r.Manager.GetClient(),
		r.Manager.GetScheme(),
		&operations.AdhocContainerParams{
			Image:           r.cluster.Spec.Image,
			Labels:          r.cluster.GetLabels(),
			NodeSelector:    r.cluster.Spec.NodeSelector,
			ImagePullSecret: r.cluster.Spec.ImagePullSecret,
			Tolerations:     util.ExpandTolerations([]corev1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
		},
	)
}

// GetFeatureFlagsForContainer returns feature flags for the image a container is actually running,
// which is what callers parsing that container's CLI output need: mid-upgrade the cluster spec
// already names the new image while the container still runs the old one. It also reaches the
// flags by exec'ing the container rather than starting an ad-hoc one.
func (r *wekaClusterReconcilerLoop) GetFeatureFlagsForContainer(ctx context.Context, container *weka.WekaContainer) (*domain.FeatureFlags, error) {
	return operations.GetFeatureFlagsForContainer(ctx, r.Manager, r.RestClient, container)
}

// WekaServiceForContainer builds a service bound to the container. Only the methods that still read
// a generation off the feature flags need WekaServiceWithFlagsForContainer; everything else resolves
// the CLI generation from the payload it parses.
func (r *wekaClusterReconcilerLoop) WekaServiceForContainer(container *weka.WekaContainer, timeout *time.Duration) services.WekaService {
	return services.NewWekaServiceWithTimeout(r.ExecService, container, timeout)
}

// WekaServiceWithFlagsForContainer additionally binds the feature flags of the image the container is
// running. Returns WaitError if the flags are still being fetched, so prefer WekaServiceForContainer
// unless calling a method that requires them.
func (r *wekaClusterReconcilerLoop) WekaServiceWithFlagsForContainer(ctx context.Context, container *weka.WekaContainer, timeout *time.Duration) (services.WekaService, error) {
	featureFlags, err := r.GetFeatureFlagsForContainer(ctx, container)
	if err != nil {
		return nil, err
	}
	return services.NewWekaServiceWithTimeout(r.ExecService, container, timeout).WithFeatureFlags(featureFlags), nil
}
