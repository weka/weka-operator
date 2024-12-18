package domain

import "github.com/weka/weka-k8s-api/api/v1alpha1"

var ContainerModesWithFrontend = []string{
	v1alpha1.WekaContainerModeNfs,
	v1alpha1.WekaContainerModeS3,
	v1alpha1.WekaContainerModeClient,
}
