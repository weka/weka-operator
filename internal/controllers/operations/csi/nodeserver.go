package csi

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

func NewCsiNodePod(
	name,
	namespace,
	csiDriverName,
	nodeName string,
	labels map[string]string,
	tolerations []corev1.Toleration,
	enforceTrustedHttps bool,
) *corev1.Pod {
	privileged := true
	args := []string{
		"--v=5",
		"--drivername=$(CSI_DRIVER_NAME)",
		"--endpoint=$(CSI_ENDPOINT)",
		"--nodeid=$(KUBE_NODE_NAME)",
		"--dynamic-path=$(CSI_DYNAMIC_PATH)",
		"--csimode=$(X_CSI_MODE)",
		"--newvolumeprefix=csivol-",
		"--newsnapshotprefix=csisnp-",
		"--seedsnapshotprefix=csisnp-seed-",
		"--enablemetrics",
		"--metricsport=9094",
		"--mutuallyexclusivemountoptions=readcache,writecache,coherent,forcedirect",
		"--mutuallyexclusivemountoptions=sync,async",
		"--mutuallyexclusivemountoptions=ro,rw",
		"--grpcrequesttimeoutseconds=30",
		"--concurrency.nodePublishVolume=5",
		"--concurrency.nodeUnpublishVolume=5",
		"--nfsprotocolversion=4.1",
	}
	if !enforceTrustedHttps {
		args = append(args, "--allowinsecurehttps")
	}

	tracingFlag := GetTracingFlag()
	if tracingFlag != "" {
		args = append(args, tracingFlag)
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"prometheus.io/scrape": "true",
				"prometheus.io/path":   "/metrics",
				"prometheus.io/port":   "9094",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           nodeName,
			ServiceAccountName: "csi-wekafs-node-sa",
			Containers: []corev1.Container{
				{
					Name: "wekafs",
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Image:           config.Config.CsiImage,
					ImagePullPolicy: corev1.PullAlways,
					Args:            args,
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 9899,
							Name:          "healthz",
							Protocol:      corev1.ProtocolTCP,
						},
						{
							ContainerPort: 9094,
							Name:          "metrics",
							Protocol:      corev1.ProtocolTCP,
						},
					},
					LivenessProbe: &corev1.Probe{
						FailureThreshold: 5,
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/healthz",
								Port: intstr.FromString("healthz"),
							},
						},
						InitialDelaySeconds: 10,
						TimeoutSeconds:      3,
						PeriodSeconds:       2,
					},
					Env: []corev1.EnvVar{
						{
							Name:  "CSI_DRIVER_NAME",
							Value: csiDriverName,
						},
						{
							Name:  "CSI_ENDPOINT",
							Value: "unix:///csi/csi.sock",
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
							Name:  "CSI_DYNAMIC_PATH",
							Value: "csi-volumes",
						},
						{
							Name:  "X_CSI_MODE",
							Value: "node",
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
							MountPropagation: mountPropagationBidirectional(),
							Name:             "mountpoint-dir",
						},
						{
							MountPath:        "/var/lib/kubelet/plugins",
							MountPropagation: mountPropagationBidirectional(),
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
						{
							MountPath: "/etc/nodeinfo",
							Name:      "nodeinfo",
							ReadOnly:  true,
						},
					},
				},
				{
					Name:  "liveness-probe",
					Image: config.Config.CsiLivenessProbeImage,
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
							Value: "9899",
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							MountPath: "/csi",
							Name:      "socket-dir",
						},
					},
				},
				{
					Name:  "csi-registrar",
					Image: config.Config.CsiRegistrarImage,
					Args: []string{
						"--v=5",
						"--csi-address=$(ADDRESS)",
						"--kubelet-registration-path=$(KUBELET_REGISTRATION_PATH)",
						"--timeout=60s",
						"--health-port=9809",
					},
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 9809,
							Name:          "healthz",
						},
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/healthz",
								Port: intstr.FromString("healthz"),
							},
						},
						InitialDelaySeconds: 5,
						TimeoutSeconds:      5,
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Env: []corev1.EnvVar{
						{
							Name:  "ADDRESS",
							Value: "unix:///csi/csi.sock",
						},
						{
							Name:  "KUBELET_REGISTRATION_PATH",
							Value: fmt.Sprintf("/var/lib/kubelet/plugins/%s/csi.sock", name),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							MountPath: "/csi",
							Name:      "socket-dir",
						},
						{
							MountPath: "/registration",
							Name:      "registration-dir",
						},
						{
							MountPath: "/var/lib/csi-wekafs-data",
							Name:      "csi-data-dir",
						},
					},
				},
			},
			Tolerations: tolerations,
			Volumes: []corev1.Volume{
				{
					Name: "mountpoint-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/pods",
							Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
						},
					},
				},
				{
					Name: "registration-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/plugins_registry",
							Type: hostPathTypePtr(corev1.HostPathDirectory),
						},
					},
				},
				{
					Name: "plugins-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/plugins",
							Type: hostPathTypePtr(corev1.HostPathDirectory),
						},
					},
				},
				{
					Name: "socket-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/kubelet/plugins/" + name,
							Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
						},
					},
				},
				{
					Name: "csi-data-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/lib/csi-wekafs-data/",
							Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
						},
					},
				},
				{
					Name: "dev-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/dev",
							Type: hostPathTypePtr(corev1.HostPathDirectory),
						},
					},
				},
				{
					Name: "nodeinfo",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}
}

func CheckAndDeleteOutdatedCsiNode(
	ctx context.Context,
	pod *corev1.Pod,
	c client.Client,
	csiDriverName string,
	labels map[string]string,
	tolerations []corev1.Toleration,
	enforceTrustedHttps bool,
) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	outdated := false
	for _, podContainer := range pod.Spec.Containers {
		switch podContainer.Name {
		case "csi-registrar":
			outdated = outdated || podContainer.Image != config.Config.CsiRegistrarImage
		case "liveness-probe":
			outdated = outdated || podContainer.Image != config.Config.CsiLivenessProbeImage
		case "wekafs":
			outdated = outdated || podContainer.Image != config.Config.CsiImage
			for _, env := range podContainer.Env {
				if env.Name == "CSI_DRIVER_NAME" {
					outdated = outdated || (env.Value != csiDriverName)
				}
				var allowInsecureHttpsFlagExists bool
				for _, arg := range podContainer.Args {
					if arg == "--allowinsecurehttps" {
						allowInsecureHttpsFlagExists = true
					}
				}
				if allowInsecureHttpsFlagExists && enforceTrustedHttps {
					outdated = true
				}
				if !allowInsecureHttpsFlagExists && !enforceTrustedHttps {
					outdated = true
				}
			}
		}
	}
	outdated = outdated || !util.AreMapsEqual(labels, pod.Labels) || !util.CompareTolerations(pod.Spec.Tolerations, tolerations, true) // ignore unhealthy default tolerations, since they are added by k8s on the pod level

	if outdated {
		logger.Info("CSI node spec changed, re-deploying")
		err := c.Delete(ctx, pod)
		if err != nil {
			return errors.Wrap(err, "failed to delete CSI node")
		}
	}
	return nil
}

func hostPathTypePtr(hostPathType corev1.HostPathType) *corev1.HostPathType {
	return &hostPathType
}

func mountPropagationBidirectional() *corev1.MountPropagationMode {
	propagationMode := corev1.MountPropagationBidirectional
	return &propagationMode
}
