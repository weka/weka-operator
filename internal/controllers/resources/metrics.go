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
			{metrics2.TagMap{"type": "read"}, float64(cluster.Status.Metrics.IoStats.Throughput.Read.Value)},
			{metrics2.TagMap{"type": "write"}, float64(cluster.Status.Metrics.IoStats.Throughput.Write.Value)},
		},
		Timestamp: cluster.Status.Metrics.IoStats.Throughput.Read.Time.Time,
		Help:      "Weka clusters throughput",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_iops",
		ValuesByTags: []metrics2.TaggedValue{
			{metrics2.TagMap{"type": "read"}, float64(cluster.Status.Metrics.IoStats.Iops.Read.Value)},
			{metrics2.TagMap{"type": "write"}, float64(cluster.Status.Metrics.IoStats.Iops.Write.Value)},
			{metrics2.TagMap{"type": "metadata"}, float64(cluster.Status.Metrics.IoStats.Iops.Metadata.Value)},
		},
		Timestamp: cluster.Status.Metrics.IoStats.Iops.Read.Time.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_num_drives",
		ValuesByTags: []metrics2.TaggedValue{
			{metrics2.TagMap{"type": "desired"}, float64(cluster.Status.Metrics.Drives.DriveCounters.Desired.Value)},
			{metrics2.TagMap{"type": "active"}, float64(cluster.Status.Metrics.Drives.DriveCounters.Active.Value)},
			{metrics2.TagMap{"type": "created"}, float64(cluster.Status.Metrics.Drives.DriveCounters.Created.Value)},
		},
		Timestamp: cluster.Status.Metrics.Drives.DriveCounters.Active.Time.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_num_containers",
		ValuesByTags: []metrics2.TaggedValue{
			{metrics2.TagMap{"type": "compute", "status": "active"}, float64(cluster.Status.Metrics.Containers.Compute.Containers.Active.Value)},
			{metrics2.TagMap{"type": "drive", "status": "active"}, float64(cluster.Status.Metrics.Containers.Drive.Containers.Active.Value)},
			{metrics2.TagMap{"type": "compute", "status": "desired"}, float64(cluster.Status.Metrics.Containers.Compute.Containers.Desired.Value)},
			{metrics2.TagMap{"type": "drive", "status": "desired"}, float64(cluster.Status.Metrics.Containers.Drive.Containers.Desired.Value)},
			{metrics2.TagMap{"type": "compute", "status": "created"}, float64(cluster.Status.Metrics.Containers.Compute.Containers.Created.Value)},
			{metrics2.TagMap{"type": "drive", "status": "created"}, float64(cluster.Status.Metrics.Containers.Drive.Containers.Created.Value)},
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
