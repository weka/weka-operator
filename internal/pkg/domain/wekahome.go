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

	if config.CacertSecret == "" {
		config.CacertSecret = env.Config.WekaHome.CacertSecret
	}

	if config.EnableStats == nil {
		val := env.Config.WekaHome.EnableStats
		config.EnableStats = &val
	}

	return config, nil
}

func GetWekaHomeSecretRef(config v1alpha1.WekaHomeConfig) *string {
	if config.CacertSecret != "" {
		return &config.CacertSecret
	}

	return nil
}
