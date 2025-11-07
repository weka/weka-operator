package csi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	util2 "github.com/weka/weka-operator/pkg/util"
)

// CsiNodeHashableSpec represents the fields from CSI Node DaemonSet
// that are relevant for determining if an update is needed
type CsiNodeHashableSpec struct {
	CsiDriverName         string
	CsiImage              string
	CsiRegistrarImage     string
	CsiLivenessProbeImage string
	Labels                *util2.HashableMap
	Tolerations           []corev1.Toleration
	NodeSelector          *util2.HashableMap
	EnforceTrustedHttps   bool
	LogLevel              int
	PriorityClassName     string
}

// GetCsiNodeDaemonSetHash generates a hash for the CSI Node DaemonSet
// that includes only the fields that are relevant for updates
func GetCsiNodeDaemonSetHash(csiGroupName string, wekaClient *weka.WekaClient) (string, error) {
	csiDriverName := GetCsiDriverName(csiGroupName)
	// CSI node plugins are infrastructure components and must run on all nodes
	tolerations := []corev1.Toleration{
		{
			Operator: corev1.TolerationOpExists, // Tolerates all taints
		},
	}

	var csiLabels map[string]string
	var enforceTrustedHttps bool

	if wekaClient.Spec.CsiConfig != nil && wekaClient.Spec.CsiConfig.Advanced != nil {
		tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.NodeTolerations...)
		csiLabels = wekaClient.Spec.CsiConfig.Advanced.NodeLabels
		enforceTrustedHttps = wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
	}

	// Get the complete labels that would be applied to the daemonset
	labels := GetCsiLabels(csiDriverName, CSINode, wekaClient.Labels, csiLabels)

	// Convert maps to HashableMap for consistent hashing
	labelsHashable := util2.NewHashableMap(labels)
	var nodeSelectorHashable *util2.HashableMap
	if wekaClient.Spec.NodeSelector != nil {
		nodeSelectorHashable = util2.NewHashableMap(wekaClient.Spec.NodeSelector)
	}

	spec := CsiNodeHashableSpec{
		CsiDriverName:         csiDriverName,
		CsiImage:              config.Config.Csi.WekafsImage,
		CsiRegistrarImage:     config.Config.Csi.RegistrarImage,
		CsiLivenessProbeImage: config.Config.Csi.LivenessProbeImage,
		Labels:                labelsHashable,
		Tolerations:           tolerations,
		NodeSelector:          nodeSelectorHashable,
		EnforceTrustedHttps:   enforceTrustedHttps,
		LogLevel:              config.Config.Csi.LogLevel,
		PriorityClassName:     config.Config.PriorityClasses.Targeted,
	}

	return util2.HashStruct(spec)
}

func GetCSINodeDaemonSetName(CSIGroupName string) string {
	return strings.Replace(CSIGroupName, ".", "-", -1) + "-weka-csi-node"
}

func NewCsiNodeDaemonSet(ctx context.Context, csiGroupName string, wekaClient *weka.WekaClient) (*appsv1.DaemonSet, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "NewCsiNodeDaemonSet")
	defer end()

	name := GetCSINodeDaemonSetName(csiGroupName)
	csiDriverName := GetCsiDriverName(csiGroupName)
	// CSI node plugins are infrastructure components and must run on all nodes
	// Use wildcard toleration to ensure CSI runs everywhere, like node-agent
	tolerations := []corev1.Toleration{
		{
			Operator: corev1.TolerationOpExists, // Tolerates all taints
		},
	}
	csiLabels := map[string]string{
		"app.kubernetes.io/created-by": "weka-operator",
	}
	var enforceTrustedHttps bool
	if wekaClient.Spec.CsiConfig != nil && wekaClient.Spec.CsiConfig.Advanced != nil {
		tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.NodeTolerations...)
		csiLabels = wekaClient.Spec.CsiConfig.Advanced.NodeLabels
		enforceTrustedHttps = wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
	}
	labels := GetCsiLabels(csiDriverName, CSINode, wekaClient.Labels, csiLabels)

	targetHash, err := GetCsiNodeDaemonSetHash(csiGroupName, wekaClient)
	if err != nil {
		logger.Error(err, "Failed to get CSI node daemonset hash")
		return nil, fmt.Errorf("failed to get CSI node daemonset hash: %w", err)
	}

	nodeSelector := wekaClient.Spec.NodeSelector
	namespace, _ := util2.GetPodNamespace()

	privileged := true

	args := []string{
		"--v=$(LOG_LEVEL)",
		"--wekafscontainername=$(WEKAFS_CONTAINER_NAME)",
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

	wekaContainerName := resources.GetWekaClientContainerName(wekaClient)

	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       name,
					"component": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       name,
						"component": name,
					},
					Annotations: map[string]string{
						"prometheus.io/scrape":  "true",
						"prometheus.io/path":    "/metrics",
						"prometheus.io/port":    "9094",
						"weka.io/csi-node-hash": targetHash,
						// link the daemonset to the client for easier identification of "owner"
						// NOTE: we cannot use owner references because the client and node are in different namespaces
						"weka.io/csi-node-owner":           string(wekaClient.GetUID()),
						"weka.io/csi-node-owner-name":      wekaClient.Name,
						"weka.io/csi-node-owner-namespace": wekaClient.Namespace,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector:       nodeSelector,
					ServiceAccountName: "csi-wekafs-node-sa",
					PriorityClassName:  config.Config.PriorityClasses.Targeted,
					Containers: []corev1.Container{
						{
							Name: "wekafs",
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Image:           config.Config.Csi.WekafsImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            args,
							Resources:       toK8sResourceRequirements(config.Config.Csi.NodeResources.Wekafs),
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
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
								},
								{
									Name:  "WEKAFS_CONTAINER_NAME",
									Value: wekaContainerName,
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
								{
									MountPath: "/etc/nodeinfo",
									Name:      "nodeinfo",
									ReadOnly:  true,
								},
							},
						},
						{
							Name:      "liveness-probe",
							Image:     config.Config.Csi.LivenessProbeImage,
							Resources: toK8sResourceRequirements(config.Config.Csi.NodeResources.LivenessProbe),
							Args: []string{
								"--v=$(LOG_LEVEL)",
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
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
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
							Name:      "csi-registrar",
							Image:     config.Config.Csi.RegistrarImage,
							Resources: toK8sResourceRequirements(config.Config.Csi.NodeResources.CsiRegistrar),
							Args: []string{
								"--v=$(LOG_LEVEL)",
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
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
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
							Name: "socket-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins/" + name,
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
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
						{
							Name: "nodeinfo",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}, nil
}
