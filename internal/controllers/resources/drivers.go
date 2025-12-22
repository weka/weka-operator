package resources

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/services/discovery"
)

const (
	libmodulesMountName = "libmodules"
)

func (f *PodFactory) setDriverContainerDependencies(pod *corev1.Pod) {
	if f.container.Spec.Instructions != nil && f.container.Spec.Instructions.Type ==
		weka.InstructionCopyWekaFilesToDriverLoader {
		f.copyWekaVersionToDriverLoader(pod)
	}
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
		setLibModules(pod, f.nodeInfo)
	}
}

// setLibModules sets up overlayfs for /lib/modules to allow driver loading
// without modifying the host directory. This is necessary for driver containers
// that need to compile and load kernel modules.
func setLibModules(pod *corev1.Pod, nodeInfo *discovery.DiscoveryNodeInfo) {
	libModulesPath := "/lib/modules"
	usrSrcPath := "/usr/src"
	if nodeInfo.IsRhCos() {
		libModulesPath = "/hostpath/lib/modules"
		usrSrcPath = "/hostpath/usr/src"
	}

	// For drivers-loader: set up overlayfs to allow writes to /lib/modules
	// without modifying the host directory

	// Add host /lib/modules as read-only lower layer
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "libmodules-lower",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/lib/modules",
			},
		},
	})

	// Add single emptyDir for overlayfs workspace (contains upper, work, and merged subdirectories)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: libmodulesMountName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
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

	// Add init container to set up overlayfs
	mountPropagation := corev1.MountPropagationBidirectional
	privileged := true
	overlayfsInitContainer := corev1.Container{
		Name:    "overlayfs-setup",
		Image:   "busybox:latest",
		Command: []string{"/bin/sh", "-c"},
		Args: []string{`
			set -ex
			echo "Setting up overlayfs for /lib/modules..."

			# Create subdirectories in the workspace for overlay components
			mkdir -p /overlay-workspace/upper
			mkdir -p /overlay-workspace/work
			mkdir -p /overlay-workspace/merged

			# Verify the lower dir exists and is readable
			echo "Lower dir contents:"
			ls -la /libmodules-lower/ | head -10

			# Mount overlayfs
			mount -t overlay overlay \
				-o lowerdir=/libmodules-lower,upperdir=/overlay-workspace/upper,workdir=/overlay-workspace/work \
				/overlay-workspace/merged

			echo "Overlayfs mounted successfully"
			echo "Merged dir contents:"
			ls -la /overlay-workspace/merged/ | head -10
		`},
		SecurityContext: &corev1.SecurityContext{
			Privileged: &privileged,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:             "libmodules-lower",
				MountPath:        "/libmodules-lower",
				ReadOnly:         true,
				MountPropagation: &mountPropagation,
			},
			{
				Name:             libmodulesMountName,
				MountPath:        "/overlay-workspace",
				MountPropagation: &mountPropagation,
			},
		},
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, overlayfsInitContainer)

	// Mount the full overlayfs workspace so we can unmount it in preStop
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:             libmodulesMountName,
		MountPath:        "/overlay-workspace",
		MountPropagation: &mountPropagation,
	})
	// Mount the merged overlayfs directory at the expected location
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:             libmodulesMountName,
		MountPath:        libModulesPath,
		SubPath:          "merged",
		MountPropagation: &mountPropagation,
	})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      "usrsrc",
		MountPath: usrSrcPath,
	})

	// Add preStop hook to unmount overlayfs before container exits
	// This prevents the pod from getting stuck in Terminating state
	// We must unmount the actual overlayfs mount point, not the SubPath bind mount
	pod.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{
		PreStop: &corev1.LifecycleHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", "umount /overlay-workspace/merged || true"},
			},
		},
	}
}
