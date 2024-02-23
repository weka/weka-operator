package resources

import (
	"fmt"
	"strconv"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func AgentResource(client *wekav1alpha1.Client, key types.NamespacedName) (*appsv1.DaemonSet, error) {
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

	dep := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      client.Name,
			Namespace: client.Namespace,
			Labels:    ls,
		},
		Spec: appsv1.DaemonSetSpec{
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
						// wekaClientContainer(client, image),
					},
					InitContainers: []corev1.Container{
						driverInitContainer(client, image),
					},
					Volumes: []corev1.Volume{
						{
							Name: "host-dev",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/dev",
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
							Name: "supervisord-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "supervisord-conf",
									},
									Items: []corev1.KeyToPath{
										{
											Key:  "supervisord.conf",
											Path: "supervisord.conf",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return dep, nil
}

func wekaAgentContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	container := corev1.Container{
		Image:           image,
		Name:            "weka-agent",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			//"sleep", "infinity",
			"/usr/local/bin/supervisord", "-c", "/etc/supervisord/supervisord.conf",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{
					"ALL",
				},
			},
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeUnconfined,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				MountPath: "/dev",
				Name:      "host-dev",
			},
			{
				MountPath: "/dev/hugepages",
				Name:      "hugepage-2mi-1",
			},
			{
				MountPath: "/etc/supervisord",
				Name:      "supervisord-config",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"hugepages-2Mi": resource.MustParse("1500Mi"),
				"memory":        resource.MustParse("8Gi"),
				"cpu":           resource.MustParse("2"),
			},
			Requests: corev1.ResourceList{
				"memory":        resource.MustParse("8Gi"),
				"hugepages-2Mi": resource.MustParse("1500Mi"),
				"cpu":           resource.MustParse("2"),
			},
		},
		Env: environmentVariables(client),
		Ports: []corev1.ContainerPort{
			{ContainerPort: 14000},
			{ContainerPort: 14100},
		},
	}

	return container
}

func environmentVariables(client *wekav1alpha1.Client) []corev1.EnvVar {
	variables := []corev1.EnvVar{
		{
			Name:  "WEKA_VERSION",
			Value: client.Spec.Version,
		},
		{
			Name:  "DRIVERS_VERSION",
			Value: client.Spec.Version,
		},
		{
			Name:  "BACKEND_PRIVATE_IP",
			Value: client.Spec.BackendIP,
		},
		{
			Name: "MANAGEMENT_IPS",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.hostIP",
				},
			},
		},
		{
			Name:  "WEKA_CLI_DEBUG",
			Value: wekaCliDebug(client.Spec.Agent.Debug),
		},
		{
			Name:      "WEKA_USERNAME",
			ValueFrom: &client.Spec.WekaUsername,
		},
		{
			Name:      "WEKA_PASSWORD",
			ValueFrom: &client.Spec.WekaPassword,
		},
		{
			Name:  "WEKA_ORG",
			Value: wekaOrg(client),
		},
		{
			Name:  "MANAGEMENT_PORT",
			Value: strconv.Itoa(int(managementPort(client.Spec.ManagementPortBase, 0))),
		},
	}

	return variables
}

// driverInitContainer creates an init container that loads the weka driver
//
// This is based on the same agent image
func driverInitContainer(client *wekav1alpha1.Client, image string) corev1.Container {
	container := corev1.Container{
		Image:           image,
		Name:            "weka-driver-loader",
		ImagePullPolicy: corev1.PullAlways,
		Command: []string{
			"/opt/install-drivers.sh",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &[]bool{false}[0],
			Privileged:   &[]bool{true}[0],
			RunAsUser:    &[]int64{0}[0],
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{
					"ALL",
				},
			},
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeUnconfined,
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "WEKA_VERSION",
				Value: client.Spec.Version,
			},
			{
				Name:  "BACKEND_PRIVATE_IP",
				Value: client.Spec.BackendIP,
			},
			{
				Name:  "BACKEND_PORT",
				Value: strconv.Itoa(int(managementPort(client.Spec.ManagementPortBase, 0))),
			},
		},
		VolumeMounts: []corev1.VolumeMount{},
	}

	return container
}

func wekaCliDebug(debug bool) string {
	if debug {
		return "1"
	} else {
		return ""
	}
}

func wekaOrg(client *wekav1alpha1.Client) string {
	if client.Spec.WekaOrg != "" {
		return client.Spec.WekaOrg
	} else {
		return "0"
	}
}

func managementPort(base, offset int32) int32 {
	if base == 0 {
		base = 14000
	}
	return base + offset
}
