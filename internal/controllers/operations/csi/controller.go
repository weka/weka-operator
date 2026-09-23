package csi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
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
	Labels                *util2.HashableMap
	Tolerations           []corev1.Toleration
	NodeSelector          *util2.HashableMap
	EnforceTrustedHttps   bool
	SkipGarbageCollection bool
	LogLevel              int
	PriorityClassName     string
	WekaContainerName     string
	SelinuxSupport        string
	KubeletPath           string
	HostNetwork           bool
	MetricsEnabled        bool
}

// GetCsiControllerDeploymentHash generates a hash for the CSI Controller Deployment
// that includes only the fields that are relevant for updates
func GetCsiControllerDeploymentHash(csiGroupName string, wekaClient *weka.WekaClient, settings services.CsiSettings) (string, error) {
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
		CsiAttacherImage:      config.Config.Csi.AttacherImage,
		CsiProvisionerImage:   config.Config.Csi.ProvisionerImage,
		CsiResizerImage:       config.Config.Csi.ResizerImage,
		CsiSnapshotterImage:   config.Config.Csi.SnapshotterImage,
		Labels:                labelsHashable,
		Tolerations:           tolerations,
		NodeSelector:          nodeSelectorHashable,
		EnforceTrustedHttps:   enforceTrustedHttps,
		SkipGarbageCollection: skipGarbageCollection,
		LogLevel:              config.Config.Csi.LogLevel,
		PriorityClassName:     config.Config.PriorityClasses.Targeted,
		WekaContainerName:     resources.GetWekaClientContainerName(wekaClient),
		SelinuxSupport:        config.Config.Csi.SelinuxSupport,
		KubeletPath:           config.Config.Csi.KubeletPath,
		HostNetwork:           config.Config.Csi.HostNetwork,
		MetricsEnabled:        settings.MetricsEnabled,
	}

	return util2.HashStruct(spec)
}

func GetCSIControllerName(csiGroupName string) string {
	return strings.ReplaceAll(csiGroupName, ".", "-") + "-weka-csi-controller"
}

func GetCsiDriverName(csiGroup string) string {
	return fmt.Sprintf("%s.weka.io", csiGroup)
}

