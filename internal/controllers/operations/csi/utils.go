package csi

import (
	"fmt"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"regexp"
	"strings"
)

func GenerateStorageClassName(csiDriverName string, fileSystemName string, mountOptions ...string) string {
	base := "storageclass-" + strings.ReplaceAll(csiDriverName, ".", "-") + "-" + fileSystemName
	if len(mountOptions) > 0 {
		base += "-" + strings.Join(mountOptions, "-")
	}
	return base
}

func GetCsiDriverNameFromTargetCluster(wekaCluster *weka.WekaCluster) string {
	if wekaCluster.Spec.CsiConfig.CsiDriverName != "" {
		return wekaCluster.Spec.CsiConfig.CsiDriverName
	}

	return generateCsiDriverName(wekaCluster.Name + "-" + wekaCluster.Namespace)
}

func GetCsiDriverNameFromClient(wekaClient *weka.WekaClient) string {
	if wekaClient.Spec.CsiConfig.CsiGroup == "" {
		return config.Consts.CsiLegacyDriverName
	}
	return generateCsiDriverName(wekaClient.Spec.CsiConfig.CsiGroup)
}

func generateCsiDriverName(baseName string) string {
	re := regexp.MustCompile("[^a-zA-Z0-9-]")
	cleanName := re.ReplaceAllString(baseName, "-")
	cleanName = strings.ToLower(cleanName)

	parts := strings.SplitN(config.Consts.CsiLegacyDriverName, ".", 2)
	if len(parts) < 2 {
		return fmt.Sprintf("csi.%s.weka.io", cleanName)
	}
	return fmt.Sprintf("%s.%s.%s", parts[0], cleanName, parts[1])
}

func GetBaseNameFromDriverName(csiDriverName string) string {
	parts := strings.Split(csiDriverName, ".")
	if len(parts) < 3 {
		return "csi"
	}
	return parts[1]
}

func GetCsiSecretName(wekaCluster *weka.WekaCluster) string {
	prefix := "weka-csi-"
	if config.Config.CsiInstallationEnabled {
		if wekaCluster.Spec.CsiConfig.CsiDriverName != "" {
			return prefix + GetBaseNameFromDriverName(wekaCluster.Spec.CsiConfig.CsiDriverName)
		}
		return prefix + wekaCluster.Name + "-" + wekaCluster.Namespace
	}
	return prefix + wekaCluster.Name
}

func GetTracingFlag() string {
	// TODO: csi 2.7.2 appends :443 port making url invalid
	//if config.Config.Otel.ExporterOtlpEndpoint != "" {
	//	return "--tracingurl=" + config.Config.Otel.ExporterOtlpEndpoint
	//}
	return ""
}
