package validation

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// cacertSecretProblem reports why the named Secret cannot serve as a WekaHome CA bundle, or ""
// when it can. notFound separates "does not exist yet" (routinely transient) from "exists but its
// content is unusable" (permanent until someone fixes it); internalErr separates both from a lookup
// failure.
//
// The usability rule mirrors weka_runtime.py, which stages a Secret data key only when it holds a
// certificate and no private key: a kubernetes.io/tls Secret is an easy thing to point at, and its
// tls.key must never land in the CA bundle. x509 parsing alone is looser - it skips non-CERTIFICATE
// PEM blocks and reports success on the rest - so a combined cert+key file would pass here while
// the runtime stages nothing.
func cacertSecretProblem(ctx context.Context, c client.Client, namespace, secretName string) (problem string, notFound bool, internalErr error) {
	var secret corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, &secret)
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("Secret %q does not exist in namespace %q", secretName, namespace), true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("looking up wekahome cacert secret %s/%s: %w", namespace, secretName, err)
	}

	pool := x509.NewCertPool()
	for _, pemBytes := range secret.Data {
		if bytes.Contains(pemBytes, []byte("PRIVATE KEY")) {
			continue
		}
		if pool.AppendCertsFromPEM(pemBytes) {
			return "", false, nil
		}
	}
	return fmt.Sprintf("Secret %q holds no usable CA certificate: no data key contains a PEM "+
		"certificate without a private key alongside it", secretName), false, nil
}

// clusterCacertSecretName resolves the WekaHome cacert Secret a WekaCluster's cacert validators
// should check, or "" to skip: Weka Home disabled (EnsureWekaHomeCacertOverride never writes an
// override, so a Secret's usability is moot), insecure TLS on (weka verifies no server certificate,
// so the CA is never consulted), or the config lookup itself failed (fail open rather than block
// an apply on an unrelated error).
func clusterCacertSecretName(cluster *wekav1alpha1.WekaCluster) string {
	whConfig, err := domain.GetWekahomeConfig(cluster)
	if err != nil || whConfig.Endpoint == "" || whConfig.AllowInsecure {
		return ""
	}
	return whConfig.CacertSecret
}

// cacertSourceNote points the user at the operator-wide Helm value when the resolved secret name
// did not come from the field the error otherwise names: clearing spec.wekaHome.cacertSecret would
// just re-resolve to the same default, so the only way to actually change it is wekahome.cacertSecret.
func cacertSourceNote(cluster *wekav1alpha1.WekaCluster) string {
	if cluster.Spec.WekaHome == nil || cluster.Spec.WekaHome.CacertSecret == "" {
		return " This name comes from the operator-wide Helm value wekahome.cacertSecret, not from spec.wekaHome.cacertSecret."
	}
	return ""
}

// clientCacertSourceNote is cacertSourceNote for a WekaClient: it also names the target WekaCluster
// when the resolved secret name was inherited from there rather than the Helm default (see
// domain.GetWekaHomeClientCacertSecret's precedence).
func clientCacertSourceNote(c *wekav1alpha1.WekaClient, targetCluster *wekav1alpha1.WekaCluster) string {
	if c.Spec.WekaHome != nil && c.Spec.WekaHome.CacertSecret != "" {
		return ""
	}
	if targetCluster != nil && targetCluster.Namespace == c.Namespace &&
		targetCluster.Spec.WekaHome != nil && targetCluster.Spec.WekaHome.CacertSecret != "" {
		return " This name is inherited from the target WekaCluster's spec.wekaHome.cacertSecret, not from this client's spec."
	}
	return " This name comes from the operator-wide Helm value wekahome.cacertSecret, not from spec.wekaHome.cacertSecret."
}
