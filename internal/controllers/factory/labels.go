package factory

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
	"k8s.io/apimachinery/pkg/types"
)

func RequiredWekaContainerLabels(clusterUID types.UID, clusterName string, role string) map[string]string {
	return util.MergeMaps(RequiredAnyWekaContainerLabels(role), map[string]string{
		domain.WekaLabelClusterId:   string(clusterUID),
		domain.WekaLabelClusterName: clusterName,
	})
}

func BuildClientContainerLabels(client *weka.WekaClient) map[string]string {
	labels := util.MergeMaps(RequiredAnyWekaContainerLabels(weka.WekaContainerModeClient), map[string]string{
		domain.WekaLabelClientName: client.ObjectMeta.Name,
	})
	if client.Spec.TargetCluster.Name != "" {
		labels[domain.WekaLabelTargetClusterName] = client.Spec.TargetCluster.Name
	}

	return util.MergeMaps(labels, client.ObjectMeta.GetLabels())
}

func RequiredAnyWekaContainerLabels(role string) map[string]string {
	return map[string]string{
		"app":                "weka",
		domain.WekaLabelMode: role,
	}
}
