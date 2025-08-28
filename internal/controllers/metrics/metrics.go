package metrics

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/services"
	metrics2 "github.com/weka/weka-operator/pkg/metrics"
	"github.com/weka/weka-operator/pkg/util"
)

func BuildClusterPrometheusMetrics(ctx context.Context, cluster *v1alpha1.WekaCluster, wekaStatus services.WekaStatusResponse) (string, error) {
	_, logger, end := instrumentation.GetLogSpan(ctx, "BuildClusterPrometheusMetrics")
	defer end()
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
		Metric: "weka_throughput_bytes_per_second",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Stats.IoStats.Throughput.Read)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Stats.IoStats.Throughput.Write)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "DEPRECATED: Weka clusters throughput, use weka_cluster_throughput_bytes_per_second instead",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_throughput_bytes_per_second",
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
		Help:      "DEPRECATED: Weka clusters iops, use weka_cluster_iops instead",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_iops",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "read"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Read)},
			{Tags: metrics2.TagMap{"type": "write"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Write)},
			{Tags: metrics2.TagMap{"type": "metadata"}, Value: float64(cluster.Status.Stats.IoStats.Iops.Metadata)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka clusters iops",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_drives_count",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"status": "desired"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Desired)},
			{Tags: metrics2.TagMap{"status": "active"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Active)},
			{Tags: metrics2.TagMap{"status": "created"}, Value: float64(cluster.Status.Stats.Drives.DriveCounters.Created)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka cluster drives count, per status",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_processes_count",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "compute", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Compute.Processes.Active)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Drive.Processes.Active)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Compute.Processes.Desired)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Drive.Processes.Desired)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Compute.Processes.Created)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Drive.Processes.Created)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka clusters processes count, per type and status",
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_containers_count",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "compute", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Active)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "active"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Active)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Desired)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "desired"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Desired)},
			{Tags: metrics2.TagMap{"type": "compute", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Compute.Containers.Created)},
			{Tags: metrics2.TagMap{"type": "drive", "status": "created"}, Value: float64(cluster.Status.Stats.Containers.Drive.Containers.Created)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka containers count, per type and status",
	})
	// TODO: Conditionally add s3/nfs

	alertsVal := float64(cluster.Status.Stats.AlertsCount)
	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_alerts_count",
		Value:  &alertsVal,
	})

	clusterStatusVal := 0.0
	if slices.Contains([]string{
		"OK",
		"REDISTRIBUTING",
		"REBUILDING",
	}, string(cluster.Status.Stats.ClusterStatus)) {
		clusterStatusVal = 1.0
	} else {
		clusterStatusVal = 0.0
	}
	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_status",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{
				"status":           string(cluster.Status.Stats.ClusterStatus),
				"cr_status":        string(cluster.Status.Status),
				"stripe_width":     fmt.Sprintf("%d", wekaStatus.StripeWidth),
				"redundancy_level": fmt.Sprintf("%d", wekaStatus.RedundancyLevel),
				"hot_spare":        fmt.Sprintf("%d", wekaStatus.HotSpare),
			}, Value: clusterStatusVal},
		},
	})
	logger.Info("cluster statuses", "cluster_status", cluster.Status.Stats.ClusterStatus, "cr_status", cluster.Status.Status)

	rebuildTaggedValues := []metrics2.TaggedValue{}
	for rebuildType, rebuildValue := range cluster.Status.Stats.NumFailures {
		value := rebuildValue.GetValue()
		rebuildTaggedValues = append(rebuildTaggedValues, metrics2.TaggedValue{
			Tags:  metrics2.TagMap{"num_failures": fmt.Sprintf("%s", rebuildType)},
			Value: value,
		})
	}
	metrics = append(metrics, metrics2.PromMetric{
		Metric:       "weka_cluster_protection_level",
		ValuesByTags: rebuildTaggedValues,
	})

	metrics = append(metrics, metrics2.PromMetric{
		Metric: "weka_cluster_capacity_bytes",
		ValuesByTags: []metrics2.TaggedValue{
			{Tags: metrics2.TagMap{"type": "total"}, Value: float64(cluster.Status.Stats.Capacity.TotalBytes)},
			{Tags: metrics2.TagMap{"type": "unprovisioned"}, Value: float64(cluster.Status.Stats.Capacity.UnprovisionedBytes)},
			{Tags: metrics2.TagMap{"type": "unavailable"}, Value: float64(cluster.Status.Stats.Capacity.UnavailableBytes)},
			{Tags: metrics2.TagMap{"type": "hotSpare"}, Value: float64(cluster.Status.Stats.Capacity.HotSpareBytes)},
			// filesystem capacity metrics
			{Tags: metrics2.TagMap{"type": "fs_total"}, Value: float64(cluster.Status.Stats.Filesystem.TotalProvisionedCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_used"}, Value: float64(cluster.Status.Stats.Filesystem.TotalUsedCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_available"}, Value: float64(cluster.Status.Stats.Filesystem.TotalAvailableCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_ssd_total"}, Value: float64(cluster.Status.Stats.Filesystem.TotalProvisionedSSDCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_ssd_used"}, Value: float64(cluster.Status.Stats.Filesystem.TotalUsedSSDCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_ssd_available"}, Value: float64(cluster.Status.Stats.Filesystem.TotalAvailableSSDCapacity)},
			{Tags: metrics2.TagMap{"type": "fs_obs_total"}, Value: float64(cluster.Status.Stats.Filesystem.TotalObsCapacity)},
		},
		Timestamp: cluster.Status.Stats.LastUpdate.Time,
		Help:      "Weka cluster capacity in bytes",
	})

	if cluster.Status.Stats.Filesystem.HasTieredFilesystems {
		// Add OBS bucket count metrics
		metrics = append(metrics, metrics2.PromMetric{
			Metric: "weka_cluster_obs_buckets_count",
			ValuesByTags: []metrics2.TaggedValue{
				{Tags: metrics2.TagMap{"status": "total"}, Value: float64(cluster.Status.Stats.Filesystem.ObsBucketCount)},
				{Tags: metrics2.TagMap{"status": "active"}, Value: float64(cluster.Status.Stats.Filesystem.ActiveObsBucketCount)},
			},
			Timestamp: cluster.Status.Stats.LastUpdate.Time,
			Help:      "Number of Object Store buckets",
		})
	}

	for i, _ := range metrics {
		metrics[i].Tags = util.MergeMaps(metrics[i].Tags, commonTags)
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
