// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-k8s-api/util"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/metrics"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	util2 "github.com/weka/weka-operator/pkg/util"
)

// GetThrottledMetricsSteps returns the metrics steps with appropriate throttling settings
func GetThrottledMetricsSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run:  loop.UpdateContainersCounters,
			Name: "UpdateContainersCounters",
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
			ContinueOnError: true,
		},
		&lifecycle.GroupedSteps{
			Name: "ThrottledMetricsSteps",
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Clusters.Enabled),
			},
			Steps: []lifecycle.Step{
				&lifecycle.SimpleStep{
					Run:  loop.UpdateWekaStatusMetrics,
					Name: "UpdateWekaStatusMetrics",
					Predicates: lifecycle.Predicates{
						func() bool {
							for _, c := range loop.cluster.Status.Conditions {
								if c.Type == condition.CondClusterCreated {
									if time.Since(c.LastTransitionTime.Time) > time.Second*5 {
										return true
									}
								}
							}
							return false
						},
						lifecycle.IsNotFunc(loop.cluster.IsTerminating),
					},
					Throttling: &throttling.ThrottlingSettings{
						Interval: config.Config.Metrics.Clusters.PollingRate,
					},
					ContinueOnError: true,
				},
				&lifecycle.SimpleStep{
					Run:  loop.EnsureClusterMonitoringService,
					Name: "EnsureClusterMonitoringService",
					Throttling: &throttling.ThrottlingSettings{
						Interval: config.Config.Metrics.Clusters.PollingRate,
					},
					ContinueOnError: true,
				},
			},
		},
	}
}

func (r *wekaClusterReconcilerLoop) UpdateContainersCounters(ctx context.Context) error {
	cluster := r.cluster
	containers := r.containers
	roleCreatedCounts := &sync.Map{}
	roleActiveCounts := &sync.Map{}
	driveCreatedCounts := &sync.Map{}
	maxCpu := map[string]float64{}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &weka.ClusterMetrics{}
	}

	// Idea to bubble up from containers, TODO: Actually use metrics and not accumulated status
	for _, container := range containers {
		// count Active containers
		if container.Status.Status == weka.Running && container.Status.ClusterContainerID != nil {
			tickCounter(roleActiveCounts, container.Spec.Mode, 1)
		}
		// count Created containers
		if container.Status.Status != weka.PodNotRunning {
			tickCounter(roleCreatedCounts, container.Spec.Mode, 1)
			if container.Spec.Mode == weka.WekaContainerModeDrive {
				tickCounter(driveCreatedCounts, container.Spec.Mode, int64(max(container.Spec.NumDrives, 1)))
			}
		}

		if container.Status.Stats == nil {
			continue
		}

		if container.Status.Stats.CpuUsage.GetValue() > 0.0 {
			if container.Status.Stats.CpuUsage.GetValue() > maxCpu[container.Spec.Mode] {
				maxCpu[container.Spec.Mode] = container.Status.Stats.CpuUsage.GetValue()
			}
		}
	}

	// calculate desired counts
	cluster.Status.Stats.Containers.Compute.Containers.Desired = weka.IntMetric(template.ComputeContainers)
	cluster.Status.Stats.Containers.Drive.Containers.Desired = weka.IntMetric(template.DriveContainers)
	if template.S3Containers != 0 {
		if cluster.Status.Stats.Containers.S3 == nil {
			cluster.Status.Stats.Containers.S3 = &weka.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.S3.Containers.Desired = weka.IntMetric(template.S3Containers)
	}
	if template.NfsContainers != 0 {
		if cluster.Status.Stats.Containers.Nfs == nil {
			cluster.Status.Stats.Containers.Nfs = &weka.ContainerMetrics{}
		}
		cluster.Status.Stats.Containers.Nfs.Containers.Desired = weka.IntMetric(template.NfsContainers)
	}

	cluster.Status.Stats.Drives.DriveCounters.Desired = weka.IntMetric(template.DriveContainers * template.NumDrives)

	// convert to new metrics accessor
	cluster.Status.Stats.Containers.Compute.Processes.Desired = weka.IntMetric(max(int64(template.ComputeCores), 1) * int64(template.ComputeContainers))
	cluster.Status.Stats.Containers.Drive.Processes.Desired = weka.IntMetric(max(int64(template.DriveCores), 1) * int64(template.DriveContainers))

	// propagate "created" counters
	cluster.Status.Stats.Containers.Compute.Containers.Created = weka.IntMetric(getCounter(roleCreatedCounts, weka.WekaContainerModeCompute))
	cluster.Status.Stats.Containers.Drive.Containers.Created = weka.IntMetric(getCounter(roleCreatedCounts, weka.WekaContainerModeDrive))

	// propagate "active" counters for these that not exposed explicitly in weka status --json
	if template.S3Containers != 0 {
		cluster.Status.Stats.Containers.S3.Containers.Active = weka.IntMetric(getCounter(roleActiveCounts, weka.WekaContainerModeS3))
	}
	if template.NfsContainers != 0 {
		cluster.Status.Stats.Containers.Nfs.Containers.Active = weka.IntMetric(getCounter(roleActiveCounts, weka.WekaContainerModeNfs))
	}

	// fill in utilization
	if maxCpu[weka.WekaContainerModeCompute] > 0 {
		cluster.Status.Stats.Containers.Compute.CpuUtilization = weka.NewFloatMetric(maxCpu[weka.WekaContainerModeCompute])
	}

	if maxCpu[weka.WekaContainerModeDrive] > 0 {
		cluster.Status.Stats.Containers.Drive.CpuUtilization = weka.NewFloatMetric(maxCpu[weka.WekaContainerModeDrive])
	}

	if maxCpu[weka.WekaContainerModeS3] > 0 {
		cluster.Status.Stats.Containers.S3.CpuUtilization = weka.NewFloatMetric(maxCpu[weka.WekaContainerModeS3])
	}

	// prepare printerColumns
	cluster.Status.PrinterColumns.ComputeContainers = weka.StringMetric(cluster.Status.Stats.Containers.Compute.Containers.String())
	cluster.Status.PrinterColumns.DriveContainers = weka.StringMetric(cluster.Status.Stats.Containers.Drive.Containers.String())
	cluster.Status.PrinterColumns.Drives = weka.StringMetric(cluster.Status.Stats.Drives.DriveCounters.String())

	return r.getClient().Status().Update(ctx, cluster)
}

