package domain

import (
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"os"
)

func GetWekahomeConfig(cluster *v1alpha1.WekaCluster) (v1alpha1.WekaHomeConfig, error) {
	wekaHomeEndpoint := ""
	if cluster.Spec.WekaHomeConfig != nil {
		wekaHomeEndpoint = cluster.Spec.WekaHomeConfig.Endpoint
	}

	if wekaHomeEndpoint == "" {
		//get from env var instead
		var isSet bool
		wekaHomeEndpoint, isSet = os.LookupEnv("WEKA_OPERATOR_WEKA_HOME_ENDPOINT")
		if !isSet {
			wekaHomeEndpoint = "https://api.home.weka.io"
		}
	}

	config := v1alpha1.WekaHomeConfig{
		Endpoint:      wekaHomeEndpoint,
		AllowInsecure: false,
		CacertSecret:  "",
	}

	if cluster.Spec.WekaHomeConfig != nil {
		config.AllowInsecure = cluster.Spec.WekaHomeConfig.AllowInsecure
		config.CacertSecret = cluster.Spec.WekaHomeConfig.CacertSecret
	}

	if !config.AllowInsecure {
		wekaHomeInsecure, isSet := os.LookupEnv("WEKA_OPERATOR_WEKA_HOME_INSECURE")
		if isSet {
			if wekaHomeInsecure != "" {
				if wekaHomeInsecure == "true" {
					config.AllowInsecure = true
				}
			}
		}
	}

	if config.CacertSecret == "" {
		wekaHomeCacertSecret, isSet := os.LookupEnv("WEKA_OPERATOR_WEKA_HOME_CACERT_SECRET")
		if isSet {
			if wekaHomeCacertSecret != "" {
				config.CacertSecret = wekaHomeCacertSecret
			}
		}
	}

	return config, nil
}

func GetWekaHomeSecretRef(config v1alpha1.WekaHomeConfig) *string {
	if config.CacertSecret != "" {
		return &config.CacertSecret
	}

	return nil
}
