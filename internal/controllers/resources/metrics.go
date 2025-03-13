package resources

import (
	"fmt"
	"github.com/pkg/errors"
	"slices"
	"strings"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
)

func BuildClusterPrometheusMetrics(cluster *v1alpha1.WekaCluster) (string, error) {
	metrics := []metrics2.PromMetric{}

	commonTags := map[string]string{
		"cluster_name": cluster.Name,
		"namespace":    cluster.Namespace,
		"cluster_guid": cluster.Status.ClusterID,
	}

	if cluster.Status.Stats == nil {
		return "", errors.New("cluster stats are not available")
	}

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_throughput",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Stats.IoStats.Throughput.Read)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Stats.IoStats.Throughput.Write)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka clusters throughput",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_iops",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Read)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Write)},
			{Tags: metrics2.TagMap{"type": "metadata"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Metadata)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_drives",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"status": "desired"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Desired)},
			{Tags: metrics2.TagMap{"status": "active"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Active)},
			{Tags: metrics2.TagMap{"status": "created"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Created)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_containers",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "compute", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Active)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Active)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Desired)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Desired)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Created)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Created)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
	})
	// TODO: Conditionally add s3/nfs

	val := float64(cluster.Status.Stats.AlertsCount)
	metrics = append(metrics, metrics2.PromMetric{
		Metric:       "weka_alerts_count",
		ValuesByTags: []metrics2.TaggedValue{},
		Value:        &val,
	})

	if slices.Contains([]string{
		"OK",
		"REDISTRIBUTING",
		"REBUILDING",
	}, string(cluster.Status.Stats.ClusterStatus)) {
		val = 1.0
	} else {
		val = 0.0
	}
	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_status",
		Tags: metrics2.TagMap{
			"status": string(cluster.Status.Stats.ClusterStatus),
		},
		Value: &val,
	})

	rebuildTaggedValues := []metrics2.TaggedValue{}
	for rebuildType, rebuildValue := range cluster.Status.Stats.NumFailures {
		value := rebuildValue.GetValue()
		rebuildTaggedValues = append(rebuildTaggedValues, metrics2.TaggedValue{
			Tags:  metrics2.TagMap{"num_failures": fmt.Sprintf("%s", rebuildType)},
			Value: value,
		})
	}
	metrics = append(metrics, metrics2.PromMetric{
		Metric:       "weka_protection_level",
		ValuesByTags: rebuildTaggedValues,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_capacity",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "total"}, Value: float64(cluster.Status.Stats.Capacity.TotalBytes)},
			{Tags: metrics2.TagMap{"type": "unprovisioned"}, Value: float64(cluster.Status.Stats.Capacity.UnprovisionedBytes)},
			{Tags: metrics2.TagMap{"type": "unavailable"}, Value: float64(cluster.Status.Stats.Capacity.UnavailableBytes)},
			{Tags: metrics2.TagMap{"type": "hotSpare"}, Value: float64(cluster.Status.Stats.Capacity.HotSpareBytes)},
		},
	})

	for i, _ := range metrics {
		metrics[i].Tags = commonTags
		if metrics[i].Timestamp.IsZero() {
			metrics[i].Timestamp = cluster.Status.Stats.LastUpdate.Time
		}
	}

	retBuffer := strings.Builder{}
	for _, metric := range metrics {
		promString := metric.AsPrometheusString(nil)
		if promString != nil {
			retBuffer.WriteString(*promString)
			retBuffer.WriteString("\n")
		}
	}

	return retBuffer.String(), nil
}
