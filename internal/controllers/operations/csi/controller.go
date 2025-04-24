package csi

import (
	"github.com/weka/weka-operator/internal/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func NewCSIControllerDeployment(name string, namespace string, csiDriverName string, tolerations []corev1.Toleration) *appsv1.Deployment {
	privileged := true
	replicas := int32(2)

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app":       name,
				"component": name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       name,
					"component": name,
				},
			},
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       name,
						"component": name,
					},
					Annotations: map[string]string{
						"prometheus.io/scrape": "true",
						"prometheus.io/path":   "/metrics",
						"prometheus.io/port":   "9090,9091,9092,9093,9095",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "csi-wekafs-controller-sa",
					Containers: []corev1.Container{
						{
							Name: "wekafs",
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Image:           config.Config.CSIImage,
							ImagePullPolicy: corev1.PullAlways,
							Args: []string{
								"--drivername=$(CSI_DRIVER_NAME)",
								"--v=5",
								"--endpoint=$(CSI_ENDPOINT)",
								"--nodeid=$(KUBE_NODE_NAME)",
								"--dynamic-path=$(CSI_DYNAMIC_PATH)",
								"--csimode=$(X_CSI_MODE)",
								"--newvolumeprefix=csivol-",
								"--newsnapshotprefix=csisnp-",
								"--seedsnapshotprefix=csisnp-seed-",
								"--allowautofscreation",
								"--allowautofsexpansion",
								"--allowinsecurehttps",
								"--enablemetrics",
								"--metricsport=9090",
								"--mutuallyexclusivemountoptions=readcache,writecache,coherent,forcedirect",
								"--mutuallyexclusivemountoptions=sync,async",
								"--mutuallyexclusivemountoptions=ro,rw",
								"--grpcrequesttimeoutseconds=30",
								"--concurrency.createVolume=5",
								"--concurrency.deleteVolume=5",
								"--concurrency.expandVolume=5",
								"--concurrency.createSnapshot=5",
								"--concurrency.deleteSnapshot=5",
								"--nfsprotocolversion=4.1",
								GetTracingFlag(),
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9898,
									Name:          "healthz",
									Protocol:      corev1.ProtocolTCP,
								},
								{
									ContainerPort: 9090,
									Name:          "metrics",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							LivenessProbe: &corev1.Probe{
								FailureThreshold:    5,
								InitialDelaySeconds: 10,
								TimeoutSeconds:      3,
								PeriodSeconds:       2,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromString("healthz"),
									},
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "CSI_ENDPOINT",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "CSI_DRIVER_NAME",
									Value: csiDriverName,
								},
								{
									Name:  "CSI_DRIVER_VERSION",
									Value: config.Config.CSIDriverVersion,
								},
								{
									Name:  "X_CSI_MODE",
									Value: "controller",
								},
								{
									Name:  "CSI_DYNAMIC_PATH",
									Value: "csi-volumes",
								},
								{
									Name:  "X_CSI_DEBUG",
									Value: "false",
								},
								{
									Name: "KUBE_NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name: "KUBE_NODE_IP_ADDRESS",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "status.hostIP",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/csi",
									Name:      "socket-dir",
								},
								{
									MountPath:        "/var/lib/kubelet/pods",
									MountPropagation: (*corev1.MountPropagationMode)(ptr(string(corev1.MountPropagationBidirectional))),
									Name:             "mountpoint-dir",
								},
								{
									MountPath:        "/var/lib/kubelet/plugins",
									MountPropagation: (*corev1.MountPropagationMode)(ptr(string(corev1.MountPropagationBidirectional))),
									Name:             "plugins-dir",
								},
								{
									MountPath: "/var/lib/csi-wekafs-data",
									Name:      "csi-data-dir",
								},
								{
									MountPath: "/dev",
									Name:      "dev-dir",
								},
							},
						},
						{
							Name:  "csi-attacher",
							Image: config.Config.CSIAttacherImage,
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Args: []string{
								"--csi-address=$(ADDRESS)",
								"--v=5",
								"--timeout=60s",
								"--leader-election",
								"--leader-election-namespace=" + namespace,
								"--worker-threads=5",
								"--http-endpoint=:9095",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(9095),
										Path: "/healthz/leader-election",
									},
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9095,
									Name:          "pr-metrics",
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
						{
							Name:  "csi-provisioner",
							Image: config.Config.CSIProvisionerImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
								"--feature-gates=Topology=true",
								"--timeout=60s",
								"--prevent-volume-mode-conversion",
								"--leader-election",
								"--leader-election-namespace=" + namespace,
								"--worker-threads=5",
								"--retry-interval-start=10s",
								"--http-endpoint=:9091",
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(9091),
										Path: "/healthz/leader-election",
									},
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9091,
									Name:          "pr-metrics",
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
						{
							Name:  "csi-resizer",
							Image: config.Config.CSIResizerImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
								"--timeout=60s",
								"--http-endpoint=:9092",
								"--leader-election",
								"--leader-election-namespace=" + namespace,
								"--workers=5",
								"--retry-interval-start=10s",
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(9092),
										Path: "/healthz/leader-election",
									},
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9092,
									Name:          "rs-metrics",
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
						{
							Name:  "csi-snapshotter",
							Image: config.Config.CSISnapshotterImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
								"--timeout=60s",
								"--leader-election",
								"--leader-election-namespace=" + namespace,
								"--worker-threads=5",
								"--retry-interval-start=10s",
								"--http-endpoint=:9093",
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Port: intstr.FromInt(9093),
										Path: "/healthz/leader-election",
									},
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9093,
									Name:          "sn-metrics",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
							},
							ImagePullPolicy: corev1.PullIfNotPresent,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
						{
							Name: "liveness-probe",
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/csi",
									Name:      "socket-dir",
								},
							},
							Image: config.Config.CSILivenessProbeImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
								"--health-port=$(HEALTH_PORT)",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "HEALTH_PORT",
									Value: "9898",
								},
							},
						},
					},
					Tolerations: tolerations,
					Volumes: []corev1.Volume{
						{
							Name: "socket-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins/" + name,
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "mountpoint-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/pods",
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "registration-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins_registry",
									Type: typePtr(corev1.HostPathDirectory),
								},
							},
						},
						{
							Name: "plugins-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins",
									Type: typePtr(corev1.HostPathDirectory),
								},
							},
						},
						{
							Name: "csi-data-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/csi-wekafs-data/",
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "dev-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/dev",
									Type: typePtr(corev1.HostPathDirectory),
								},
							},
						},
					},
				},
			},
		},
	}
}

// Helper function to create pointers to primitive types
func ptr(s string) *string {
	return &s
}

// Helper function for HostPathType
func typePtr(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}
