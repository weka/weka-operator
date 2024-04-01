package controllers

import (
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/util"
)

func GetOperatorSecretName(cluster *wekav1alpha1.WekaCluster) string {
	return string("weka-operator-" + cluster.GetUID())
}

func GetLastGuidPart(cluster *wekav1alpha1.WekaCluster) string {
	return util.GetLastGuidPart(cluster.GetUID())
}

func GetUserClusterUsername(cluster *wekav1alpha1.WekaCluster) string {
	return "weka" + GetLastGuidPart(cluster)
}

func GetOperatorClusterUsername(cluster *wekav1alpha1.WekaCluster) string {
	return "weka-operator-" + GetLastGuidPart(cluster)
}
