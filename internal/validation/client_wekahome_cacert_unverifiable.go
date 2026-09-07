package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clientWekahomeCacertUnverifiable warns when a client-only WekaClient (no
// targetCluster, so the backend is an external cluster out of band) sets
// spec.wekaHome.cacertSecret. The operator has no visibility into that
// external cluster's weka_cloud_ca_cert_path, so this only helps if the two
// happen to agree. When targetCluster is set, the operator manages the
// cluster side too and the override is legitimate, so this stays silent.
type clientWekahomeCacertUnverifiable struct{}

func (clientWekahomeCacertUnverifiable) ID() string {
	return "client_wekahome_cacert_unverifiable"
}

func (clientWekahomeCacertUnverifiable) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}
	if wc.Spec.WekaHome == nil || wc.Spec.WekaHome.CacertSecret == "" {
		return nil
	}
	if wc.Spec.TargetCluster.Name != "" {
		return nil
	}

	path := field.NewPath("spec", "wekaHome", "cacertSecret")
	return field.ErrorList{
		field.Invalid(path, wc.Spec.WekaHome.CacertSecret,
			"has no targetCluster, so the backend is an external cluster the operator cannot "+
				"inspect. It cannot confirm that cluster's weka_cloud_ca_cert_path matches "+
				"/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem, so this setting only helps by "+
				"coincidence. For this topology, mount the CA bundle into the container's OS trust "+
				"store instead, per Weka's recommendation.",
		),
	}
}