func NewCsiControllerDeployment(ctx context.Context, csiGroupName string, wekaClient *weka.WekaClient, settings services.CsiSettings) (*appsv1.Deployment, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "NewCsiControllerDeployment")
	defer logger.End()

	name := GetCSIControllerName(csiGroupName)
	csiDriverName := GetCsiDriverName(csiGroupName)
	tolerations := util.ExpandTolerations([]corev1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations)
	csiLabels := map[string]string{
		"app.kubernetes.io/created-by": "weka-operator",
	}
	var enforceTrustedHttps bool
	var skipGarbageCollection bool
	if wekaClient.Spec.CsiConfig != nil && wekaClient.Spec.CsiConfig.Advanced != nil {
		tolerations = append(tolerations, wekaClient.Spec.CsiConfig.Advanced.ControllerTolerations...)
		csiLabels = util2.MergeMaps(csiLabels, wekaClient.Spec.CsiConfig.Advanced.ControllerLabels)
		enforceTrustedHttps = wekaClient.Spec.CsiConfig.Advanced.EnforceTrustedHttps
		skipGarbageCollection = wekaClient.Spec.CsiConfig.Advanced.SkipGarbageCollection
	}
	labels := GetCsiLabels(csiDriverName, CSIController, wekaClient.Labels, csiLabels)

	targetHash, err := GetCsiControllerDeploymentHash(csiGroupName, wekaClient, settings)
	if err != nil {
		logger.Error(err, "Failed to get CSI controller deployment hash")
		return nil, fmt.Errorf("failed to get CSI controller deployment hash: %w", err)
	}

	nodeSelector := wekaClient.Spec.NodeSelector
	namespace, _ := util2.GetPodNamespace() //nolint:errcheck // namespace used for object metadata only; failure falls back to empty string

	privileged := true
	replicas := int32(2)

	wekaContainerName := resources.GetWekaClientContainerName(wekaClient)

	args := []string{
		"--drivername=$(CSI_DRIVER_NAME)",
		"--wekafscontainername=$(WEKAFS_CONTAINER_NAME)",
		"--v=$(LOG_LEVEL)",
		"--endpoint=$(CSI_ENDPOINT)",
		"--nodeid=$(KUBE_NODE_NAME)",
		"--dynamic-path=$(CSI_DYNAMIC_PATH)",
		"--csimode=$(X_CSI_MODE)",
		"--newvolumeprefix=csivol-",
		"--newsnapshotprefix=csisnp-",
		"--seedsnapshotprefix=csisnp-seed-",
		"--allowautofscreation",
		"--allowautofsexpansion",
		"--mutuallyexclusivemountoptions=readcache,writecache,coherent,forcedirect",
		"--mutuallyexclusivemountoptions=sync,async",
		"--mutuallyexclusivemountoptions=ro,rw",
		"--grpcrequesttimeoutseconds=30",
		"--healthprobewekatimeoutseconds=5",
		"--concurrency.createVolume=5",
		"--concurrency.deleteVolume=5",
		"--concurrency.expandVolume=5",
		"--concurrency.createSnapshot=5",
		"--concurrency.deleteSnapshot=5",
		"--nfsprotocolversion=4.1",
	}

	if settings.MetricsEnabled {
		args = append(args, "--enablemetrics", fmt.Sprintf("--metricsport=%d", ControllerMetricsPort))
	}

	if !enforceTrustedHttps {
		args = append(args, "--allowinsecurehttps")
	}
	if skipGarbageCollection {
		args = append(args, "--skipgarbagecollection")
	}

	if config.Config.Csi.SelinuxSupport == "enforced" {
		args = append(args, "--selinux-support")
	}

	kubeletPath := config.Config.Csi.KubeletPath

	tracingFlag := GetTracingFlag()
	if tracingFlag != "" {
		args = append(args, tracingFlag)
	}

	podAnnotations := map[string]string{
		"weka.io/csi-controller-hash": targetHash,
		// link the deployment to the client for easier identification of "owner"
		// NOTE: we cannot use owner references because the client and controller are in different namespaces
		"weka.io/csi-controller-owner":           string(wekaClient.GetUID()),
		"weka.io/csi-controller-owner-name":      wekaClient.Name,
		"weka.io/csi-controller-owner-namespace": wekaClient.Namespace,
	}

	wekafsPorts := []corev1.ContainerPort{
		{
			ContainerPort: 8081,
			Name:          "healthz",
			Protocol:      corev1.ProtocolTCP,
		},
	}

	attacherArgs := []string{
		"/csi-attacher",
		"--csi-address=$(ADDRESS)",
		"--v=$(LOG_LEVEL)",
		"--timeout=60s",
		"--worker-threads=5",
	}
	provisionerArgs := []string{
		"/csi-provisioner",
		"--v=$(LOG_LEVEL)",
		"--csi-address=$(ADDRESS)",
		"--feature-gates=Topology=true",
		"--extra-create-metadata=true",
		"--timeout=60s",
		"--prevent-volume-mode-conversion",
		"--worker-threads=5",
		"--retry-interval-start=10s",
	}
	resizerArgs := []string{
		"/csi-resizer",
		"--v=$(LOG_LEVEL)",
		"--csi-address=$(ADDRESS)",
		"--timeout=60s",
		"--workers=5",
		"--retry-interval-start=10s",
	}
	snapshotterArgs := []string{
		"/csi-snapshotter",
		"--v=$(LOG_LEVEL)",
		"--csi-address=$(ADDRESS)",
		"--timeout=60s",
		"--worker-threads=5",
		"--retry-interval-start=10s",
	}
	var attacherPorts, provisionerPorts, resizerPorts, snapshotterPorts []corev1.ContainerPort

	if settings.MetricsEnabled {
		// Under hostNetwork these bind host ports, so the ports, the scrape annotations and the
		// flags that open them are all gated together.
		podAnnotations["prometheus.io/scrape"] = "true"
		podAnnotations["prometheus.io/path"] = "/metrics"
		podAnnotations["prometheus.io/port"] = strings.Join([]string{
			strconv.Itoa(ControllerMetricsPort),
			strconv.Itoa(ProvisionerMetricsPort),
			strconv.Itoa(ResizerMetricsPort),
			strconv.Itoa(SnapshotterMetricsPort),
			strconv.Itoa(AttacherMetricsPort),
		}, ",")

		wekafsPorts = append(wekafsPorts, metricsContainerPort(ControllerMetricsPort, "metrics"))
		attacherArgs = append(attacherArgs, httpEndpointArg(AttacherMetricsPort))
		provisionerArgs = append(provisionerArgs, httpEndpointArg(ProvisionerMetricsPort))
		resizerArgs = append(resizerArgs, httpEndpointArg(ResizerMetricsPort))
		snapshotterArgs = append(snapshotterArgs, httpEndpointArg(SnapshotterMetricsPort))
		attacherPorts = []corev1.ContainerPort{metricsContainerPort(AttacherMetricsPort, "pr-metrics")}
		provisionerPorts = []corev1.ContainerPort{metricsContainerPort(ProvisionerMetricsPort, "pr-metrics")}
		resizerPorts = []corev1.ContainerPort{metricsContainerPort(ResizerMetricsPort, "rs-metrics")}
		snapshotterPorts = []corev1.ContainerPort{metricsContainerPort(SnapshotterMetricsPort, "sn-metrics")}
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
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					SecurityContext:    resources.GetSecurityProfile(),
					NodeSelector:       nodeSelector,
					HostNetwork:        config.Config.Csi.HostNetwork,
					ServiceAccountName: "csi-wekafs-controller-sa",
					PriorityClassName:  config.Config.PriorityClasses.Targeted,
					InitContainers: []corev1.Container{
						{
							Name:    "copy-wait-binary",
							Image:   config.Config.Csi.WekafsImage,
							Command: []string{"cp", "/wait-for-leader", "/shared/wait-for-leader"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "shared-bin",
									MountPath: "/shared",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name: "wekafs",
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Image:           config.Config.Csi.WekafsImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            args,
							Resources:       toK8sResourceRequirements(config.Config.Csi.ControllerResources.Wekafs),
							Ports:           wekafsPorts,
							LivenessProbe: &corev1.Probe{
								FailureThreshold:    10,
								InitialDelaySeconds: 10,
								TimeoutSeconds:      6,
								PeriodSeconds:       10,
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
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
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
								{
									Name:  "WEKAFS_CONTAINER_NAME",
									Value: wekaContainerName,
								},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{
									Name:  "HEALTH_PORT",
									Value: "8081",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/csi",
									Name:      "socket-dir",
								},
								{
									MountPath:        kubeletPath + "/pods",
									MountPropagation: (*corev1.MountPropagationMode)(ptr(string(corev1.MountPropagationBidirectional))),
									Name:             "mountpoint-dir",
								},
								{
									MountPath:        kubeletPath + "/plugins",
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
									MountPath: "/leader-state",
									Name:      "leader-state",
								},
							},
						},
						{
							Name:    "csi-attacher",
							Image:   config.Config.Csi.AttacherImage,
							Command: []string{"/shared/wait-for-leader"},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Resources: toK8sResourceRequirements(config.Config.Csi.ControllerResources.CsiAttacher),
							Args:      attacherArgs,
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
								{
									Name:      "leader-state",
									MountPath: "/leader-state",
									ReadOnly:  true,
								},
								{
									Name:      "shared-bin",
									MountPath: "/shared",
								},
							},
							Ports: attacherPorts,
						},
						{
							Name:      "csi-provisioner",
							Image:     config.Config.Csi.ProvisionerImage,
							Command:   []string{"/shared/wait-for-leader"},
							Resources: toK8sResourceRequirements(config.Config.Csi.ControllerResources.CsiProvisioner),
							Args:      provisionerArgs,
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
								{
									Name:      "leader-state",
									MountPath: "/leader-state",
									ReadOnly:  true,
								},
								{
									Name:      "shared-bin",
									MountPath: "/shared",
								},
							},
							Ports: provisionerPorts,
						},
						{
							Name:      "csi-resizer",
							Image:     config.Config.Csi.ResizerImage,
							Command:   []string{"/shared/wait-for-leader"},
							Resources: toK8sResourceRequirements(config.Config.Csi.ControllerResources.CsiResizer),
							Args:      resizerArgs,
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
								{
									Name:      "leader-state",
									MountPath: "/leader-state",
									ReadOnly:  true,
								},
								{
									Name:      "shared-bin",
									MountPath: "/shared",
								},
							},
							Ports: resizerPorts,
						},
						{
							Name:      "csi-snapshotter",
							Image:     config.Config.Csi.SnapshotterImage,
							Command:   []string{"/shared/wait-for-leader"},
							Resources: toK8sResourceRequirements(config.Config.Csi.ControllerResources.CsiSnapshotter),
							Args:      snapshotterArgs,
							Ports:     snapshotterPorts,
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "unix:///csi/csi.sock",
								},
								{
									Name:  "LOG_LEVEL",
									Value: strconv.Itoa(config.Config.Csi.LogLevel),
								},
							},
							ImagePullPolicy: corev1.PullIfNotPresent,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
								{
									Name:      "leader-state",
									MountPath: "/leader-state",
									ReadOnly:  true,
								},
								{
									Name:      "shared-bin",
									MountPath: "/shared",
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
									Path: kubeletPath + "/plugins/" + name,
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "mountpoint-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: kubeletPath + "/pods",
									Type: typePtr(corev1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: "registration-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: kubeletPath + "/plugins_registry",
									Type: typePtr(corev1.HostPathDirectory),
								},
							},
						},
						{
							Name: "plugins-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: kubeletPath + "/plugins",
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
						{
							Name: "leader-state",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "shared-bin",
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

// Helper function to create pointers to primitive types
func ptr(s string) *string {
	return &s
}

// Helper function for HostPathType
func typePtr(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}

// Helper function to convert config ResourceRequirements to Kubernetes ResourceRequirements
func toK8sResourceRequirements(res config.ResourceRequirements) corev1.ResourceRequirements {
	k8sRes := corev1.ResourceRequirements{}

	if res.Limits.CPU != "" || res.Limits.Memory != "" {
		k8sRes.Limits = corev1.ResourceList{}
		if res.Limits.CPU != "" {
			k8sRes.Limits[corev1.ResourceCPU] = resource.MustParse(res.Limits.CPU)
		}
		if res.Limits.Memory != "" {
			k8sRes.Limits[corev1.ResourceMemory] = resource.MustParse(res.Limits.Memory)
		}
	}

	if res.Requests.CPU != "" || res.Requests.Memory != "" {
		k8sRes.Requests = corev1.ResourceList{}
		if res.Requests.CPU != "" {
			k8sRes.Requests[corev1.ResourceCPU] = resource.MustParse(res.Requests.CPU)
		}
		if res.Requests.Memory != "" {
			k8sRes.Requests[corev1.ResourceMemory] = resource.MustParse(res.Requests.Memory)
		}
	}

	return k8sRes
}
