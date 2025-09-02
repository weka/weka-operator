package resources

import v1 "k8s.io/api/core/v1"

func addUIOLoaderInitContainer(pod *v1.Pod) *v1.Pod {
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
	hostPathType := v1.HostPathUnset

	uioInitContainer := v1.Container{
		Name:    "uio-loader-init",
		Image:   "busybox:latest",
		Command: command,
		SecurityContext: &v1.SecurityContext{
			Privileged: &privileged,
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "host-modules",
				MountPath: "/lib/modules",
				ReadOnly:  true,
			},
		},
	}

	if pod.Spec.InitContainers == nil {
		pod.Spec.InitContainers = []v1.Container{}
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
		hostModulesVolume := v1.Volume{
			Name: "host-modules",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
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
