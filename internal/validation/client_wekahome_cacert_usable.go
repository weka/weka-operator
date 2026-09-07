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

// clientWekahomeCacertUsable warns when the WekaHome cacert Secret a WekaClient resolves to yields
// no usable X.509 certificate: weka_runtime.py stages nothing into WekaHomeCacertPath, so the
// client silently falls back to its container's OS trust store and only fails against a private
// Weka Home endpoint, not a public one, with no other signal that it's misconfigured.
//
// A warning, not an error like the cluster case: a client sets no cluster-wide
// weka_cloud_ca_cert_path override, so nothing else depends on this Secret being usable.
type clientWekahomeCacertUsable struct{}

func (clientWekahomeCacertUsable) ID() string {
	return "client_wekahome_cacert_usable"
}

func (clientWekahomeCacertUsable) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	wc, ok := obj.(*wekav1alpha1.WekaClient)
	if !ok {
		return nil
	}

	// The target cluster participates in resolution (see GetWekaHomeClientCacertSecret).
	// clientTargetClusterExists already owns a missing or unreadable target, so fail open here
	// and resolve without it rather than double-report.
	var targetCluster *wekav1alpha1.WekaCluster
	if tc := wc.Spec.TargetCluster; tc.Name != "" && tc.Namespace != "" {
		var found wekav1alpha1.WekaCluster
		if err := c.Get(ctx, client.ObjectKey{Name: tc.Name, Namespace: tc.Namespace}, &found); err == nil {
			targetCluster = &found
		}
	}

	secretName, _ := domain.GetWekaHomeClientCacertSecret(wc, targetCluster)
	if secretName == "" {
		return nil
	}

	fldPath := field.NewPath("spec", "wekaHome", "cacertSecret")
	problem, notFound, err := cacertSecretProblem(ctx, c, wc.Namespace, secretName)
	if err != nil {
		return field.ErrorList{field.InternalError(fldPath, err)}
	}
	if problem == "" {
		return nil
	}

	note := clientCacertSourceNote(wc, targetCluster)
	var consequence string
	if notFound {
		// AdditionalSecrets become a non-optional SecretVolumeSource (pod.go), so this is a FailedMount, not a fallback.
		consequence = fmt.Sprintf(
			"It is mounted as a non-optional volume, so the client pod will not start until the Secret exists.%s",
			note)
	} else {
		consequence = fmt.Sprintf(
			"Nothing is staged at %s, so this client silently falls back to its container's OS trust "+
				"store: it keeps working against a public Weka Home endpoint and fails against one "+
				"signed by a private CA.%s",
			domain.WekaHomeCacertPath, note)
	}
	return field.ErrorList{
		field.Invalid(fldPath, secretName, fmt.Sprintf("%s. %s", problem, consequence)),
	}
}
