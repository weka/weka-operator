package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientWekahomeCacertNotInherited warns when a WekaClient targets a cross-namespace WekaCluster that
// has its own cacertSecret: GetWekaHomeClientCacertSecret only inherits the cluster's Secret when the
// cluster shares the client's namespace (the Secret is mounted by name, so a cross-namespace name would
// not resolve), so the client silently falls back to the operator-wide default instead.
type clientWekahomeCacertNotInherited struct{}

func (clientWekahomeCacertNotInherited) ID() string {
	return "client_wekahome_cacert_not_inherited"
}

func (clientWekahomeCacertNotInherited) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	tc := wc.Spec.TargetCluster
	if tc.Name == "" || tc.Namespace == "" {
		return nil
	}

	var targetCluster wekav1alpha1.WekaCluster
	// clientTargetClusterExists already owns a not-found (or any other Get error); fail open here rather
	// than double-report it.
	if err := c.Get(ctx, client.ObjectKey{Name: tc.Name, Namespace: tc.Namespace}, &targetCluster); err != nil {
		return nil
	}

	_, crossNamespaceSkipped := domain.GetWekaHomeClientCacertSecret(wc, &targetCluster)
	if !crossNamespaceSkipped {
		return nil
	}

	// Reported against targetCluster, the field that actually triggers this: inheritance is only
	// skipped when the client sets no cacertSecret of its own, so spec.wekaHome.cacertSecret is
	// empty here and usually absent altogether.
	path := field.NewPath("spec", "targetCluster")
	return field.ErrorList{
		field.Invalid(path, tc.Namespace+"/"+tc.Name,
			fmt.Sprintf("targetCluster %s/%s sets spec.wekaHome.cacertSecret %q, but it lives in a "+
				"different namespace so this client will not inherit it. Set spec.wekaHome.cacertSecret "+
				"on this WekaClient explicitly if it needs to trust the same CA.",
				tc.Namespace, tc.Name, targetCluster.Spec.WekaHome.CacertSecret),
		),
	}
}
