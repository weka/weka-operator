package resources

// Definition for KMM Module type
import (
	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	v1 "k8s.io/api/core/v1"
)

// WekaFSModule is a template for the two wekafs drivers
func WekaFSModule(moduleName string, moduleLoadingOrder []string, imagePullSecretName string) v1beta1.Module {
	containerImage := "quay.io/weka.io/${MOD_NAME}-driver:v4.2.6-${KERNEL_FULL_VERSION}-7"
	regexp := "^.*$"
	dockerfileConfigMapName := "weka-kmod-dockerfile-ubuntu"
	wekaVersion := "4.2.6.3212-61e9145d99a867bf6aab053cd75ea77f"
	backendIP := "10.108.158.139"

	return v1beta1.Module{
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
	}
}

func imageRepoSecret(secretName string) *v1.LocalObjectReference {
	return &v1.LocalObjectReference{Name: secretName}
}
