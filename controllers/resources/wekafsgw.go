package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
)

// WekaFSIOModule returns the module definition for the `wekafsio` module
func WekaFSGWModule(options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	options.ModuleName = "wekafsgw"
	options.ModuleLoadingOrder = []string{}
	return WekaFSModule(options)
}