func (r *wekaClusterReconcilerLoop) UpdateWekaStatusMetrics(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	if cluster.IsTerminating() || r.cluster.Status.Status != weka.WekaClusterStatusReady {
		return nil
	}

	activeContainer := discovery.SelectActiveContainer(r.containers)
	if activeContainer == nil {
		return errors.New("No active container found")
	}

	timeout := time.Second * 30
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, activeContainer, &timeout)
	wekaStatus, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to get Weka status")
	}

	// Get filesystem capacity information
	capacity, err := wekaService.GetCapacity(ctx)
	if err != nil {
		// Log error but continue - we still want to update other metrics
		logger.Error(err, "Failed to get filesystem capacity information")
	}

	if cluster.Status.Stats == nil {
		cluster.Status.Stats = &weka.ClusterMetrics{}
	}

	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	cluster.Status.Stats.Containers.Compute.Processes.Active = weka.IntMetric(int64(wekaStatus.Containers.Computes.Active * template.ComputeCores))
	cluster.Status.Stats.Containers.Compute.Containers.Active = weka.IntMetric(int64(wekaStatus.Containers.Computes.Active))
	cluster.Status.Stats.Containers.Drive.Processes.Active = weka.IntMetric(int64(wekaStatus.Containers.Drives.Active * template.DriveCores))
	cluster.Status.Stats.Containers.Drive.Containers.Active = weka.IntMetric(int64(wekaStatus.Containers.Drives.Active))
	cluster.Status.Stats.Drives.DriveCounters.Active = weka.IntMetric(int64(wekaStatus.Drives.Active))

	cluster.Status.Stats.Containers.Compute.Processes.Created = weka.IntMetric(int64(wekaStatus.Containers.Computes.Total * template.ComputeCores))
	cluster.Status.Stats.Containers.Drive.Processes.Created = weka.IntMetric(int64(wekaStatus.Containers.Drives.Total * template.DriveCores))
	// TODO: might be incorrect with bad drives, and better to buble up from containers
	cluster.Status.Stats.Drives.DriveCounters.Created = weka.IntMetric(int64(wekaStatus.Drives.Total))
	//TODO: this should go via template builder and not direct dynamic access
	//if cluster.Spec.Dynamic.S3Containers != 0 {
	//TODO: S3 cant be implemented this way, should buble up from containers instead(for all)
	//}

	cluster.Status.Stats.IoStats.Throughput.Read = weka.IntMetric(int64(wekaStatus.Activity.SumBytesRead))
	cluster.Status.Stats.IoStats.Throughput.Write = weka.IntMetric(int64(wekaStatus.Activity.SumBytesWritten))
	cluster.Status.Stats.IoStats.Iops.Read = weka.IntMetric(int64(wekaStatus.Activity.NumReads))
	cluster.Status.Stats.IoStats.Iops.Write = weka.IntMetric(int64(wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Metadata = weka.IntMetric(int64(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites))
	cluster.Status.Stats.IoStats.Iops.Total = weka.IntMetric(int64(wekaStatus.Activity.NumOps))
	cluster.Status.Stats.AlertsCount = weka.IntMetric(wekaStatus.ActiveAlertsCount)
	cluster.Status.Stats.ClusterStatus = weka.StringMetric(wekaStatus.Status)
	cluster.Status.Stats.ClusterStatus = weka.StringMetric(wekaStatus.Status)
	cluster.Status.Stats.NumFailures = map[string]weka.FloatMetric{} // resetting every time as some keys might go away
	for _, rebuildDetails := range wekaStatus.Rebuild.ProtectionState {
		cluster.Status.Stats.NumFailures[strconv.Itoa(rebuildDetails.NumFailures)] = weka.NewFloatMetric(rebuildDetails.Percent)
	}

	cluster.Status.Stats.Capacity.TotalBytes = weka.IntMetric(wekaStatus.Capacity.TotalBytes)
	cluster.Status.Stats.Capacity.UnavailableBytes = weka.IntMetric(wekaStatus.Capacity.UnavailableBytes)
	cluster.Status.Stats.Capacity.UnprovisionedBytes = weka.IntMetric(wekaStatus.Capacity.UnprovisionedBytes)
	cluster.Status.Stats.Capacity.HotSpareBytes = weka.IntMetric(wekaStatus.Capacity.HotSpareBytes)

	// Update filesystem capacity metrics if available
	if len(capacity.Filesystems) > 0 {
		// Initialize the Filesystem field if it's nil
		if cluster.Status.Stats.Filesystem.TotalProvisionedCapacity == 0 {
			cluster.Status.Stats.Filesystem = weka.FilesystemMetrics{}
		}

		// Update the metrics with data from GetCapacity
		cluster.Status.Stats.Filesystem.TotalProvisionedCapacity = weka.IntMetric(capacity.TotalProvisionedCapacity)
		cluster.Status.Stats.Filesystem.TotalUsedCapacity = weka.IntMetric(capacity.TotalUsedCapacity)
		cluster.Status.Stats.Filesystem.TotalAvailableCapacity = weka.IntMetric(capacity.TotalAvailableCapacity)

		// SSD-specific metrics
		cluster.Status.Stats.Filesystem.TotalProvisionedSSDCapacity = weka.IntMetric(capacity.TotalProvisionedSSDCapacity)
		cluster.Status.Stats.Filesystem.TotalUsedSSDCapacity = weka.IntMetric(capacity.TotalUsedSSDCapacity)
		cluster.Status.Stats.Filesystem.TotalAvailableSSDCapacity = weka.IntMetric(capacity.TotalAvailableSSDCapacity)

		// Object Store information
		cluster.Status.Stats.Filesystem.HasTieredFilesystems = capacity.HasTieredFilesystems
		cluster.Status.Stats.Filesystem.ObsBucketCount = weka.IntMetric(capacity.ObsBucketCount)
		cluster.Status.Stats.Filesystem.ActiveObsBucketCount = weka.IntMetric(capacity.ActiveObsBucketCount)

		if capacity.HasTieredFilesystems && capacity.TotalObsCapacity > 0 {
			cluster.Status.Stats.Filesystem.TotalObsCapacity = weka.IntMetric(capacity.TotalObsCapacity)
		}

		// Add filesystem capacity information to printer columns
		availableGiB := util2.HumanReadableSize(int64(cluster.Status.Stats.Filesystem.TotalAvailableCapacity))
		usedGiB := util2.HumanReadableSize(int64(cluster.Status.Stats.Filesystem.TotalUsedCapacity))
		cluster.Status.PrinterColumns.FilesystemCapacity = weka.StringMetric(fmt.Sprintf("%s/%s", usedGiB, availableGiB))
	}

	tpsRead := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesRead)
	tpsWrite := util2.HumanReadableThroughput(wekaStatus.Activity.SumBytesWritten)
	cluster.Status.PrinterColumns.Throughput = weka.StringMetric(fmt.Sprintf("%s/%s", tpsRead, tpsWrite))

	iopsRead := util2.HumanReadableIops(wekaStatus.Activity.NumReads)
	iopsWrite := util2.HumanReadableIops(wekaStatus.Activity.NumWrites)
	iopsOther := util2.HumanReadableIops(wekaStatus.Activity.NumOps - wekaStatus.Activity.NumReads - wekaStatus.Activity.NumWrites)
	cluster.Status.PrinterColumns.Iops = weka.StringMetric(fmt.Sprintf("%s/%s/%s", iopsRead, iopsWrite, iopsOther))
	cluster.Status.Stats.LastUpdate = metav1.NewTime(time.Now())

	if err := r.getClient().Status().Update(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) EnsureClusterMonitoringService(ctx context.Context) error {
	// TODO: Re-wrap as operation

	identifierLabels := map[string]string{
		"app":                "weka-cluster-monitoring",
		"weka.io/cluster-id": string(r.cluster.GetUID()),
	}

	labels := make(map[string]string)
	for k, v := range identifierLabels {
		labels[k] = v
	}
	for k, v := range r.cluster.Labels {
		labels[k] = v
	}

	annototations := map[string]string{
		// prometheus annotations
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   "80",
		"prometheus.io/path":   "/metrics",
	}

	deployment := apps.Deployment{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "monitoring-" + r.cluster.Name,
			Namespace: r.cluster.Namespace,
			Labels:    labels,
		},
		Spec: apps.DeploymentSpec{
			Replicas: util2.Int32Ref(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":                "weka-cluster-monitoring",
					"weka.io/cluster-id": string(r.cluster.GetUID()),
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:      labels,
					Annotations: annototations,
				},
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{Name: r.cluster.Spec.ImagePullSecret},
					},
					Tolerations:  util.ExpandTolerations([]v1.Toleration{}, r.cluster.Spec.Tolerations, r.cluster.Spec.RawTolerations),
					NodeSelector: r.cluster.Spec.NodeSelector, //TODO: Monitoring-specific node-selector
					Containers: []v1.Container{
						{
							Name:  "weka-cluster-metrics",
							Image: config.Config.Metrics.Clusters.Image,
							Ports: []v1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
							Command: []string{
								"/bin/sh",
								"-c",
								`echo 'server {
								listen 80;
								location / {
									root /data;
									autoindex on;
									default_type text/plain;
								}
							}' > /etc/nginx/conf.d/default.conf &&
							nginx -g 'daemon off;'`,
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "data",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
	err := ctrl.SetControllerReference(r.cluster, &deployment, r.Manager.GetScheme())
	if err != nil {
		return err
	}

	upsert := func() error {
		err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
		if err == nil {
			// already exists, no need to stress api with creates
			return nil
		}

		err = r.getClient().Create(ctx, &deployment)
		if err != nil {
			if alreadyExists := client.IgnoreAlreadyExists(err); alreadyExists == nil {
				//fetch current deployment
				err = r.getClient().Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, &deployment)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		return nil
	}

	if err := upsert(); err != nil {
		return err
	}

	// we should have deployment at hand now
	kubeService := kubernetes.NewKubeService(r.getClient())
	// searching for own pods
	pods, err := kubeService.GetPodsSimple(ctx, r.cluster.Namespace, "", identifierLabels)
	if err != nil {
		return err
	}
	// find running pod
	var pod *v1.Pod
	for _, p := range pods {
		if p.Status.Phase == v1.PodRunning {
			pod = &p
			break
		}
	}
	if pod == nil {
		return lifecycle.NewWaitError(errors.New("No running monitoring pod found"))
	}

	exec, err := util2.NewExecInPodByName(r.RestClient, r.Manager.GetConfig(), pod, "weka-cluster-metrics")
	if err != nil {
		return err
	}

	container := discovery.SelectActiveContainer(r.containers)

	timeout := time.Second * 30
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	data, err := metrics.BuildClusterPrometheusMetrics(ctx, r.cluster, status)
	if err != nil {
		return err
	}
	// finally write a data
	// better data operation/labeling to come
	cmd := []string{
		"/bin/sh",
		"-ec",
		`cat <<EOF > /data/metrics.tmp
` + data + `
EOF
mv /data/metrics.tmp /data/metrics
`,
	}
	stdout, stderr, err := exec.ExecNamed(ctx, "WriteMetrics", cmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to write metrics: %s\n%s", stderr.String(), stdout.String())
	}

	return nil
}

func tickCounter(counters *sync.Map, key string, value int64) {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		ptr += value
		counters.Store(key, ptr)
	} else {
		counters.Store(key, value)
	}
}

func getCounter(counters *sync.Map, key string) int64 {
	val, ok := counters.Load(key)
	if ok {
		ptr := val.(int64)
		return ptr
	}
	return 0
}
