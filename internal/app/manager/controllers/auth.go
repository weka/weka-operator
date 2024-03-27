package controllers

import (
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"strings"
)

func GetOperatorSecretName(cluster *wekav1alpha1.DummyCluster) string {
	return string("weka-operator-" + cluster.GetUID())
}

func GetUserSecretName(cluster *wekav1alpha1.DummyCluster) string {
	return "weka-cluster-" + cluster.Name
}

func GetLastGuidPart(cluster *wekav1alpha1.DummyCluster) string {
	guidLastPart := string(cluster.GetUID()[strings.LastIndex(string(cluster.GetUID()), "-")+1:])
	return guidLastPart
}

func GetUserClusterUsername(cluster *wekav1alpha1.DummyCluster) string {
	return "weka" + GetLastGuidPart(cluster)
}

func GetOperatorClusterUsername(cluster *wekav1alpha1.DummyCluster) string {
	return "weka-operator-" + GetLastGuidPart(cluster)
}
