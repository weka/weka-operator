package resources

import (
	"strconv"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ContainerFactory struct {
	container *wekav1alpha1.WekaContainer
}

func NewContainerFactory(container *wekav1alpha1.WekaContainer) *ContainerFactory {
	return &ContainerFactory{
		container: container,
	}
}

// NewDeployment creates a new deployment for the container
//
// The deployment creates a single pod locked a specific node
func (f *ContainerFactory) NewDeployment() (*appsv1.Deployment, error) {
	container := f.container.Spec
	objectMeta := f.container.ObjectMeta
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectMeta.Name,
			Namespace: objectMeta.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": container.Name,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": container.Name,
					},
				},
				Spec: v1.PodSpec{
					// Affinity: &container.Affinity,
					Containers: []v1.Container{
						{
							Name:            container.Name,
							Image:           container.Image,
							ImagePullPolicy: corev1.PullAlways,
							Command: []string{
								"sleep", "infinity",
								//"/usr/local/bin/supervisord", "-c", "/etc/supervisord/supervisord.conf",
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
									MountPath: "/mnt/huge",
									Name:      "hugepage-2mi-1",
								},
								{
									MountPath: "/etc/supervisord",
									Name:      "supervisord-config",
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									"cpu":           resource.MustParse("2"),
									"hugepages-2Mi": resource.MustParse("2Gi"),
									"memory":        resource.MustParse("8Gi"),
								},
								Requests: corev1.ResourceList{
									"cpu":           resource.MustParse("2"),
									"hugepages-2Mi": resource.MustParse("2Gi"),
									"memory":        resource.MustParse("8Gi"),
								},
							},
							Env: f.environmentVariables(),
							Ports: []corev1.ContainerPort{
								{ContainerPort: 14000},
								{ContainerPort: 14100},
							},
						},
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
	return deployment, nil
}

func (f *ContainerFactory) environmentVariables() []corev1.EnvVar {
	container := f.container.Spec
	variables := []corev1.EnvVar{
		{
			Name:  "WEKA_VERSION",
			Value: container.WekaVersion,
		},
		{
			Name:  "BACKEND_PRIVATE_IP",
			Value: container.BackendIP,
		},
		{
			Name: "MANAGEMENT_IPS",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.hostIP",
				},
			},
		},
		//{
		//Name:      "WEKA_USERNAME",
		//ValueFrom: &container.WekaUsername,
		//},
		//{
		//Name:      "WEKA_PASSWORD",
		//ValueFrom: &container.WekaPassword,
		//},
		{
			Name:  "MANAGEMENT_PORT",
			Value: strconv.Itoa(int(managementPort(container.ManagementPort, 0))),
		},
		{
			Name:  "BACKEND_NET",
			Value: container.InterfaceName,
		},
	}

	return variables
}
