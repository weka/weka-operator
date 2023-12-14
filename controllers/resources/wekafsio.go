package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	clientv1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// WekaFSIOModule returns the module definition for the `wekafsio` module
func (b *Builder) WekaFSIOModule(client *clientv1alpha1.Client, key types.NamespacedName, options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	options.ModuleName = "wekafsio"
	options.ModuleLoadingOrder = []string{
		"wekafsio",
		"wekafsgw",
	}
	return b.WekaFSModule(client, key, options)
}
