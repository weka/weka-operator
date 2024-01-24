package resources

// Definition for KMM Module type
import (
	"errors"
	"fmt"
	"strings"

	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	clientv1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type WekaFSModuleOptions struct {
	ModuleName              string
	ModuleLoadingOrder      []string
	ImagePullSecretName     string
	ContainerImage          string
	KernelRegexp            string
	DockerfileConfigMapName string
	WekaVersion             string
	BackendIP               string
}

// WekaFSModule is a template for the two wekafs drivers
func (b *Builder) WekaFSModule(client *clientv1alpha1.Client, key types.NamespacedName, options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	err := validateModuleOptions(options)
	if err != nil {
		return nil, err
	}

	moduleName := options.ModuleName

	// ModuleLoadingOrder is optional
	moduleLoadingOrder := options.ModuleLoadingOrder

	containerImage := fmt.Sprintf("weka-image-registry.weka-operator-system:5000/weka-drivers-${MOD_NAME}:${KERNEL_FULL_VERSION}-%s", options.WekaVersion)
	if options.ContainerImage != "" {
		containerImage = options.ContainerImage
	}

	regexp := "^.*$"
	if options.KernelRegexp != "" {
		regexp = options.KernelRegexp
	}

	dockerfileConfigMapName := "weka-kmod-downloader"
	if options.DockerfileConfigMapName != "" {
		dockerfileConfigMapName = options.DockerfileConfigMapName
	}

	imagePullSecretName := options.ImagePullSecretName
	// wekaVersion := options.WekaVersion
	backendIP := options.BackendIP

	module := &v1beta1.Module{
		ObjectMeta: metav1.ObjectMeta{
			Name:        toObjectName(moduleName),
			Namespace:   key.Namespace,
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
		Spec: v1beta1.ModuleSpec{
			ModuleLoader: v1beta1.ModuleLoaderSpec{
				Container: v1beta1.ModuleLoaderContainerSpec{
					Modprobe: v1beta1.ModprobeSpec{
						ModuleName:          moduleName,
						ModulesLoadingOrder: moduleLoadingOrder,
					},
					KernelMappings: []v1beta1.KernelMapping{
						{
							Regexp:         regexp,
							ContainerImage: containerImage,
							Build: &v1beta1.Build{
								BuildArgs: []v1beta1.BuildArg{
									{
										Name:  "WEKA_VERSION",
										Value: options.WekaVersion,
									},
									{
										Name:  "BACKEND_IP",
										Value: backendIP,
									},
								},
								DockerfileConfigMap: &v1.LocalObjectReference{
									Name: dockerfileConfigMapName,
								},
							},
						},
					},
					RegistryTLS: v1beta1.TLSOptions{
						Insecure:              true,
						InsecureSkipTLSVerify: true,
					},
				},
			},
			ImageRepoSecret: imageRepoSecret(imagePullSecretName),
			Selector: map[string]string{
				"weka.io/role": "client",
			},
		},
	}

	if err := controllerutil.SetControllerReference(client, module, b.scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	return module, nil
}

func validateModuleOptions(options *WekaFSModuleOptions) error {
	if options == nil {
		return errors.New("options cannot be nil")
	}

	if options.ModuleName == "" {
		return errors.New("options.ModuleName cannot be empty")
	}

	if options.ImagePullSecretName == "" {
		return errors.New("options.ImagePullSecretName cannot be empty")
	}

	if options.WekaVersion == "" {
		return errors.New("options.WekaVersion cannot be empty")
	}

	if options.BackendIP == "" {
		return errors.New("options.BackendIP cannot be empty")
	}

	return nil
}

func imageRepoSecret(secretName string) *v1.LocalObjectReference {
	return &v1.LocalObjectReference{Name: secretName}
}

// toObjectName converts a module name to a valid object name
// converts _ to -
func toObjectName(name string) string {
	return strings.ReplaceAll(name, "_", "-")
}
