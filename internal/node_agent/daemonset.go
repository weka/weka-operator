package node_agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	daemonSetHashAnnotation = "weka.io/node-agent-hash"
	daemonSetEnsureInterval = 30 * time.Second
	nodeAgentContainerName  = "node-agent"
	nodeAgentPort           = 8090
)

// URL is the node-agent endpoint for path on the agent pod with the given IP.
func URL(podIP, path string) string {
	return "http://" + net.JoinHostPort(podIP, strconv.Itoa(nodeAgentPort)) + path
}

// NewNodeAgentDaemonSet builds the node-agent DaemonSet for the given settings, owned by the operator
// deployment.
func NewNodeAgentDaemonSet(settings *services.NodeAgentSettings, operatorDeployment *appsv1.Deployment) *appsv1.DaemonSet {
	podLabels := map[string]string{
		"control-plane":                "weka-node-agent",
		"app.kubernetes.io/created-by": "weka-operator",
		"app.kubernetes.io/component":  "weka-node-agent",
		"app":                          "weka-node-agent",
	}

	volumes := []corev1.Volume{
		{Name: "tmpdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: config.Config.Metrics.NodeAgentSecretName}}},
		hostPathVolume("weka-persistence", settings.PersistencePath+"/shared", nil),
		hostPathVolume("dev", "/dev", nil),
		// udev data under /run is how findDrives reads drive serial IDs for SCSI devices
		hostPathVolume("host-run", "/run", nil),
	}
	mounts := []corev1.VolumeMount{
		{Name: "tmpdir", MountPath: "/tmp"},
		{Name: "weka-persistence", MountPath: "/host-binds/shared"},
		{Name: "token", MountPath: "/var/run/secrets/kubernetes.io/token", ReadOnly: true},
		{Name: "dev", MountPath: "/dev"},
		{Name: "host-run", MountPath: "/host/run", ReadOnly: true},
	}
	if settings.DevicePluginEnabled {
		dirOrCreate := corev1.HostPathDirectoryOrCreate
		volumes = append(volumes, hostPathVolume("device-plugins", settings.KubeletPath+"/device-plugins", &dirOrCreate))
		mounts = append(mounts, corev1.VolumeMount{Name: "device-plugins", MountPath: "/var/lib/kubelet/device-plugins"})
	}

	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: fieldRef("metadata.name")},
		{Name: "POD_UID", ValueFrom: fieldRef("metadata.uid")},
		{Name: "NODE_NAME", ValueFrom: fieldRef("spec.nodeName")},
		{Name: "OTEL_DEPLOYMENT_IDENTIFIER", Value: config.Config.Otel.DeploymentIdentifier},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: config.Config.Otel.ExporterOtlpEndpoint},
		{Name: "VERSION", Value: config.Config.Version},
		{Name: "OPERATOR_MODE", Value: string(config.OperatorModeNodeAgent)},
		{Name: "NODE_AGENT_BIND_ADDRESS", Value: fmt.Sprintf(":%d", nodeAgentPort)},
		{Name: "LOG_LEVEL", Value: os.Getenv("LOG_LEVEL")},
		{Name: "LOG_FORMAT", Value: os.Getenv("LOG_FORMAT")},
		{Name: "LOG_CALLER_DIR_LVL", Value: os.Getenv("LOG_CALLER_DIR_LVL")},
		{Name: "NODE_AGENT_DEVICE_PLUGIN_ENABLED", Value: fmt.Sprintf("%t", settings.DevicePluginEnabled)},
	}

	privileged := true
	runAsNonRoot := false
	securityContext := resources.GetSecurityProfile()
	if securityContext == nil {
		securityContext = &corev1.PodSecurityContext{}
	}
	if securityContext.RunAsNonRoot == nil {
		securityContext.RunAsNonRoot = &runAsNonRoot
	}
	gracePeriod := int64(10)
	affinity := settings.Affinity

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: config.Config.NodeAgentDaemonSetName,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "weka-node-agent",
				"app.kubernetes.io/created-by": "weka-operator",
				"app.kubernetes.io/instance":   "weka-node-agent",
				"app.kubernetes.io/name":       "weka-node-agent",
				"app.kubernetes.io/part-of":    "weka-operator",
				"control-plane":                "node-agent",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: settings.UpdateStrategy,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"control-plane":               "weka-node-agent",
				"app.kubernetes.io/component": "weka-node-agent",
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						"prometheus.io/scrape":                    "true",
						"prometheus.io/port":                      fmt.Sprintf("%d", nodeAgentPort),
						"prometheus.io/path":                      "/metrics",
						"kubectl.kubernetes.io/default-container": nodeAgentContainerName,
					},
				},
				Spec: corev1.PodSpec{
					SecurityContext:               securityContext,
					ImagePullSecrets:              operatorDeployment.Spec.Template.Spec.ImagePullSecrets,
					NodeSelector:                  settings.NodeSelector,
					Affinity:                      &affinity,
					Tolerations:                   settings.Tolerations,
					DNSPolicy:                     corev1.DNSPolicy(config.Config.DNSPolicy.K8sNetwork),
					PriorityClassName:             config.Config.PriorityClasses.Targeted,
					Volumes:                       volumes,
					ServiceAccountName:            config.Config.MaintenanceSaName,
					TerminationGracePeriodSeconds: &gracePeriod,
					Containers: []corev1.Container{{
						Name:            nodeAgentContainerName,
						Image:           config.Config.OperatorImage,
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{"/weka-operator"},
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: nodeAgentPort, Protocol: corev1.ProtocolTCP}},
						Env:             env,
						VolumeMounts:    mounts,
						SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
						Resources:       settings.Resources,
					}},
				},
			},
		},
	}

	return ds
}

