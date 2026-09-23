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

	if f.nodeInfo.IsCos() {
		// in COS we can't load it in the drivers-loader pod because of /lib/modules override
		addUIOLoaderInitContainer(pod)
		allowCosDisableDriverSigning := config.Config.GkeCompatibility.DisableDriverSigning
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				Name:      "weka-boot-scripts",
				MountPath: "/devenv.sh",
				SubPath:   "devenv.sh",
			},
			corev1.VolumeMount{
				Name:      "proc-sysrq-trigger",
				MountPath: "/hostside/proc/sysrq-trigger",
			},
			corev1.VolumeMount{
				Name:      "proc-cmdline",
				MountPath: "/hostside/proc/cmdline",
			},
		)
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
	} else if f.nodeInfo.IsNixos() {
		// NixOS has no FHS /lib/modules or /usr/src. The host's system profile carries the
		// kernel headers (environment.systemPackages = [ kernel.dev ]) at
		// sw/lib/modules/$(uname -r)/build, the same shape as a distro's /lib/modules, and the
		// in-tree modules at kernel-modules/lib/modules; both are symlink farms into /nix/store.
		// The builder image's runtime unions the host store under the image store and links
		// /lib/modules/$(uname -r) from these mounts (see nixos_prepare_host_kernel in
		// weka_runtime.py). Mounting /run/current-system at its host path would make NixOS's
		// modprobe ignore /lib/modules, hence the /host/ prefix.
		pod.Spec.Volumes = append(pod.Spec.Volumes,
			corev1.Volume{
				Name: "nix-store",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/nix/store",
						Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
					},
				},
			},
			corev1.Volume{
				Name: "nix-current-system",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/run/current-system",
					},
				},
			},
		)
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				Name:      "nix-store",
				MountPath: "/host/nix/store",
				ReadOnly:  true,
			},
			corev1.VolumeMount{
				Name:      "nix-current-system",
				MountPath: "/host/current-system",
				ReadOnly:  true,
			},
		)
	} else {
		libModulesPath := "/lib/modules"
		usrSrcPath := "/usr/src"

		// adding mount of headers only for case of drivers-related container
		pod.Spec.Volumes = append(pod.Spec.Volumes,
			corev1.Volume{
				Name: "libmodules",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/lib/modules",
					},
				},
			},
			corev1.Volume{
				Name: "usrsrc",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/usr/src",
					},
				},
			},
		)
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
