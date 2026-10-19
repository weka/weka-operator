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

// ssdProxyUsesVidLabel reports the node ssdproxy image's ssd_proxy_vid_label_add flag, which decides
// whether the allocator fills in the VID or leaves it for the proxy to return.
func (r *containerReconcilerLoop) ssdProxyUsesVidLabel(ctx context.Context) (bool, error) {
	proxy, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return false, err
	}
	image := proxy.Status.LastAppliedImage
	if image == "" {
		image = proxy.Spec.Image
	}
	// The ssdproxy runtime never writes its feature flags file, so they come from the image.
	flags, err := operations.GetFeatureFlagsForImage(ctx, r.Manager.GetClient(), r.Manager.GetScheme(), &operations.AdhocContainerParams{
		Image:           image,
		NodeAffinity:    proxy.GetNodeAffinity(),
		ImagePullSecret: proxy.Spec.ImagePullSecret,
		Tolerations:     proxy.Spec.Tolerations,
	})
	if err != nil {
		return false, err
	}
	return flags.SsdProxyVidLabelAdd, nil
}
