package wekacluster

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	util2 "github.com/weka/weka-operator/pkg/util"
)

func (r *wekaClusterReconcilerLoop) getReadyForClusterCreateContainers(ctx context.Context) *ReadyForClusterizationContainers {
	if r.readyContainers != nil {
		return r.readyContainers
	}

	cluster := r.cluster
	containers := r.containers

	findSameNetworkConfig := len(cluster.Spec.Network.DeviceSubnets) > 0
	readyContainers := &ReadyForClusterizationContainers{}

	for _, container := range containers {
		if container.GetDeletionTimestamp() != nil {
			continue
		}
		if container.Status.Status == weka.Unhealthy {
			readyContainers.Ignored = append(readyContainers.Ignored, container)
			continue
		}
		if container.Status.Status != weka.Running {
			continue
		}
		// if deviceSubnets are provided, we should only consider containers that have devices in the provided subnets
		if findSameNetworkConfig {
			if !resources.ContainerHasDevicesInSubnets(container, cluster.Spec.Network.DeviceSubnets) {
				readyContainers.Ignored = append(readyContainers.Ignored, container)
			}
		}
		if container.Spec.Mode == weka.WekaContainerModeDrive {
			readyContainers.Drive = append(readyContainers.Drive, container)
		}
		if container.Spec.Mode == weka.WekaContainerModeCompute {
			readyContainers.Compute = append(readyContainers.Compute, container)
		}
	}

	r.readyContainers = readyContainers
	return readyContainers
}

