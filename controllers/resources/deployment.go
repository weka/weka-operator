package resources

import (
	"fmt"
	"strconv"

	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (b *Builder) DeploymentForClient(client *wekav1alpha1.Client, key types.NamespacedName) (*appsv1.Deployment, error) {
	runAsNonRoot := false
	privileged := true
	runAsUser := int64(0)

	image := fmt.Sprintf("%s:%s", client.Spec.Image, client.Spec.Version)

	ls := map[string]string{
		"app.kubernetes.io": "weka-agent",
		"iteration":         "2",
		"runAsNonRoot":      strconv.FormatBool(runAsNonRoot),
		"privileged":        strconv.FormatBool(privileged),
		"runAsUser":         strconv.FormatInt(runAsUser, 10),
	}
	replicas := int32(1)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      client.Name,
			Namespace: client.Namespace,
			Labels:    ls,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io": "weka-agent",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &[]bool{true}[0],
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					ImagePullSecrets: []corev1.LocalObjectReference{
						{
							Name: client.Spec.ImagePullSecretName,
						},
					},
					Containers: []corev1.Container{
						// Agent Container
						wekaAgentContainer(client, image),
						wekaClientContainer(client, image),
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-root",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
						{
							Name: "host-dev",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/dev",
								},
							},
						},
						{
							Name: "host-cgroup",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys/fs/cgroup",
								},
							},
						},
						{
							Name: "opt-weka-data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "hugepage-2mi-1",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium: corev1.StorageMediumHugePages,
								},
							},
						},
						{
							Name: "hugepage-2mi-2",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium: corev1.StorageMediumHugePages,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(client, dep, b.scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}
	return dep, nil
}

func wekaAgentContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	container := corev1.Container{
		Image:           image,
		Name:            "weka-agent",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			"/usr/bin/weka",
			"--agent",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				MountPath: "/mnt/root",
				Name:      "host-root",
			},
			{
				MountPath: "/dev",
				Name:      "host-dev",
			},
			{
				MountPath: "/sys/fs/cgroup",
				Name:      "host-cgroup",
			},
			{
				MountPath: "/opt/weka/data",
				Name:      "opt-weka-data",
			},
			{
				MountPath: "/dev/hugepages",
				Name:      "hugepage-2mi-1",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"hugepages-2Mi": resource.MustParse("512Mi"),
				"memory":        resource.MustParse("4Gi"),
			},
			Requests: corev1.ResourceList{
				"memory":        resource.MustParse("4Gi"),
				"hugepages-2Mi": resource.MustParse("512Mi"),
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "WEKA_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "DRIVERS_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "IONODE_COUNT",
				Value: strconv.Itoa(int(client.Spec.IONodeCount)),
			},
			{
				Name:  "BACKEND_PRIVATE_IP",
				Value: client.Spec.Backend.IP,
			},
			{
				Name:  "WEKA_CLI_DEBUG",
				Value: wekaCliDebug(client.Spec.Agent.Debug),
			},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 14000},
			{ContainerPort: 14100},
		},
	}

	return container
}

func wekaClientContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	return corev1.Container{
		Image:           image,
		Name:            "weka-client",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			//"sleep", "infinity",
			"/opt/start-weka-client.sh",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				MountPath: "/mnt/root",
				Name:      "host-root",
			},
			{
				MountPath: "/dev",
				Name:      "host-dev",
			},
			{
				MountPath: "/sys/fs/cgroup",
				Name:      "host-cgroup",
			},
			{
				MountPath: "/opt/weka/data",
				Name:      "opt-weka-data",
			},
			{
				Name:      "hugepage-2mi-2",
				MountPath: "/dev/hugepages",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"hugepages-2Mi": resource.MustParse("512Mi"),
				"memory":        resource.MustParse("512Mi"),
			},
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("512Mi"),
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "WEKA_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "DRIVERS_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "IONODE_COUNT",
				Value: strconv.Itoa(int(client.Spec.IONodeCount)),
			},
			{
				Name:  "BACKEND_PRIVATE_IP",
				Value: client.Spec.Backend.IP,
			},
			{
				Name:  "MANAGEMENT_IPS",
				Value: client.Spec.ManagementIPs,
			},
			{
				Name:  "WEKA_CLI_DEBUG",
				Value: wekaCliDebug(client.Spec.Client.Debug),
			},
		},
	}
}

func wekaCliDebug(debug bool) string {
	if debug {
		return "1"
	} else {
		return ""
	}
}
