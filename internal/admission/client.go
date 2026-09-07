package admission

import (
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newValidatorClient builds a client for admission validators: cache-backed for everything (the
// Node lists most validators run), except corev1.Secret. The cacert validators read a Secret by
// exact name; a cache-backed read would start a cluster-wide Secret informer whose first sync is a
// full LIST blocking an admission request under failurePolicy: Fail, and informer lag can return a
// stale NotFound for a Secret created moments earlier.
func newValidatorClient(mgr ctrl.Manager) (client.Client, error) {
	return client.New(mgr.GetConfig(), client.Options{
		Scheme:     mgr.GetScheme(),
		HTTPClient: mgr.GetHTTPClient(),
		Mapper:     mgr.GetRESTMapper(),
		Cache: &client.CacheOptions{
			Reader:     mgr.GetCache(),
			DisableFor: []client.Object{&corev1.Secret{}},
		},
	})
}
