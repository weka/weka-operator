package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type S3ClusterCreatedError struct {
	errors.WrappedError
}

func (e S3ClusterCreatedError) Error() string {
	return "error creating S3 cluster: " + e.WrappedError.Error()
}

func (state *ClusterState) S3ClusterCreated() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "S3ClusterCreated")
		defer end()

		wekaCluster := state.Subject
		if wekaCluster == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		containers := state.Containers
		if len(containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		logger.SetPhase("CONFIGURING_DEFAULT_FS")
		containers = selectS3Containers(containers)
		if len(containers) == 0 {
			return nil
		}
		wekaClusterService, err := state.NewWekaClusterService()
		if err != nil {
			return &S3ClusterCreatedError{
				WrappedError: errors.NewWrappedError(ctx, err),
			}
		}
		if err := wekaClusterService.EnsureS3Cluster(ctx, containers); err != nil {
			return &S3ClusterCreatedError{
				WrappedError: errors.WrappedError{Err: err},
			}
		}

		return nil
	}
}

func selectS3Containers(containers []*wekav1alpha1.WekaContainer) []*wekav1alpha1.WekaContainer {
	var s3Containers []*wekav1alpha1.WekaContainer
	for _, container := range containers {
		if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 {
			s3Containers = append(s3Containers, container)
		}
	}
	return s3Containers
}
