package csi

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
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

// mountProtocolArgs renders the transport flags shared by the controller and the node plugin.
//
// Only --usenfs: the plugin's own defaults cover the rest - it picks an interface group, creates
// its client group and negotiates the NFS version - and NFS access is a cluster prerequisite
// rather than something this operator configures.
func mountProtocolArgs(p *DeploymentParams) []string {
	if !p.ForceNfs {
		return nil
	}
	return []string{"--usenfs"}
}

// csiHostNetwork resolves the effective hostNetwork of the CSI pods. The operator-wide
// CSI_HOST_NETWORK opts in explicitly; an NFS transport requires it regardless, since the mount
// and the client-group registration both depend on the node's own network namespace.
func csiHostNetwork(p *DeploymentParams) bool {
	return config.Config.Csi.HostNetwork || p.ForceNfs
}
