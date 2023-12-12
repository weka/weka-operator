package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WekaFSIOModule returns the module definition for the `wekafsio` module
func WekaFSIOModule(metadata *metav1.ObjectMeta, options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	options.ModuleName = "wekafsio"
	options.ModuleLoadingOrder = []string{
		"wekafsio",
		"wekafsgw",
	}
	return WekaFSModule(metadata, options)
}
