package wekacluster

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) EnsureS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	// check if s3 cluster already exists
	s3Cluster, err := wekaService.GetS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to get S3 cluster")
		return err
	}

	if s3Cluster.Active || len(s3Cluster.S3Hosts) > 0 {
		logger.Info("S3 cluster already exists")
		return nil
	}

	s3Containers := r.SelectS3Containers(r.containers)
	containerIds := []int{}
	for _, c := range s3Containers {
		if len(containerIds) == config.Consts.FormS3ClusterMaxContainerCount {
			logger.Debug("Max S3 containers reached for initial s3 cluster creation", "maxContainers", config.Consts.FormS3ClusterMaxContainerCount)
			break
		}

		if c.Status.ClusterContainerID == nil {
			msg := fmt.Sprintf("s3 container %s does not have a cluster container id", c.Name)
			logger.Debug(msg)
			continue
		}
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	if len(containerIds) == 0 {
		err := errors.New("Ready S3 containers not found")
		logger.Error(err, "Cannot create S3 cluster")
		return err
	}

	logger.Debug("Creating S3 cluster", "containers", containerIds)

	var clusterCreateArgs []string
	if cluster.Spec.S3Config != nil {
		clusterCreateArgs = cluster.Spec.S3Config.ClusterCreateArgs
	}

	err = wekaService.CreateS3Cluster(ctx, services.S3Params{
		EnvoyPort:         cluster.Status.Ports.LbPort,
		EnvoyAdminPort:    cluster.Status.Ports.LbAdminPort,
		S3Port:            cluster.Status.Ports.S3Port,
		ContainerIds:      containerIds,
		ClusterCreateArgs: clusterCreateArgs,
	})
	if err != nil {
		var s3ClusterExists *services.S3ClusterExists
		if !errors.As(err, &s3ClusterExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "S3 cluster ensured")

	return nil
}

func (r *wekaClusterReconcilerLoop) ShouldDestroyS3Cluster() bool {
	if !r.cluster.Spec.GetOverrides().AllowS3ClusterDestroy {
		return false
	}

	// if spec contains desired S3 containers, do not destroy the cluster
	template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
	if !ok {
		return false
	}
	if template.S3Containers > 0 {
		return false
	}

	containers := r.SelectS3Containers(r.containers)

	// if there are more that 1 S3 container, we should not destroy the cluster
	if len(containers) > 1 {
		return false
	}

	// if S3 cluster was not created, we should not destroy it
	if !meta.IsStatusConditionTrue(r.cluster.Status.Conditions, condition.CondS3ClusterCreated) {
		return false
	}

	return true
}

func (r *wekaClusterReconcilerLoop) DestroyS3Cluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)

	s3ContainerIds, err := wekaService.ListS3ClusterContainers(ctx)
	if err != nil {
		return err
	}
	logger.Info("S3 cluster containers", "containers", s3ContainerIds)

	if len(s3ContainerIds) > 1 {
		err := fmt.Errorf("more than one container in S3 cluster: %v", s3ContainerIds)
		return lifecycle.NewWaitError(err)
	}

	logger.Info("Destroying S3 cluster")
	err = wekaService.DeleteS3Cluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to delete S3 cluster")
		return err
	}

	// invalidate S3 cluster created condition
	changed := meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondS3ClusterCreated,
		Status: metav1.ConditionFalse,
		Reason: "DestroyS3Cluster",
	})
	if changed {
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			return err
		}
	}

	return nil
}
