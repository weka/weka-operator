package resources

import (
	"fmt"
	"strconv"
	"strings"

	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
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
					},
					// Limit pods to running on nodes with the drivers installed.  These
					// nodes are identified by the presence of the label
					// `kmm.node.kubernetes.io/weka-operator-system.<driver-name>.ready`.
					// This label has no value.
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "kmm.node.kubernetes.io/weka-operator-system.mpin-user.ready",
												Operator: corev1.NodeSelectorOpExists,
											},
											{
												Key:      "kmm.node.kubernetes.io/weka-operator-system.wekafsgw.ready",
												Operator: corev1.NodeSelectorOpExists,
											},
											{
												Key:      "kmm.node.kubernetes.io/weka-operator-system.wekafsio.ready",
												Operator: corev1.NodeSelectorOpExists,
											},
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
			"/usr/local/bin/supervisord", "-c", "/etc/supervisor.conf",
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
				MountPath: "/mnt/root",
				Name:      "host-root",
			},
			{
				MountPath: "/sys/fs/cgroup",
				Name:      "host-cgroup",
			},
			{
				MountPath: "/dev/hugepages",
				Name:      "hugepage-2mi-1",
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"hugepages-2Mi": resource.MustParse("1500Mi"),
				"memory":        resource.MustParse("8Gi"),
			},
			Requests: corev1.ResourceList{
				"memory":        resource.MustParse("8Gi"),
				"hugepages-2Mi": resource.MustParse("1500Mi"),
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
	}

	if len(client.Spec.CoreIds) > 0 {
		variables = append(variables, corev1.EnvVar{
			Name:  "CORE_IDS",
			Value: coreIds(client),
		})
	} else if client.Spec.IONodeCount != 0 {
		variables = append(variables, corev1.EnvVar{
			Name:  "IONODE_COUNT",
			Value: strconv.Itoa(int(client.Spec.IONodeCount)),
		})
	}

	return variables
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

func coreIds(client *wekav1alpha1.Client) string {
	coreIds := []string{}
	for _, coreId := range client.Spec.CoreIds {
		coreIds = append(coreIds, strconv.Itoa(int(coreId)))
	}
	return strings.Join(coreIds, ",")
}
