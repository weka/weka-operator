package resources

// Definition for KMM Module type
import (
	"errors"

	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
func WekaFSModule(metadata *metav1.ObjectMeta, options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	err := validateModuleOptions(options)
	if err != nil {
		return nil, err
	}

	moduleName := options.ModuleName

	// ModuleLoadingOrder is optional
	moduleLoadingOrder := options.ModuleLoadingOrder

	containerImage := "quay.io/weka.io/${MOD_NAME}-driver:v4.2.6-${KERNEL_FULL_VERSION}-7"
	if options.ContainerImage != "" {
		containerImage = options.ContainerImage
	}

	regexp := "^.*$"
	if options.KernelRegexp != "" {
		regexp = options.KernelRegexp
	}

	dockerfileConfigMapName := "weka-kmod-dockerfile-ubuntu"
	if options.DockerfileConfigMapName != "" {
		dockerfileConfigMapName = options.DockerfileConfigMapName
	}

	imagePullSecretName := options.ImagePullSecretName
	wekaVersion := options.WekaVersion
	backendIP := options.BackendIP

	return &v1beta1.Module{
		ObjectMeta: *metadata,
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
										Value: wekaVersion,
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
				},
			},
			ImageRepoSecret: imageRepoSecret(imagePullSecretName),
			Selector: map[string]string{
				"weka.io/role": "client",
			},
		},
	}, nil
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
