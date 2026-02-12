package resources

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
)

func (f *PodFactory) setDriverDependencies(pod *corev1.Pod) {
	// Copy weka files from cluster image when using a different image (builder image)
	// This applies to both drivers-builder and drivers-loader when Instructions is set
	if f.container.Spec.Instructions != nil &&
		f.container.Spec.Instructions.Type == weka.InstructionCopyWekaFilesToDriverLoader {
		f.copyWekaVersionToContainer(pod)
	}

	// Set the cluster image so the container knows which version to use
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "CLUSTER_IMAGE_NAME",
		Value: f.container.Spec.Image,
	})

	if f.nodeInfo.IsCos() {
		// in COS we can't load it in the drivers-loader pod because of /lib/modules override
		addUIOLoaderInitContainer(pod)
		allowCosDisableDriverSigning := config.Config.GkeCompatibility.DisableDriverSigning
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "weka-boot-scripts",
			MountPath: "/devenv.sh",
			SubPath:   "devenv.sh",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "proc-sysrq-trigger",
			MountPath: "/hostside/proc/sysrq-trigger",
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "proc-cmdline",
			MountPath: "/hostside/proc/cmdline",
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "proc-sysrq-trigger",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc/sysrq-trigger",
					Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
				},
			},
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "proc-cmdline",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/proc/cmdline",
					Type: &[]corev1.HostPathType{corev1.HostPathFile}[0],
				},
			},
		})
		if allowCosDisableDriverSigning {
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING",
				Value: "true",
			})
		}

		if f.container.IsDriversBuilder() {
			pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
				Name: "gcloud-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: config.Config.GkeCompatibility.ServiceAccountSecret,
					},
				},
			})
			pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "gcloud-credentials",
				MountPath: "/var/secrets/google",
			})
			pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
				Name:  "GOOGLE_APPLICATION_CREDENTIALS",
				Value: "/var/secrets/google/service-account.json",
			})
		}
	} else {
		libModulesPath := "/lib/modules"
		usrSrcPath := "/usr/src"

		// adding mount of headers only for case of drivers-related container
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "libmodules",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/lib/modules",
				},
			},
		})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: "usrsrc",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/usr/src",
				},
			},
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "libmodules",
			MountPath: libModulesPath,
			ReadOnly:  true,
		})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "usrsrc",
			MountPath: usrSrcPath,
		})
	}
}

// todo find a common place (will need to be used for builder later)
func CopyWekaCliToMainContainer(pod *corev1.Pod) {

	sharedVolumeName := "shared-weka-cli"
	sharedVolumeMountPath := "/shared-weka-cli"

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    "init-copy-cli",
		Image:   config.Config.DefaultCliContainer,
		Command: []string{"sh", "-c"},
		Args: []string{
			`
					cp /usr/bin/weka /shared-weka-cli/
					echo "Init container copy cli completed successfully"
					`,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      sharedVolumeName,
				MountPath: sharedVolumeMountPath,
			},
		},
	})

	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      sharedVolumeName,
		MountPath: sharedVolumeMountPath,
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{

		Name: sharedVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
}
