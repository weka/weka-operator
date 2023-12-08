package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
)

// WekaFSIOModule returns the module definition for the `wekafsio` module
func WekaFSIOModule(imagePullSecretName string) v1beta1.Module {
	moduleName := "wekafsio"
	moduleLoadingOrder := []string{
		"wekafsio",
		"wekafsgw",
	}
	return WekaFSModule(moduleName, moduleLoadingOrder, imagePullSecretName)
}
