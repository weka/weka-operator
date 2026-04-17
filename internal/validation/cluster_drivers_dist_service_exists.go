package validation

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterDriversDistServiceExists verifies that
// spec.driversDistService — when set to an in-cluster Kubernetes Service
// URL — points to a Service that actually exists. Hostnames are
// classified as in-cluster when they either contain a `svc` segment
// (e.g. `name.ns.svc(.cluster.local)`) or are a single label
// (`weka-driver`, treated as a Service in the WekaCluster's namespace).
// Empty values (operator auto-resolves via WekaPolicy) and other hosts
// (multi-segment names without a `svc` segment) are skipped silently.
// Malformed URLs always fail.
type clusterDriversDistServiceExists struct{}

func (clusterDriversDistServiceExists) ID() string {
	return "cluster_drivers_dist_service_exists"
}

func (clusterDriversDistServiceExists) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	raw := cluster.Spec.DriversDistService
	if raw == "" {
		return nil
	}

	fldPath := field.NewPath("spec", "driversDistService")

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return field.ErrorList{
			field.Invalid(fldPath, raw, fmt.Sprintf("malformed URL: %v", err)),
		}
	}

	host := u.Hostname()
	parts := strings.Split(host, ".")
	svcIdx := -1
	for i, p := range parts {
		if p == "svc" {
			svcIdx = i
			break
		}
	}

	var name, namespace string
	switch {
	case svcIdx > 0:
		name = parts[0]
		if svcIdx >= 2 {
			namespace = parts[1]
		} else {
			namespace = cluster.Namespace
		}
	case svcIdx == -1 && len(parts) == 1 && parts[0] != "":
		name = parts[0]
		namespace = cluster.Namespace
	default:
		return nil
	}

	var svc corev1.Service
	getErr := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &svc)
	if getErr == nil {
		return nil
	}
	if apierrors.IsNotFound(getErr) {
		detail := fmt.Sprintf(
			"spec.driversDistService references in-cluster Service %q in "+
				"namespace %q, which does not exist. The cluster will fail to "+
				"load drivers at runtime. Create the Service or change "+
				"driversDistService.",
			name, namespace,
		)
		return field.ErrorList{
			field.Invalid(fldPath, raw, detail),
		}
	}
	return field.ErrorList{
		field.InternalError(fldPath, fmt.Errorf("looking up service %s/%s: %w", namespace, name, getErr)),
	}
}
