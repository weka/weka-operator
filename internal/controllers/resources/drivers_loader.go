package resources

import (
	corev1 "k8s.io/api/core/v1"
)

const OperationCopyVersionToDriverLoader = "copy-version-to-driver-loader"

// copy weka dist files if drivers-loader image is
// different from cluster image
func (f *PodFactory) copyWekaVersionToDriverLoader(pod *corev1.Pod) {
	if f.container.Spec.Instructions.Type != OperationCopyVersionToDriverLoader {
		return
	}
	originalImage := f.container.Spec.Instructions.Payload
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "CLUSTER_IMAGE_NAME",
		Value: originalImage,
	})

	sharedVolumeName := "shared-weka-version-data"
	sharedVolumeMountPath := "/shared-weka-version-data"

	pod.Spec.InitContainers = []corev1.Container{
		{
			Name:    "init-setup",
			Image:   originalImage,
			Command: []string{"sh", "-c"},
			Args: []string{
				`
						mkdir -p /shared-weka-version-data/dist &&
						cp -r /opt/weka/dist/* /shared-weka-version-data/dist/ && 
						echo "Init container completed successfully"
						`,
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      sharedVolumeName,
					MountPath: sharedVolumeMountPath,
				},
			},
		},
	}

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

func (f *PodFactory) addUIOLoaderInitContainer(pod *corev1.Pod) *corev1.Pod {
	if pod == nil {
		return nil
	}

	script := `#!/bin/sh
set -e

echo "Loading UIO kernel module..."
modprobe uio
lsmod | grep uio
echo "UIO module loaded successfully"
`

	command := []string{"/bin/sh", "-c", script}
	privileged := true
	hostPathType := corev1.HostPathUnset

	uioInitContainer := corev1.Container{
		Name:    "uio-loader-init",
		Image:   "busybox:latest",
		Command: command,
		SecurityContext: &corev1.SecurityContext{
			Privileged: &privileged,
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "host-modules",
				MountPath: "/lib/modules",
				ReadOnly:  true,
			},
		},
	}

	if pod.Spec.InitContainers == nil {
		pod.Spec.InitContainers = []corev1.Container{}
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, uioInitContainer)

	// Add the required volume if it doesn't already exist
	volumeExists := false
	for _, vol := range pod.Spec.Volumes {
		if vol.Name == "host-modules" {
			volumeExists = true
			break
		}
	}

	if !volumeExists {
		hostModulesVolume := corev1.Volume{
			Name: "host-modules",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/lib/modules",
					Type: &hostPathType,
				},
			},
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, hostModulesVolume)
	}

	pod.Spec.HostPID = true
	pod.Spec.HostNetwork = true

	return pod
}
