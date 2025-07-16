package csi

import (
	"fmt"
	"github.com/weka/weka-k8s-api/util"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
)

type CSIRole string

const (
	CSIController   CSIRole = "csi_controller"
	CSINode         CSIRole = "csi_node"
	CSIDriver       CSIRole = "csi_driver"
	CSIStorageclass CSIRole = "csi_storageclass"
)

func GenerateStorageClassName(csiGroup string, fileSystemName string, mountOptions ...string) string {
	base := "weka-" + strings.ReplaceAll(csiGroup, ".", "-") + "-" + fileSystemName
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

func GetAllowInsecureHttpsFlag(enforceSecureHttps bool) string {
	if !enforceSecureHttps {
		return "--allowinsecurehttps"
	}
	return ""
}

func GetSkipGarbageCollectionFlag(skipGarbageCollection bool) string {
	if skipGarbageCollection {
		return "--skipgarbagecollection"
	}
	return ""
}

func GetCsiLabels(csiDriverName string, role CSIRole, parentLabels, csiLabels map[string]string) map[string]string {
	labels := map[string]string{
		"weka.io/csi-driver-name": csiDriverName,
		"weka.io/mode":            string(role),
	}

	if parentLabels != nil {
		for k, v := range parentLabels {
			if _, exists := labels[k]; !exists {
				labels[k] = v
			}
		}
	}

	if csiLabels != nil {
		for k, v := range csiLabels {
			labels[k] = v
		}
	}

	return labels
}

type updatableCSISpec struct {
	csiDriverName string
	labels        map[string]string
	tolerations   []v1.Toleration
}

func GetCSISpecHash(csiDriverName string, wekaClient *weka.WekaClient, role CSIRole) (string, error) {
	labels := make(map[string]string)
	tolerations := util.ExpandTolerations([]v1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations)
	switch role {
	case CSIController:
		if wekaClient.Spec.CsiConfig.Advanced != nil {
			labels = GetCsiLabels(csiDriverName, CSIController, wekaClient.Labels, wekaClient.Spec.CsiConfig.Advanced.ControllerLabels)
			tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.ControllerTolerations...)
		}
	case CSINode:
		if wekaClient.Spec.CsiConfig.Advanced != nil {
			labels = GetCsiLabels(csiDriverName, CSINode, wekaClient.Labels, wekaClient.Spec.CsiConfig.Advanced.NodeLabels)
			tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.NodeTolerations...)
		}
	default:
		return "", fmt.Errorf("unsupported CSI role: %s", role)
	}

	return util2.HashStruct(updatableCSISpec{
		csiDriverName: csiDriverName,
		labels:        labels,
		tolerations:   tolerations,
	})
}
