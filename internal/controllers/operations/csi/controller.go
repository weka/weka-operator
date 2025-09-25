package csi

import (
	"context"
	"fmt"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/weka/weka-operator/internal/config"
	util2 "github.com/weka/weka-operator/pkg/util"
)

// CsiControllerHashableSpec represents the fields from CSI Controller Deployment
// that are relevant for determining if an update is needed
type CsiControllerHashableSpec struct {
	CsiDriverName         string
	CsiImage              string
	CsiAttacherImage      string
	CsiProvisionerImage   string
	CsiResizerImage       string
	CsiSnapshotterImage   string
	CsiLivenessProbeImage string
	Labels                *util2.HashableMap
	Tolerations           []corev1.Toleration
	NodeSelector          *util2.HashableMap
	EnforceTrustedHttps   bool
	SkipGarbageCollection bool
}

// GetCsiControllerDeploymentHash generates a hash for the CSI Controller Deployment
// that includes only the fields that are relevant for updates
func GetCsiControllerDeploymentHash(csiGroupName string, wekaClient *weka.WekaClient) (string, error) {
	csiDriverName := GetCsiDriverName(csiGroupName)
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations)

	var csiLabels map[string]string
	var enforceTrustedHttps bool
	var skipGarbageCollection bool

	if wekaClient.Spec.CsiConfig != nil && wekaClient.Spec.CsiConfig.Advanced != nil {
		tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.ControllerTolerations...)
		csiLabels = wekaClient.Spec.CsiConfig.Advanced.ControllerLabels
		enforceTrustedHttps = wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
		skipGarbageCollection = wekaClient.Spec.CsiConfig.Advanced.SkipGarbageCollection
	}

	// Get the complete labels that would be applied to the deployment
	labels := GetCsiLabels(csiDriverName, CSIController, wekaClient.Labels, csiLabels)

	// Convert maps to HashableMap for consistent hashing
	labelsHashable := util2.NewHashableMap(labels)
	var nodeSelectorHashable *util2.HashableMap
	if wekaClient.Spec.NodeSelector != nil {
		nodeSelectorHashable = util2.NewHashableMap(wekaClient.Spec.NodeSelector)
	}

	spec := CsiControllerHashableSpec{
		CsiDriverName:         csiDriverName,
		CsiImage:              config.Config.Csi.WekafsImage,
		CsiAttacherImage:      config.Config.Csi.ProvisionerImage,
		CsiResizerImage:       config.Config.Csi.ResizerImage,
		CsiSnapshotterImage:   config.Config.Csi.SnapshotterImage,
		CsiLivenessProbeImage: config.Config.Csi.LivenessProbeImage,
		Labels:                labelsHashable,
		Tolerations:           tolerations,
		NodeSelector:          nodeSelectorHashable,
		EnforceTrustedHttps:   enforceTrustedHttps,
		SkipGarbageCollection: skipGarbageCollection,
	}

	return util2.HashStruct(spec)
}

func GetCSIControllerName(CSIGroupName string) string {
	return strings.Replace(CSIGroupName, ".", "-", -1) + "-weka-csi-controller"
}

func GetCsiDriverName(csiGroup string) string {
	return fmt.Sprintf("%s.weka.io", csiGroup)
}

func NewCsiControllerDeployment(ctx context.Context, csiGroupName string, wekaClient *weka.WekaClient) (*appsv1.Deployment, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "NewCsiControllerDeployment")
	defer end()

	name := GetCSIControllerName(csiGroupName)
	csiDriverName := GetCsiDriverName(csiGroupName)
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations)
	var csiLabels map[string]string
	var enforceTrustedHttps bool
	var skipGarbageCollection bool
	if wekaClient.Spec.CsiConfig != nil && wekaClient.Spec.CsiConfig.Advanced != nil {
		tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.ControllerTolerations...)
		csiLabels = wekaClient.Spec.CsiConfig.Advanced.ControllerLabels
		enforceTrustedHttps = wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
		skipGarbageCollection = wekaClient.Spec.CsiConfig.Advanced.SkipGarbageCollection
	}
	labels := GetCsiLabels(csiDriverName, CSIController, wekaClient.Labels, csiLabels)

	targetHash, err := GetCsiControllerDeploymentHash(csiGroupName, wekaClient)
	if err != nil {
		logger.Error(err, "Failed to get CSI controller deployment hash")
		return nil, fmt.Errorf("failed to get CSI controller deployment hash: %w", err)
	}

	nodeSelector := wekaClient.Spec.NodeSelector
	namespace, _ := util2.GetPodNamespace()

	privileged := true
	replicas := int32(2)

	args := []string{
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
	}

	if !enforceTrustedHttps {
		args = append(args, "--allowinsecurehttps")
	}
	if skipGarbageCollection {
		args = append(args, "--skipgarbagecollection")
	}

	tracingFlag := GetTracingFlag()
	if tracingFlag != "" {
		args = append(args, tracingFlag)
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
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
						"prometheus.io/scrape":        "true",
						"prometheus.io/path":          "/metrics",
						"prometheus.io/port":          "9090,9091,9092,9093,9095",
						"weka.io/csi-controller-hash": targetHash,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector:       nodeSelector,
					ServiceAccountName: "csi-wekafs-controller-sa",
					Containers: []corev1.Container{
						{
							Name: "wekafs",
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Image:           config.Config.Csi.WekafsImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            args,
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
							Image: config.Config.Csi.AttacherImage,
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
							Image: config.Config.Csi.ProvisionerImage,
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
							Image: config.Config.Csi.ResizerImage,
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
							Image: config.Config.Csi.SnapshotterImage,
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
							Image: config.Config.Csi.LivenessProbeImage,
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
	}, nil
}

// Helper function to create pointers to primitive types
func ptr(s string) *string {
	return &s
}

// Helper function for HostPathType
func typePtr(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}
