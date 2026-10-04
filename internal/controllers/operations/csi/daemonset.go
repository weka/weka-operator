package csi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	util2 "github.com/weka/weka-operator/pkg/util"
)

// CsiNodeHashableSpec represents the fields from CSI Node DaemonSet
// that are relevant for determining if an update is needed
type CsiNodeHashableSpec struct {
	CsiDriverName             string
	ClientName                string
	ClientNamespace           string
	CsiImage                  string
	CsiRegistrarImage         string
	CsiLivenessProbeImage     string
	Labels                    *util2.HashableMap
	Tolerations               []corev1.Toleration
	NodeSelector              *util2.HashableMap
	EnforceTrustedHttps       bool
	AllowMountOptionOverrides bool
	LogLevel                  int
	PriorityClassName         string
	SelinuxSupport            string
	KubeletPath               string
	HostNetwork               bool
	PlacementScheme           string
	MetricsEnabled            bool
}

// csiNodePlacementScheme identifies how csi-node placement is expressed in the rendered pod spec.
// It is part of the hashable spec so that upgrading from an operator that expressed placement
// differently rolls existing DaemonSets exactly once, even when nothing else about the client
// changed.
const csiNodePlacementScheme = "node-affinity-with-retain-v1"

// GetCsiNodeDaemonSetHash generates a hash for the CSI Node DaemonSet
// that includes only the fields that are relevant for updates
func GetCsiNodeDaemonSetHash(p *DeploymentParams, settings services.CsiSettings) (string, error) {
	csiDriverName := p.CsiDriverName()
	// CSI node plugins are infrastructure components and must run on all nodes
	tolerations := []corev1.Toleration{
		{
			Operator: corev1.TolerationOpExists, // Tolerates all taints
		},
	}

	var csiLabels map[string]string
	var enforceTrustedHttps bool

	if p.Advanced != nil {
		tolerations = append(tolerations, p.Advanced.NodeTolerations...)
		csiLabels = p.Advanced.NodeLabels
		enforceTrustedHttps = p.Advanced.EnforceTrustedHttps
	}

	// Get the complete labels that would be applied to the daemonset
	labels := GetCsiLabels(csiDriverName, CSINode, p.OwnerLabels, csiLabels)

	// Convert maps to HashableMap for consistent hashing
	labelsHashable := util2.NewHashableMap(labels)
	var nodeSelectorHashable *util2.HashableMap
	if p.NodeSelector != nil {
		nodeSelectorHashable = util2.NewHashableMap(p.NodeSelector)
	}

	spec := CsiNodeHashableSpec{
		CsiDriverName:             csiDriverName,
		ClientName:                p.OwnerName,
		ClientNamespace:           p.OwnerNamespace,
		CsiImage:                  config.Config.Csi.WekafsImage,
		CsiRegistrarImage:         config.Config.Csi.RegistrarImage,
		CsiLivenessProbeImage:     config.Config.Csi.LivenessProbeImage,
		Labels:                    labelsHashable,
		Tolerations:               tolerations,
		NodeSelector:              nodeSelectorHashable,
		EnforceTrustedHttps:       enforceTrustedHttps,
		AllowMountOptionOverrides: config.Config.Csi.AllowMountOptionOverrides,
		LogLevel:                  config.Config.Csi.LogLevel,
		PriorityClassName:         config.Config.PriorityClasses.Targeted,
		SelinuxSupport:            config.Config.Csi.SelinuxSupport,
		KubeletPath:               config.Config.Csi.KubeletPath,
		HostNetwork:               csiHostNetwork(p),
		PlacementScheme:           csiNodePlacementScheme,
		MetricsEnabled:            settings.MetricsEnabled,
	}

	return util2.HashStruct(spec)
}

func GetCSINodeDaemonSetName(csiGroupName string) string {
	return strings.ReplaceAll(csiGroupName, ".", "-") + "-weka-csi-node"
}

