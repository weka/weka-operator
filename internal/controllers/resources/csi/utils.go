package csi

import (
	"fmt"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"regexp"
	"strings"
)

func GenerateCsiDriverName(baseName string) string {
	re := regexp.MustCompile("[^a-zA-Z0-9-]")
	cleanName := re.ReplaceAllString(baseName, "-")
	cleanName = strings.ToLower(cleanName)
	return fmt.Sprintf("csi.%s.weka.io", cleanName)
}

func GenerateStorageClassName(csiDriverName string, fileSystemName string, mountOptions ...string) string {
	base := "storageclass-" + strings.ReplaceAll(csiDriverName, ".", "-") + "-" + fileSystemName
	if len(mountOptions) > 0 {
		base += "-" + strings.Join(mountOptions, "-")
	}
	return base
}

func GetCsiBaseName(wekaClient *weka.WekaClient) string {
	baseName := wekaClient.Name
	emptyRef := weka.ObjectReference{}
	if wekaClient.Spec.TargetCluster != emptyRef {
		baseName = wekaClient.Spec.TargetCluster.Name + "-" + wekaClient.Spec.TargetCluster.Namespace
	} else if wekaClient.Spec.CSIGroup != "" {
		baseName = wekaClient.Spec.CSIGroup
	}
	return baseName
}
