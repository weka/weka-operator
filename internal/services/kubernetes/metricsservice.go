package kubernetes

import (
	"context"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
)

type PodMetrics struct {
	CpuUsage      float64
	CpuRequest    float64
	CpuLimit      float64
	MemoryRequest int64
	MemoryLimit   int64
	MemoryUsage   int64
}

type KubeMetricsService interface {
	GetPodMetrics(ctx context.Context, pod *v1.Pod) (*PodMetrics, error)
}

type ApiKubeMetricsService struct {
	MetricsClient *metricsclientset.Clientset
}

func NewKubeMetricsServiceFromManager(manager ctrl.Manager) (KubeMetricsService, error) {
	metricsClient, err := metricsclientset.NewForConfig(manager.GetConfig())
	if err != nil {
		return nil, err
	}
	return &ApiKubeMetricsService{
		MetricsClient: metricsClient,
	}, nil
}

func (a *ApiKubeMetricsService) GetPodMetrics(ctx context.Context, pod *v1.Pod) (*PodMetrics, error) {
	// TODO: Should we be able to use manager client here instead? We are creating new client, but maybe can make original client for for metrics?
	c := a.MetricsClient.MetricsV1beta1()
	ret := &PodMetrics{}
	podMetrics, err := c.PodMetricses(pod.GetNamespace()).Get(ctx, pod.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	// TODO: We dont have here access to original requests? so need to use standard client as well
	// We can go another direction, and start from us having a pod here as well
	for _, container := range podMetrics.Containers {
		ret.CpuUsage += container.Usage.Cpu().AsApproximateFloat64()
		ret.MemoryUsage += container.Usage.Memory().Value()
	}

	for _, container := range pod.Spec.Containers {
		ret.CpuRequest += container.Resources.Requests.Cpu().AsApproximateFloat64()
		ret.CpuLimit += container.Resources.Limits.Cpu().AsApproximateFloat64()
		ret.MemoryRequest += container.Resources.Requests.Memory().Value()
		ret.MemoryLimit += container.Resources.Limits.Memory().Value()
	}
	return ret, nil
}