func hostPathVolume(name, path string, hostPathType *corev1.HostPathType) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path, Type: hostPathType}}}
}

func fieldRef(path string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: path}}
}

// daemonSetHash covers the DaemonSet spec only, so a change confined to metadata is not an update.
func daemonSetHash(ds *appsv1.DaemonSet) (string, error) {
	specJSON, err := json.Marshal(ds.Spec)
	if err != nil {
		return "", err
	}
	return util.GetHash(string(specJSON), 16), nil
}

// EnsureNodeAgentDaemonSet creates the node-agent DaemonSet, or updates it when its spec differs from the
// one derived from the current settings.
func EnsureNodeAgentDaemonSet(ctx context.Context, c client.Client) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureNodeAgentDaemonSet")
	defer logger.End()

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return err
	}
	operatorDeployment, err := util.GetOperatorDeployment(ctx, c)
	if err != nil {
		return err
	}

	nodeAgentSettings := services.GetSettings(ctx).NodeAgent
	// Create/Update decode the API response into target; the copy keeps that out of the shared settings.
	target := NewNodeAgentDaemonSet(&nodeAgentSettings, operatorDeployment).DeepCopy()
	target.Namespace = namespace
	hash, err := daemonSetHash(target)
	if err != nil {
		return errors.Wrap(err, "failed to hash node-agent daemonset")
	}
	if target.Annotations == nil {
		target.Annotations = map[string]string{}
	}
	target.Annotations[daemonSetHashAnnotation] = hash
	err = controllerutil.SetControllerReference(operatorDeployment, target, c.Scheme())
	if err != nil {
		return err
	}

	existing := &appsv1.DaemonSet{}
	err = c.Get(ctx, client.ObjectKeyFromObject(target), existing)
	if apierrors.IsNotFound(err) {
		logger.Info("Creating node-agent daemonset", "name", target.Name)
		return c.Create(ctx, target)
	}
	if err != nil {
		return errors.Wrap(err, "failed to get node-agent daemonset")
	}
	if existing.Annotations[daemonSetHashAnnotation] == hash {
		return nil
	}

	logger.Info("Node-agent daemonset spec changed, updating", "name", target.Name, "hash", hash)
	keepUnownedMetadata(existing, target)
	target.ResourceVersion = existing.ResourceVersion
	return c.Update(ctx, target)
}

// keepUnownedMetadata carries over keys other tools set on the live object: dropping a pod-template label or
// annotation such as kubectl's restartedAt would restart every pod on an otherwise metadata-free update.
func keepUnownedMetadata(existing, target *appsv1.DaemonSet) {
	target.Labels = util.MergeMaps(existing.Labels, target.Labels)
	target.Annotations = util.MergeMaps(existing.Annotations, target.Annotations)
	target.Spec.Template.Labels = util.MergeMaps(existing.Spec.Template.Labels, target.Spec.Template.Labels)
	target.Spec.Template.Annotations = util.MergeMaps(existing.Spec.Template.Annotations, target.Spec.Template.Annotations)
}

// RunNodeAgentDaemonSetLoop keeps the node-agent DaemonSet converged until ctx is done. A failed tick is
// retried on the next one, which also covers losing a create race to another manager replica.
func RunNodeAgentDaemonSetLoop(ctx context.Context, c client.Client) {
	ticker := time.NewTicker(daemonSetEnsureInterval)
	defer ticker.Stop()
	for {
		ensureNodeAgentDaemonSetRecovering(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ensureNodeAgentDaemonSetRecovering turns the panic services.GetSettings raises on an unresolvable
// configuration into a logged skip, since a panic in this goroutine would take the manager down.
func ensureNodeAgentDaemonSetRecovering(ctx context.Context, c client.Client) {
	_, logger := instrumentation.CreateLogSpan(ctx, "EnsureNodeAgentDaemonSetTick")
	defer logger.End()
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Errorf("%v", r), "Skipping node-agent daemonset reconcile", "stack", string(debug.Stack()))
		}
	}()
	if err := EnsureNodeAgentDaemonSet(ctx, c); err != nil {
		logger.Error(err, "Failed to ensure node-agent daemonset")
	}
}
