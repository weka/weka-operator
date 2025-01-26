package factory

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/pkg/util"
	"k8s.io/apimachinery/pkg/types"
)

func RequiredWekaContainerLabels(clusterUID types.UID, role string) map[string]string {
	return util.MergeMaps(RequiredAnyWekaContainerLabels(role), map[string]string{
		domain.WekaLabelClusterId: string(clusterUID),
	})
}

func RequiredWekaClientLabels(clientName string) map[string]string {
	return util.MergeMaps(RequiredAnyWekaContainerLabels(v1alpha1.WekaContainerModeClient), map[string]string{
		domain.WekaLabelClientName: clientName,
	})
}

func RequiredAnyWekaContainerLabels(role string) map[string]string {
	return map[string]string{
		"app":                "weka",
		domain.WekaLabelMode: role,
	}
}