// InitialContainersReady Logic:
// there are 4 consts defined in config:
//   - FormClusterMinDriveContainers   (5 by default)
//   - FormClusterMinComputeContainers (5 by default)
//   - FormClusterMaxDriveContainers   (10 by default)
//   - FormClusterMaxComputeContainers (10 by default)
//
// Uses getReadyForClusterCreateContainers to get the containers that are UP and ready for cluster creation
// and containers that should be ignored (e.g. unhealthy).
//
// Expected containers number is derived from the template:
//   - expectedComputeContainersNum = min(template.ComputeContainers, FormClusterMaxComputeContainers)
//   - expectedDriveContainersNum = min(template.DriveContainers, FormClusterMaxDriveContainers)
//
// The following checks are performed:
//   - number of "ready" drive containers < FormClusterMinDriveContainers --> error
//   - number of "ready" compute containers < FormClusterMinComputeContainers --> error
//   - number of "ready" containers + number of "ignored" containers < expectedComputeContainersNum + expectedDriveContainersNum --> error
func (r *wekaClusterReconcilerLoop) InitialContainersReady(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	minDriveContainers := config.Consts.FormClusterMinDriveContainers
	minComputeContainers := config.Consts.FormClusterMinComputeContainers

	findSameNetworkConfig := len(cluster.Spec.Network.DeviceSubnets) > 0
	if findSameNetworkConfig {
		msg := fmt.Sprintf("Looking for %d compute and %d drive containers with device subnets %v", minComputeContainers, minDriveContainers, cluster.Spec.Network.DeviceSubnets)
		logger.Debug(msg)
	} else {
		msg := fmt.Sprintf("Looking for %d compute and %d drive containers", minComputeContainers, minDriveContainers)
		logger.Debug(msg)
	}

	// containers that UP and ready for cluster creation
	readyContainers := r.getReadyForClusterCreateContainers(ctx)
	driveContainersCount := len(readyContainers.Drive)
	computeContainersCount := len(readyContainers.Compute)
	ignoredContainersCount := len(readyContainers.Ignored)

	if driveContainersCount < minDriveContainers {
		err := fmt.Errorf("not enough drive containers ready, expected %d, got %d", minDriveContainers, driveContainersCount)
		r.RecordEvent("", "MinContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}
	if computeContainersCount < minComputeContainers {
		err := fmt.Errorf("not enough compute containers ready, expected %d, got %d", minComputeContainers, computeContainersCount)
		r.RecordEvent("", "MinContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	// check expected containers number
	template, ok := allocator.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		return errors.New("Failed to get template")
	}

	expectedComputeContainersNum := util2.GetMinValue(template.ComputeContainers, config.Consts.FormClusterMaxComputeContainers)
	expectedDriveContainersNum := util2.GetMinValue(template.DriveContainers, config.Consts.FormClusterMaxDriveContainers)

	if driveContainersCount+computeContainersCount+ignoredContainersCount < expectedComputeContainersNum+expectedDriveContainersNum {
		err := fmt.Errorf("waiting for all containers to be either ready or ignored before forming the cluster, expected %d, got %d", expectedComputeContainersNum+expectedDriveContainersNum, driveContainersCount+computeContainersCount+ignoredContainersCount)
		r.RecordEvent("", "ContainersNotReady", err.Error())
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	msg := fmt.Sprintf("Initial containers are ready for cluster creation, drive containers: %d, compute containers: %d", driveContainersCount, computeContainersCount)
	r.RecordEvent("", "InitialContainersReady", msg)
	logger.InfoWithStatus(codes.Ok, msg)

	return nil
}

func (r *wekaClusterReconcilerLoop) FormCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	wekaCluster := r.cluster
	wekaClusterService := r.clusterService

	if wekaCluster.Spec.ExpandEndpoints == nil {
		readyContainers := r.getReadyForClusterCreateContainers(ctx)

		err := wekaClusterService.FormCluster(ctx, readyContainers.GetAll())
		if err != nil {
			logger.Error(err, "Failed to form cluster")
			return lifecycle.NewWaitError(err)
		}
		return nil
		// TODO: We might want to capture specific errors, and return "unknown"/bigger errors
	}
	return nil
}

// Waits for initial containers to join the cluster right after the cluster is formed
func (r *wekaClusterReconcilerLoop) WaitForContainersJoin(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	readyContainers := r.getReadyForClusterCreateContainers(ctx)

	logger.Debug("Ensuring all ready containers are up in the cluster")
	joinedContainers := 0
	for _, container := range readyContainers.GetAll() {
		if !container.ShouldJoinCluster() {
			continue
		}
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
			logger.Info("Container has not joined the cluster yet", "container", container.Name)
			return lifecycle.NewWaitError(errors.New("container did not join cluster yet"))
		} else {
			if r.cluster.Status.ClusterID == "" {
				r.cluster.Status.ClusterID = container.Status.ClusterID
				err := r.getClient().Status().Update(ctx, r.cluster)
				if err != nil {
					return err
				}
				logger.Info("Container joined cluster successfully", "container_name", container.Name)
			}
			joinedContainers++
		}
		if container.Status.ClusterContainerID == nil {
			err := fmt.Errorf("container %s does not have a cluster container id", container.Name)
			r.RecordEvent(v1.EventTypeWarning, "ContainerJoinError", err.Error())
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) RunPostFormClusterScript(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RunPostFormClusterScript")
	defer end()

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, r.containers, weka.WekaContainerModeDrive)
	if err != nil {
		return err
	}
	executor, err := r.ExecService.GetExecutor(ctx, activeContainer)
	if err != nil {
		return err
	}

	script := r.cluster.Spec.GetOverrides().PostFormClusterScript
	cmd := []string{
		"/bin/sh",
		"-ec",
		script, // TODO: Might need additional escaping
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "PostFormClusterScript", cmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to run post-form cluster script: %s\n%s", stderr.String(), stdout.String())
	}

	logger.Info("Post-form cluster script executed")

	return nil
}

func (r *wekaClusterReconcilerLoop) WaitForDrivesAdd(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusWaitDrives)
	if err != nil {
		return err
	}
	// minimum required number of drives to be added to the cluster
	var minDrivesNum int

	if r.cluster.Spec.GetStartIoConditions().MinNumDrives > 0 {
		minDrivesNum = r.cluster.Spec.StartIoConditions.MinNumDrives
	}

	// if not provided, derive it from the template
	if minDrivesNum == 0 {
		template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
		if !ok {
			return errors.New("Failed to get template")
		}
		minDrivesNum = template.NumDrives * template.DriveContainers
	}

	// get the number of drives added to the cluster from weka status
	container := discovery.SelectActiveContainer(r.containers)

	timeout := time.Second * 30
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	totalDrivesNum := status.Drives.Total
	if totalDrivesNum >= minDrivesNum {
		logger.Info("Min drives num added to the cluster", "addedDrivesNum", totalDrivesNum, "minDrivesNum", minDrivesNum)
		return nil
	}

	msg := fmt.Sprintf("Min drives num not added to the cluster yet, added drives: %d, min drives: %d", totalDrivesNum, minDrivesNum)
	r.RecordEvent("", "WaitingForDrives", msg)

	return lifecycle.NewWaitErrorWithDuration(errors.New("containers did not add drives yet"), 10*time.Second)
}

func (r *wekaClusterReconcilerLoop) StartIo(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	err := r.updateClusterStatusIfNotEquals(ctx, weka.WekaClusterStatusStartingIO)
	if err != nil {
		return err
	}

	containers := r.containers

	if len(containers) == 0 {
		err := errors.New("containers list is empty")
		logger.Error(err, "containers list is empty")
		return err
	}

	kubeExecTimeout := config.Config.Timeouts.KubeExecTimeout
	if kubeExecTimeout < 10*time.Minute {
		kubeExecTimeout = 10 * time.Minute
	}

	execInContainer := discovery.SelectActiveContainer(containers)
	executor, err := r.ExecService.GetExecutorWithTimeout(ctx, execInContainer, &kubeExecTimeout)
	if err != nil {
		return errors.Wrap(err, "Error creating executor")
	}

	cmd := "wekaauthcli cluster start-io"
	_, stderr, err := executor.ExecNamed(ctx, "StartIO", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Failed to start-io")
		return errors.Wrapf(err, "Failed to start-io: %s", stderr.String())
	}
	logger.InfoWithStatus(codes.Ok, "IO started", "time_taken", time.Since(r.cluster.CreationTimestamp.Time).String())

	return nil
}