// wekafsContainerNameEnv renders the WEKAFS_CONTAINER_NAME env var, or nothing when there is no
// weka client container on these nodes. It must stay in step with the --wekafscontainername flag:
// the flag's value is $(WEKAFS_CONTAINER_NAME), and Kubernetes leaves that literal text in argv
// when the variable is undefined.
func wekafsContainerNameEnv(name string) []corev1.EnvVar {
	if name == "" {
		return nil
	}
	return []corev1.EnvVar{{Name: "WEKAFS_CONTAINER_NAME", Value: name}}
}

func GetCSINodeDaemonSetNameForClient(csiGroupName, clientName, clientNamespace string) string {
	base := strings.ReplaceAll(csiGroupName, ".", "-") + "-csi-node-" + clientNamespace + "-" + strings.ReplaceAll(clientName, ".", "-")
	if len(base) > 63 {
		// Use hash suffix to preserve uniqueness when truncating
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(base)))[:8]
		base = base[:63-9] + "-" + hash
	}
	// Ensure name doesn't end with a hyphen
	base = strings.TrimRight(base, "-")
	return base
}

// buildCsiNodeAffinity renders csi-node placement as node affinity.
//
// Two terms, which Kubernetes ORs together: the client's own node selector, and CsiNodeRetainLabel.
// The retain term is what keeps the plugin on a node whose client-selector label was just removed but
// which may still hold weka mounts. Without it the DaemonSet controller deschedules the only thing
// able to serve NodeUnpublishVolume, and the client container can then never finish draining.
//
// An empty selector keeps its existing "run everywhere" meaning by rendering no affinity at all: a
// nodeSelectorTerm with no matchExpressions is not valid, and nothing ever deschedules the plugin in
// that configuration, so a retain term would be pointless.
//
// An empty retainLabel renders only the selector term. That is the standalone case: with no weka
// client containers there are no mounts to drain and nobody to release the label, and a term
// keyed on "" would match nothing while still widening the OR.
func buildCsiNodeAffinity(nodeSelector map[string]string, retainLabel string) *corev1.Affinity {
	if len(nodeSelector) == 0 {
		return nil
	}

	// Sorted deliberately: this is rendered into a spec that gets hashed to decide whether to roll the
	// DaemonSet, and Go map iteration order is random.
	keys := make([]string, 0, len(nodeSelector))
	for key := range nodeSelector {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	selectorExpressions := make([]corev1.NodeSelectorRequirement, 0, len(keys))
	for _, key := range keys {
		selectorExpressions = append(selectorExpressions, corev1.NodeSelectorRequirement{
			Key:      key,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{nodeSelector[key]},
		})
	}

	terms := []corev1.NodeSelectorTerm{{MatchExpressions: selectorExpressions}}
	if retainLabel != "" {
		terms = append(terms, corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      retainLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{CsiNodeRetainLabelValue},
				},
			},
		})
	}

	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: terms,
			},
		},
	}
}

