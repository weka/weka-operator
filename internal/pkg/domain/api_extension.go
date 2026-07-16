package domain

import "github.com/weka/weka-k8s-api/api/v1alpha1"

var ContainerModesWithFrontend = []string{
	v1alpha1.WekaContainerModeNfs,
	v1alpha1.WekaContainerModeS3,
	v1alpha1.WekaContainerModeClient,
	v1alpha1.WekaContainerModeSmbw,
}

// ContainerModesBackend lists the WekaContainer modes that run backend (server-side) processes on a
// backend node. Kept in sync with WekaContainer.IsBackend() (weka-k8s-api container_types.go).
var ContainerModesBackend = []string{
	v1alpha1.WekaContainerModeDrive,
	v1alpha1.WekaContainerModeCompute,
	v1alpha1.WekaContainerModeS3,
	v1alpha1.WekaContainerModeNfs,
	v1alpha1.WekaContainerModeSmbw,
	v1alpha1.WekaContainerModeDataServices,
}
