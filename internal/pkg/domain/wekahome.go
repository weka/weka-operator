package domain

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	env "github.com/weka/weka-operator/internal/config"
)

func GetWekahomeConfig(cluster *v1alpha1.WekaCluster) (v1alpha1.WekaHomeConfig, error) {
	wekaHomeEndpoint := ""
	if cluster.Spec.WekaHome != nil {
		wekaHomeEndpoint = cluster.Spec.WekaHome.Endpoint
	}

	if wekaHomeEndpoint == "" {
		wekaHomeEndpoint = env.Config.WekaHome.Endpoint
	}

	config := v1alpha1.WekaHomeConfig{
		Endpoint:      wekaHomeEndpoint,
		AllowInsecure: false,
		CacertSecret:  "",
	}

	if cluster.Spec.WekaHome != nil {
		config.AllowInsecure = cluster.Spec.WekaHome.AllowInsecure
		config.CacertSecret = cluster.Spec.WekaHome.CacertSecret
	}

	if !config.AllowInsecure {
		config.AllowInsecure = env.Config.WekaHome.AllowInsecure
	}

	config.CacertSecret = GetWekaHomeClusterCacertSecret(cluster)

	if config.EnableStats == nil {
		val := env.Config.WekaHome.EnableStats
		config.EnableStats = &val
	}

	return config, nil
}

// WekaHomeCacertPath is where weka_runtime.py stages the mounted CA secret inside every pod and
// where the cluster-wide weka_cloud_ca_cert_path override points.
const WekaHomeCacertPath = "/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem"

// GetWekaHomeClusterCacertSecret resolves the cacert secret backend containers mount: the cluster's
// own value, else the operator-wide default.
func GetWekaHomeClusterCacertSecret(cluster *v1alpha1.WekaCluster) string {
	if cluster.Spec.WekaHome != nil && cluster.Spec.WekaHome.CacertSecret != "" {
		return cluster.Spec.WekaHome.CacertSecret
	}
	return env.Config.WekaHome.CacertSecret
}

// WekaHomeAdditionalSecrets returns a copy of existing with the wekahome-cacert entry set to the
// resolved secret, or removed when it is empty. Other entries are kept: the pod factory mounts
// every key, so replacing the map would silently drop them.
func WekaHomeAdditionalSecrets(existing map[string]string, cacertSecret string) map[string]string {
	secrets := make(map[string]string, len(existing)+1)
	for k, v := range existing {
		secrets[k] = v
	}
	if cacertSecret != "" {
		secrets["wekahome-cacert"] = cacertSecret
	} else {
		delete(secrets, "wekahome-cacert")
	}
	return secrets
}

// GetWekaHomeClientCacertSecret resolves the WekaHome CA cert secret a WekaClient should mount.
// Precedence: the client's own value, then the target cluster's (only when the cluster shares the
// client's namespace - the secret is mounted by name, so a cross-namespace name would not resolve),
// then the operator-wide default. The cluster fallback exists because only the cluster-wide
// weka_cloud_ca_cert_path replicates to joining machines, never the certificate file, so a client of
// a private-CA cluster must place the same PEM itself.
//
// crossNamespaceSkipped reports that the cluster has its own cacertSecret but it was not inherited
// because the cluster lives in a different namespace than the client.
func GetWekaHomeClientCacertSecret(c *v1alpha1.WekaClient, targetCluster *v1alpha1.WekaCluster) (secret string, crossNamespaceSkipped bool) {
	clientSecret := ""
	if c.Spec.WekaHome != nil {
		clientSecret = c.Spec.WekaHome.CacertSecret
	}

	sameNamespace := targetCluster != nil && targetCluster.Namespace == c.Namespace

	clusterSecret := ""
	if sameNamespace && targetCluster.Spec.WekaHome != nil {
		clusterSecret = targetCluster.Spec.WekaHome.CacertSecret
	}

	if clientSecret != "" {
		return clientSecret, false
	}
	if clusterSecret != "" {
		return clusterSecret, false
	}

	if !sameNamespace && targetCluster != nil && targetCluster.Spec.WekaHome != nil && targetCluster.Spec.WekaHome.CacertSecret != "" {
		crossNamespaceSkipped = true
	}

	return env.Config.WekaHome.CacertSecret, crossNamespaceSkipped
}
