package factory

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"k8s.io/apimachinery/pkg/types"
)

func RequiredWekaContainerLabels(clusterUID types.UID, role string) map[string]string {
	return map[string]string{
		"app":                     "weka",
		domain.WekaLabelClusterId: string(clusterUID),
		domain.WekaLabelMode:      role, // in addition to spec for indexing on k8s side for filtering by mode
	}
}

func RequiredWekaClientLabels(clientName string) map[string]string {
	return map[string]string{
		"app":                      "weka-client",
		domain.WekaLabelClientName: clientName,
		domain.WekaLabelMode:       v1alpha1.WekaContainerModeClient,
	}
}
