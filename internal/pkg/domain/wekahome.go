package domain

import (
	"os"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
)

func GetWekahomeConfig(cluster *v1alpha1.WekaCluster) (v1alpha1.WekaHomeConfig, error) {
	wekaHomeEndpoint := ""
	if cluster.Spec.WekaHome != nil {
		wekaHomeEndpoint = cluster.Spec.WekaHome.Endpoint
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

	if cluster.Spec.WekaHome != nil {
		config.AllowInsecure = cluster.Spec.WekaHome.AllowInsecure
		config.CacertSecret = cluster.Spec.WekaHome.CacertSecret
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

	if config.EnableStats == nil {
		wekahomeEnableStats, isSet := os.LookupEnv("WEKA_OPERATOR_WEKA_HOME_ENABLE_STATS")
		var val bool
		if isSet {
			if wekahomeEnableStats == "true" {
				val = true
			} else {
				val = false
			}
		} else {
			val = true
		}
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
