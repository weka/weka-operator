package csi

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
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
