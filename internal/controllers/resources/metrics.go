package resources

import (
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
	"strings"
)

func BuildClusterPrometheusMetrics(cluster *v1alpha1.WekaCluster) (string, error) {
	metrics := []metrics2.PromMetric{}

	commonTags := map[string]string{
		"cluster_name": cluster.Name,
		"namespace":    cluster.Namespace,
		"cluster_guid": cluster.Status.ClusterID,
	}

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_throughput",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Metrics.IoStats.Throughput.Read.Value)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Metrics.IoStats.Throughput.Write.Value)},
		},
		Timestamp: cluster.Status.Metrics.IoStats.Throughput.Read.Time.Time,
		Help:      "Weka clusters throughput",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_iops",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Metrics.IoStats.Iops.Read.Value)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Metrics.IoStats.Iops.Write.Value)},
			{Tags: metrics2.TagMap{"type": "metadata"}, Value: float64(cluster.Status.Metrics.IoStats.Iops.Metadata.Value)},
		},
		Timestamp: cluster.Status.Metrics.IoStats.Iops.Read.Time.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_drives",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "desired"}, Value: float64(cluster.Status.Metrics.Drives.DriveCounters.Desired.Value)},
			{Tags: metrics2.TagMap{"type": "active"}, Value: float64(cluster.Status.Metrics.Drives.DriveCounters.Active.Value)},
			{Tags: metrics2.TagMap{"type": "created"}, Value: float64(cluster.Status.Metrics.Drives.DriveCounters.Created.Value)},
		},
		Timestamp: cluster.Status.Metrics.Drives.DriveCounters.Active.Time.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_containers",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "compute", "status": "active"}, Value: float64(cluster.Status.Metrics.Containers.Compute.Containers.Active.Value)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "active"}, Value: float64(cluster.Status.Metrics.Containers.Drive.Containers.Active.Value)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "desired"}, Value: float64(cluster.Status.Metrics.Containers.Compute.Containers.Desired.Value)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "desired"}, Value: float64(cluster.Status.Metrics.Containers.Drive.Containers.Desired.Value)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "created"}, Value: float64(cluster.Status.Metrics.Containers.Compute.Containers.Created.Value)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "created"}, Value: float64(cluster.Status.Metrics.Containers.Drive.Containers.Created.Value)},
		},
		Timestamp: cluster.Status.Metrics.Containers.Compute.Containers.Active.Time.Time,
	})
	// TODO: Conditionally add s3/nfs

	for i, _ := range metrics {
		metrics[i].Tags = commonTags
	}

	retBuffer := strings.Builder{}
	for _, metric := range metrics {
		promString := metric.AsPrometheusString(commonTags)
		if promString != nil {
			retBuffer.WriteString(*promString)
			retBuffer.WriteString("\n")
		}
	}

	return retBuffer.String(), nil
}
