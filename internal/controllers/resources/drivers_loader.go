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
