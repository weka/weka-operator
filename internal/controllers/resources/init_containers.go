package resources

import (
	"encoding/json"

	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
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
		Image:   config.Config.MaintenanceImage,
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
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == "host-modules" {
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
					# Stage the weka CLI outside /opt/weka: the extraction step below bind-mounts
					# over /opt/weka, where the CLI lives, and would otherwise hide it.
					mkdir -p /shared-weka-version/cli || exit 1
					# the wekactl filename carries a per-image hash and the machine arch
					ARCH=$(uname -m)
					CLI=$(ls -1 /opt/weka/dist/image/wekactl-*-"$ARCH" 2>/dev/null | head -1)
					if [ -z "$CLI" ]; then
						# older images ship only the weka binary; resolve the path we shadow
						# rather than PATH, so a wrapper script can never be staged onto the
						# very path it delegates to
						CLI=$(readlink -f -- /usr/bin/weka)
					fi
					if [ -z "$CLI" ] || [ ! -f "$CLI" ]; then
						echo "ERROR: no weka CLI found to stage" >&2
						exit 1
					fi
					cp -- "$CLI" /shared-weka-version/cli/weka || exit 1
					# the source is not executable in the image, so set the bit explicitly
					chmod 0755 /shared-weka-version/cli/weka || exit 1
					echo "copy-cli init container done: staged $CLI"
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
	// the wrapper at /usr/local/bin/weka execs /usr/bin/weka, so shadowing that path makes the
	// container use the CLI staged from the cluster image instead of its own older one
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, v1.VolumeMount{
		Name:      sharedVolumeName,
		MountPath: "/usr/bin/weka",
		SubPath:   "cli/weka",
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