func NewCsiNodeDaemonSet(ctx context.Context, p *DeploymentParams, nodes []corev1.Node, settings services.CsiSettings) (*appsv1.DaemonSet, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "NewCsiNodeDaemonSet")
	defer logger.End()

	name := p.NodeDaemonSetName
	csiDriverName := p.CsiDriverName()
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
	if p.Advanced != nil {
		tolerations = append(tolerations, p.Advanced.NodeTolerations...)
		csiLabels = p.Advanced.NodeLabels
		enforceTrustedHttps = p.Advanced.EnforceTrustedHttps
	}
	labels := GetCsiLabels(csiDriverName, CSINode, p.OwnerLabels, csiLabels)

	targetHash, err := GetCsiNodeDaemonSetHash(p, settings)
	if err != nil {
		logger.Error(err, "Failed to get CSI node daemonset hash")
		return nil, fmt.Errorf("failed to get CSI node daemonset hash: %w", err)
	}

	nodeSelector := p.NodeSelector
	namespace, _ := util2.GetPodNamespace() //nolint:errcheck // namespace used for object metadata only; failure falls back to empty string

	privileged := true

	args := []string{
		"--v=$(LOG_LEVEL)",
		"--drivername=$(CSI_DRIVER_NAME)",
		"--endpoint=$(CSI_ENDPOINT)",
		"--nodeid=$(KUBE_NODE_NAME)",
		"--dynamic-path=$(CSI_DYNAMIC_PATH)",
		"--csimode=$(X_CSI_MODE)",
		"--newvolumeprefix=csivol-",
		"--newsnapshotprefix=csisnp-",
		"--seedsnapshotprefix=csisnp-seed-",
		"--mutuallyexclusivemountoptions=readcache,writecache,coherent,forcedirect",
		"--mutuallyexclusivemountoptions=sync,async",
		"--mutuallyexclusivemountoptions=ro,rw",
		"--grpcrequesttimeoutseconds=30",
		"--healthprobewekatimeoutseconds=5",
		"--concurrency.nodePublishVolume=5",
		"--concurrency.nodeUnpublishVolume=5",
		"--nfsprotocolversion=4.1",
	}

	if settings.MetricsEnabled {
		args = append(args, "--enablemetrics", fmt.Sprintf("--metricsport=%d", NodeMetricsPort))
	}

	args = append(args, mountProtocolArgs(p)...)

	// Gated together with the WEKAFS_CONTAINER_NAME env var below: the flag's value is
	// $(WEKAFS_CONTAINER_NAME), so rendering the flag without the env var leaves that literal
	// string in argv and the plugin reads it as a container name.
	if p.WekafsContainerName != "" {
		args = append(args, "--wekafscontainername=$(WEKAFS_CONTAINER_NAME)")
	}

	// Hand the plugin its own topology.<driver>/* node labels. Only where no weka client container
	// exists to derive them from — the operator stamps them otherwise, and two writers would flap.
	if p.ManageNodeTopologyLabels {
		args = append(args, "--managenodetopologylabels")
	}

	if !enforceTrustedHttps {
		args = append(args, "--allowinsecurehttps")
	}

	if config.Config.Csi.AllowMountOptionOverrides {
		args = append(args, "--allowmountoptionoverrides")
	}

	tracingFlag := GetTracingFlag()
	if tracingFlag != "" {
		args = append(args, tracingFlag)
	}

	wekaContainerName := p.WekafsContainerName

	var selinuxEnabled bool
	switch config.Config.Csi.SelinuxSupport {
	case "enforced":
		selinuxEnabled = true
	case "off":
		selinuxEnabled = false
	default: // "auto"
		selinuxEnabled = discovery.AnyNodeHasSelinux(nodes)
	}

	if selinuxEnabled {
		args = append(args, "--selinux-support")
	}
	kubeletPath := config.Config.Csi.KubeletPath

	wekafsVolumeMounts := []corev1.VolumeMount{
		{MountPath: "/csi", Name: "socket-dir"},
		{MountPath: kubeletPath + "/pods", MountPropagation: (*corev1.MountPropagationMode)(ptr(string(corev1.MountPropagationBidirectional))), Name: "mountpoint-dir"},
		{MountPath: kubeletPath + "/plugins", MountPropagation: (*corev1.MountPropagationMode)(ptr(string(corev1.MountPropagationBidirectional))), Name: "plugins-dir"},
		{MountPath: "/var/lib/csi-wekafs-data", Name: "csi-data-dir"},
		{MountPath: "/dev", Name: "dev-dir"},
		{MountPath: "/etc/nodeinfo", Name: "nodeinfo", ReadOnly: true},
	}
	if selinuxEnabled {
		wekafsVolumeMounts = append(wekafsVolumeMounts,
			corev1.VolumeMount{
				Name:      "selinux-config",
				MountPath: "/etc/selinux/config",
				ReadOnly:  true,
			},
			// selinuxfs has to be writable: applying an fscontext= mount option canonicalizes the
			// context by writing to /sys/fs/selinux/context. Mounted read-only that write returns
			// EROFS, the mount then succeeds with fscontext= absent from the resulting superblock,
			// and volumes come up unlabeled_t instead of wekafs_t.
			corev1.VolumeMount{
				Name:      "selinux-fs",
				MountPath: "/sys/fs/selinux",
			},
		)
	}

	volumes := []corev1.Volume{
		{Name: "mountpoint-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: kubeletPath + "/pods", Type: typePtr(corev1.HostPathDirectoryOrCreate)}}},
		{Name: "registration-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: kubeletPath + "/plugins_registry", Type: typePtr(corev1.HostPathDirectory)}}},
		{Name: "plugins-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: kubeletPath + "/plugins", Type: typePtr(corev1.HostPathDirectory)}}},
		{Name: "socket-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: kubeletPath + "/plugins/" + name, Type: typePtr(corev1.HostPathDirectoryOrCreate)}}},
		{Name: "csi-data-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/csi-wekafs-data/", Type: typePtr(corev1.HostPathDirectoryOrCreate)}}},
		{Name: "dev-dir", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev", Type: typePtr(corev1.HostPathDirectory)}}},
		{Name: "nodeinfo", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if selinuxEnabled {
		volumes = append(volumes,
			corev1.Volume{
				Name: "selinux-config",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/etc/selinux/config",
						Type: typePtr(corev1.HostPathFileOrCreate),
					},
				},
			},
			corev1.Volume{
				Name: "selinux-fs",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/sys/fs/selinux",
						Type: typePtr(corev1.HostPathDirectory),
					},
				},
			},
		)
	}

	podAnnotations := map[string]string{
		"weka.io/csi-node-hash": targetHash,
		// link the daemonset to the client for easier identification of "owner"
		// NOTE: we cannot use owner references because the client and node are in different namespaces
		"weka.io/csi-node-owner":           p.OwnerUID,
		"weka.io/csi-node-owner-name":      p.OwnerName,
		"weka.io/csi-node-owner-namespace": p.OwnerNamespace,
	}

	wekafsPorts := []corev1.ContainerPort{
		{
			ContainerPort: 9899,
			Name:          "healthz",
			Protocol:      corev1.ProtocolTCP,
		},
	}

	if settings.MetricsEnabled {
		// Under hostNetwork this binds a host port, so the port, the scrape annotations and the
		// flags that open them are all gated together.
		podAnnotations["prometheus.io/scrape"] = "true"
		podAnnotations["prometheus.io/path"] = "/metrics"
		podAnnotations["prometheus.io/port"] = strconv.Itoa(NodeMetricsPort)
		wekafsPorts = append(wekafsPorts, metricsContainerPort(NodeMetricsPort, "metrics"))
	}

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
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					SecurityContext:    resources.GetSecurityProfile(),
					Affinity:           buildCsiNodeAffinity(nodeSelector, p.RetainLabel),
					HostNetwork:        csiHostNetwork(p),
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
							Ports:           wekafsPorts,
							LivenessProbe: &corev1.Probe{
								FailureThreshold: 10,
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromString("healthz"),
									},
								},
								InitialDelaySeconds: 10,
								TimeoutSeconds:      7,
								PeriodSeconds:       10,
							},
							Env: append([]corev1.EnvVar{
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
							}, wekafsContainerNameEnv(wekaContainerName)...),
							VolumeMounts: wekafsVolumeMounts,
						},
						{
							Name:      "liveness-probe",
							Image:     config.Config.Csi.LivenessProbeImage,
							Resources: toK8sResourceRequirements(config.Config.Csi.NodeResources.LivenessProbe),
							Args: []string{
								"--v=$(LOG_LEVEL)",
								"--csi-address=$(ADDRESS)",
								"--health-port=$(HEALTH_PORT)",
								"--probe-timeout=6s",
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
									Value: fmt.Sprintf("%s/plugins/%s/csi.sock", kubeletPath, name),
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
					Volumes:     volumes,
				},
			},
		},
	}, nil
}
