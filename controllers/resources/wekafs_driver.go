package resources

// Definition for KMM Module type
import (
	"errors"

	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	v1 "k8s.io/api/core/v1"
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
func WekaFSModule(options *WekaFSModuleOptions) (*v1beta1.Module, error) {
	if options == nil {
		return nil, errors.New("options cannot be nil")
	}

	if options.ModuleName == "" {
		return nil, errors.New("options.ModuleName cannot be empty")
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

	if options.ImagePullSecretName == "" {
		return nil, errors.New("options.ImagePullSecretName cannot be empty")
	}
	imagePullSecretName := options.ImagePullSecretName

	if options.WekaVersion == "" {
		return nil, errors.New("options.WekaVersion cannot be empty")
	}
	wekaVersion := options.WekaVersion

	if options.BackendIP == "" {
		return nil, errors.New("options.BackendIP cannot be empty")
	}
	backendIP := options.BackendIP

	return &v1beta1.Module{
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

func imageRepoSecret(secretName string) *v1.LocalObjectReference {
	return &v1.LocalObjectReference{Name: secretName}
}
