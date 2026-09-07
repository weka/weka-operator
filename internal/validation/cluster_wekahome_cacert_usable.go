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

// clusterWekahomeCacertUsable rejects a WekaCluster whose resolved WekaHome cacert Secret exists but
// yields no usable X.509 certificate. Without one, weka_runtime.py stages nothing into the CA path
// even though the cluster-wide override still points at it, silently breaking Weka Home TLS since an
// explicit CA path replaces the OS trust store.
//
// A missing Secret is skipped here rather than flagged: it is routinely transient (a GitOps sweep
// applying the WekaCluster before the Secret, or ExternalSecrets materialising it seconds later)
// and self-heals once the Secret appears.
type clusterWekahomeCacertUsable struct{}

func (clusterWekahomeCacertUsable) ID() string {
	return "cluster_wekahome_cacert_usable"
}

func (clusterWekahomeCacertUsable) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	// The resolved value, not the spec field: the operator-wide default is equally what the pod
	// mounts and what the override is set from, and it is not guaranteed to exist in every
	// namespace a cluster is created in.
	secretName := clusterCacertSecretName(cluster)
	if secretName == "" {
		return nil
	}

	fldPath := field.NewPath("spec", "wekaHome", "cacertSecret")
	problem, notFound, err := cacertSecretProblem(ctx, c, cluster.Namespace, secretName)
	if err != nil {
		return field.ErrorList{field.InternalError(fldPath, err)}
	}
	if problem == "" || notFound {
		return nil
	}
	return field.ErrorList{
		field.Invalid(fldPath, secretName,
			fmt.Sprintf("%s. The cluster-wide weka_cloud_ca_cert_path override still gets set to "+
				"%s, but no pod stages anything there, and an explicit CA path replaces the OS "+
				"trust store — so Weka Home TLS would break for the whole cluster.%s",
				problem, domain.WekaHomeCacertPath, cacertSourceNote(cluster))),
	}
}
