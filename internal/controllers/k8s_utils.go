package controllers

import (
	util2 "github.com/weka/weka-operator/pkg/util"
	ctrl "sigs.k8s.io/controller-runtime"
)

// GetKubernetesVersion returns the Kubernetes version as a string, defaulting to "unknown" on error
func GetKubernetesVersion(manager ctrl.Manager) string {
	if version, err := util2.GetKubernetesVersion(manager.GetConfig()); err == nil {
		return version
	}
	return "unknown"
}
