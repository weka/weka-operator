package csi

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// Ports the CSI plugin and its sidecars bind for Prometheus metrics. Under hostNetwork these are
// host ports, which is why metrics can be turned off wholesale.
const (
	ControllerMetricsPort  = 9090
	ProvisionerMetricsPort = 9091
	ResizerMetricsPort     = 9092
	SnapshotterMetricsPort = 9093
	NodeMetricsPort        = 9094
	AttacherMetricsPort    = 9095
)

// httpEndpointArg is the metrics listener flag of the controller sidecars.
func httpEndpointArg(port int) string {
	return fmt.Sprintf("--http-endpoint=:%d", port)
}

func metricsContainerPort(port int, name string) corev1.ContainerPort {
	return corev1.ContainerPort{
		ContainerPort: int32(port),
		Name:          name,
		Protocol:      corev1.ProtocolTCP,
	}
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

// csiHostNetwork resolves the effective hostNetwork of the CSI pods. The operator-wide
// CSI_HOST_NETWORK opts in explicitly; an NFS transport requires it regardless, since the mount
// and the client-group registration both depend on the node's own network namespace.
func csiHostNetwork(wekaClient *weka.WekaClient) bool {
	return config.Config.Csi.HostNetwork || wekaClient.Spec.UseNfs
}

// controllerAntiAffinity spreads the controller replicas across nodes when UseNfs puts them on
// hostNetwork; co-located replicas would bind the same healthz/metrics host ports.
func controllerAntiAffinity(wekaClient *weka.WekaClient, podLabels map[string]string) *corev1.Affinity {
	if !wekaClient.Spec.UseNfs {
		return nil
	}
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: podLabels},
				TopologyKey:   "kubernetes.io/hostname",
			}},
		},
	}
}
