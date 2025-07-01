package csi

import (
	"fmt"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"strings"
)

func GenerateStorageClassName(csiGroup string, fileSystemName string, mountOptions ...string) string {
	base := "storageclass-" + strings.ReplaceAll(csiGroup, ".", "-") + "-" + fileSystemName
	if len(mountOptions) > 0 {
		base += "-" + strings.Join(mountOptions, "-")
	}
	return base
}

func GetGroupFromTargetCluster(wekaCluster *weka.WekaCluster) string {
	if wekaCluster.Spec.CsiConfig.CsiGroup != "" {
		return wekaCluster.Spec.CsiConfig.CsiGroup
	}
	return fmt.Sprintf("%s.%s", wekaCluster.Name, wekaCluster.Namespace)
}

func GetGroupFromClient(wekaClient *weka.WekaClient) string {
	if wekaClient.Spec.CsiConfig == nil || wekaClient.Spec.CsiConfig.CsiGroup == "" {
		return "csi"
	}
	return wekaClient.Spec.CsiConfig.CsiGroup
}

func GetTracingFlag() string {
	if config.Config.Otel.ExporterOtlpEndpoint != "" {
		endpoint := strings.TrimPrefix(config.Config.Otel.ExporterOtlpEndpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")
		return "--tracingurl=" + endpoint
	}
	return ""
}
