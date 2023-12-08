package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
)

// WekaFSIOModule returns the module definition for the `wekafsio` module
func WekaFSGWModule(imagePullSecretName string) v1beta1.Module {
	moduleName := "wekafsgw"
	moduleLoadingOrder := []string{}
	return WekaFSModule(moduleName, moduleLoadingOrder, imagePullSecretName)
}
