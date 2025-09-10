package resources

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// GetImageForNodeAgent gets the operator image for node-agent containers
func GetImageForNodeAgent(container *weka.WekaContainer) string {
	// Priority order:
	// 1. NodeAgent.Image from values.yaml (NODE_AGENT_IMAGE env var)
	// 2. OperatorImage from manager.yaml (OPERATOR_IMAGE env var)
	// 3. Fallback to the same image as the triggering container
	if config.Config.NodeAgent.Image != "" {
		return config.Config.NodeAgent.Image
	}
	if config.Config.OperatorImage != "" {
		return config.Config.OperatorImage
	}
	return container.Spec.Image
}

type NodeAgentPodFactory struct {
	container *weka.WekaContainer
	nodeInfo  *discovery.DiscoveryNodeInfo
}

func NewNodeAgentPodFactory(container *weka.WekaContainer, nodeInfo *discovery.DiscoveryNodeInfo) *NodeAgentPodFactory {
	return &NodeAgentPodFactory{
		nodeInfo:  nodeInfo,
		container: container,
	}
}

func (f *NodeAgentPodFactory) Create(ctx context.Context) (*corev1.Pod, error) {
	nodeName := f.container.GetNodeAffinity()
	if nodeName == "" {
		return nil, fmt.Errorf("node affinity is not set")
	}

	labels := LabelsForWekaPod(f.container)
	annotations := AnnotationsForWekaPod(f.container.GetAnnotations(), nil)

	// Add specific labels and annotations for node-agent
	labels["app.kubernetes.io/component"] = "weka-node-agent"
	labels["app.kubernetes.io/instance"] = "weka-node-agent"
	labels["app.kubernetes.io/name"] = "weka-node-agent"
	labels["control-plane"] = "weka-node-agent"
	labels["app"] = "weka-node-agent"

	annotations["prometheus.io/scrape"] = "true"
	annotations["prometheus.io/port"] = "8090"
	annotations["prometheus.io/path"] = "/metrics"
	annotations["kubectl.kubernetes.io/default-container"] = "node-agent"

	image := GetImageForNodeAgent(f.container)
	if image == "" {
		return nil, fmt.Errorf("no image specified for node-agent container")
	}

	imagePullSecrets := []corev1.LocalObjectReference{}
	if f.container.Spec.ImagePullSecret != "" {
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{
			Name: f.container.Spec.ImagePullSecret,
		})
	}

	tolerations := f.getTolerations()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        f.container.Name,
			Namespace:   f.container.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			NodeName:                      string(nodeName),
			Tolerations:                   tolerations,
			ImagePullSecrets:              imagePullSecrets,
			TerminationGracePeriodSeconds: toPtr(int64(10)),
			HostNetwork:                   false,
			HostPID:                       false,
			DNSPolicy:                     corev1.DNSPolicy(config.Config.DNSPolicy.K8sNetwork),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: toPtr(false),
			},
			Containers: []corev1.Container{
				{
					Image:           image,
					Name:            "node-agent",
					ImagePullPolicy: corev1.PullAlways,
					Command:         []string{"/weka-operator"},
					SecurityContext: &corev1.SecurityContext{
						Privileged: toPtr(true),
					},
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 8090,
							Name:          "http",
							Protocol:      corev1.ProtocolTCP,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "tmpdir",
							MountPath: "/tmp",
						},
						{
							Name:      "weka-persistence",
							MountPath: "/host-binds/shared",
						},
						{
							Name:      "token",
							MountPath: "/var/run/secrets/kubernetes.io/token",
							ReadOnly:  true,
						},
					},
					Env: []corev1.EnvVar{
						{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							},
						},
						{
							Name: "POD_UID",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.uid",
								},
							},
						},
						{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
						{
							Name:  "OTEL_DEPLOYMENT_IDENTIFIER",
							Value: config.Config.Otel.DeploymentIdentifier,
						},
						{
							Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
							Value: config.Config.Otel.ExporterOtlpEndpoint,
						},
						{
							Name:  "VERSION",
							Value: config.Config.Version,
						},
						{
							Name:  "OPERATOR_MODE",
							Value: "node-agent",
						},
						{
							Name:  "NODE_AGENT_BIND_ADDRESS",
							Value: ":8090",
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(config.Config.NodeAgent.Resources.Limits.CPU),
							corev1.ResourceMemory: resource.MustParse(config.Config.NodeAgent.Resources.Limits.Memory),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(config.Config.NodeAgent.Resources.Requests.CPU),
							corev1.ResourceMemory: resource.MustParse(config.Config.NodeAgent.Resources.Requests.Memory),
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "tmpdir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "token",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "weka-node-agent-secret",
						},
					},
				},
				{
					Name: "weka-persistence",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: fmt.Sprintf("%s/shared", config.Config.NodeAgent.PersistencePaths),
						},
					},
				},
			},
			ServiceAccountName: config.Config.MaintenanceSaName,
		},
	}

	return pod, nil
}

func (f *NodeAgentPodFactory) getTolerations() []corev1.Toleration {
	tolerations := []corev1.Toleration{}

	if !config.Config.SkipUnhealthyToleration {
		tolerations = append(tolerations, getUnhealthyTolerations()...)
	}

	pressureTolerations := getPressureTolerations()
	tolerations = append(tolerations, pressureTolerations...)

	tolerations = append(tolerations, f.container.Spec.Tolerations...)
	return tolerations
}

func toPtr[T any](v T) *T {
	return &v
}
