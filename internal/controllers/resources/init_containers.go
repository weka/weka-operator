package resources

import (
	"encoding/json"

	v1 "k8s.io/api/core/v1"
)

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

// copyWekaVersionToContainer adds init containers to copy weka files from the target image
// when the pod uses a different image (builder/loader). It parses the JSON instruction payload
// to get targetImage and cliImage.
func (f *PodFactory) copyWekaVersionToContainer(pod *v1.Pod) {
	var payload struct {
		TargetImage string `json:"targetImage"`
		CliImage    string `json:"cliImage"`
	}
	if err := json.Unmarshal([]byte(f.container.Spec.Instructions.Payload), &payload); err != nil {
		// Fallback for legacy single-image payload
		payload.TargetImage = f.container.Spec.Instructions.Payload
		payload.CliImage = f.container.Spec.Instructions.Payload
	}

	sharedVolumeName := "shared-weka-version"
	sharedVolumeMountPath := "/shared-weka-version"

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, v1.Container{
		Name:    "copy-cli",
		Image:   payload.CliImage,
		Command: []string{"sh", "-c"},
		Args: []string{
			`
					# Copy the actual binary file that command resolves to (follows symlinks)
					mkdir -p /shared-weka-version/cli &&
					cp -a -- "$(readlink -f -- "$(command -v weka)")" /shared-weka-version/cli/weka &&
					echo "copy-cli init container done"
					`,
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      sharedVolumeName,
				MountPath: sharedVolumeMountPath,
			},
		},
	})

	privileged := true
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, v1.Container{
		Name:    "copy-weka-version",
		Image:   payload.TargetImage,
		Command: []string{"sh", "-c"},
		Args: []string{
			`
			# Detect version from release spec file
			SPEC_FILE=$(ls /opt/weka/dist/release/*.spec 2>/dev/null | head -1)
			if [ -z "$SPEC_FILE" ]; then
				echo "ERROR: No .spec file found in /opt/weka/dist/release/" >&2
				exit 1
			fi
			VERSION=$(basename "$SPEC_FILE" .spec)
			echo "Detected weka version: $VERSION"
			mkdir -p /original-opt-weka &&
			mkdir -p /shared-weka-version/opt-weka &&
			mount -o bind /opt/weka /original-opt-weka &&
			mount -o bind /shared-weka-version/opt-weka /opt/weka &&
			/shared-weka-version/cli/weka version get "$VERSION" --without-agent --driver-only --from file://original-opt-weka
					`,
		},
		SecurityContext: &v1.SecurityContext{
			Privileged: &privileged,
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      sharedVolumeName,
				MountPath: sharedVolumeMountPath,
			},
		},
	})

	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, v1.VolumeMount{
		Name:      sharedVolumeName,
		MountPath: sharedVolumeMountPath,
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, v1.Volume{
		Name: sharedVolumeName,
		VolumeSource: v1.VolumeSource{
			EmptyDir: &v1.EmptyDirVolumeSource{},
		},
	})

	// Set the target image so the container knows which version to use
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, v1.EnvVar{
		Name:  "TARGET_IMAGE_NAME",
		Value: payload.TargetImage,
	})
}
