package domain

import weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"

// NeedsOperatorResources is the shared producer/consumer contract for the
// operator-written runtime resources.json. Clients receive identity and NIC
// overrides without participating in backend allocation.
func NeedsOperatorResources(mode string) bool {
	container := weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: mode}}
	return container.IsAllocatable() || container.IsClientContainer()
}

// ShutdownInstructions is the on-disk operator/runtime shutdown contract.
type ShutdownInstructions struct {
	AllowStop      bool `json:"allow_stop"`
	AllowForceStop bool `json:"allow_force_stop"`
}
