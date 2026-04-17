package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientTargetClusterExists rejects WekaClients whose spec.targetCluster
// refers to a WekaCluster that does not exist. The reconciler's
// FetchTargetCluster() retries forever otherwise. Skipped if Name or
// Namespace is empty (schema concern, not this policy's).
type clientTargetClusterExists struct{}

func (clientTargetClusterExists) ID() string {
	return "client_target_cluster_exists"
}

func (clientTargetClusterExists) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	tc := wc.Spec.TargetCluster
	if tc.Name == "" || tc.Namespace == "" {
		return nil
	}

	fldPath := field.NewPath("spec", "targetCluster")
	var target wekav1alpha1.WekaCluster
	err := c.Get(ctx, client.ObjectKey{Name: tc.Name, Namespace: tc.Namespace}, &target)
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		detail := fmt.Sprintf(
			"spec.targetCluster references WekaCluster %q in namespace %q, "+
				"which does not exist. The client will not be able to connect "+
				"to the target cluster. Create the WekaCluster first or fix "+
				"the targetCluster reference.",
			tc.Name, tc.Namespace,
		)
		return field.ErrorList{
			field.Invalid(fldPath, fmt.Sprintf("%s/%s", tc.Namespace, tc.Name), detail),
		}
	}
	return field.ErrorList{
		field.InternalError(fldPath, fmt.Errorf("looking up WekaCluster %s/%s: %w", tc.Namespace, tc.Name, err)),
	}
}
