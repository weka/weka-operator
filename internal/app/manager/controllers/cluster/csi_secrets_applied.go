package cluster

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
)

type CSISecretsAppliedError struct {
	errors.WrappedError
	Cluster *wekav1alpha1.WekaCluster
}

type stepState struct {
	*ClusterState
	WekaClusterService services.WekaClusterService
	ExecService        services.ExecService
}

func (state *ClusterState) ClusterCSISecretsApplied(wekaClusterService services.WekaClusterService, execService services.ExecService) lifecycle.StepFunc {
	step := &stepState{
		ClusterState:       state,
		WekaClusterService: wekaClusterService,
		ExecService:        execService,
	}
	return step.ClusterCSISecretsApplied
}

func (state *stepState) ClusterCSISecretsApplied(ctx context.Context) error {
	ctx, _, done := instrumentation.GetLogSpan(ctx, "ClusterCSISecretsApplied")
	defer done()

	cluster := state.Subject
	if cluster == nil {
		return &lifecycle.StateError{Property: "Subject", Message: "Subject is nil"}
	}

	containers := state.Containers
	if containers == nil {
		return &lifecycle.StateError{Property: "Containers", Message: "Containers is nil"}
	}

	err := state.applyCSILoginCredentials(ctx, cluster, containers)
	if err != nil {
		return &CSISecretsAppliedError{
			WrappedError: errors.WrappedError{
				Err:  err,
				Span: instrumentation.GetLogName(ctx),
			},
			Cluster: cluster,
		}
	}

	return nil
}

func (state *stepState) applyCSILoginCredentials(ctx context.Context, cluster *wekav1alpha1.WekaCluster, containers []*wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "applyCSILoginCredentials")
	defer end()

	container := selectActiveContainer(containers)
	clusterService := state.WekaClusterService
	username, password, err := clusterService.GetUsernameAndPassword(ctx, cluster.Namespace, cluster.GetCSISecretName())
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(state.ExecService, container)
	err = wekaService.EnsureUser(ctx, username, password, "clusteradmin")
	if err != nil {
		logger.Error(err, "Failed to ensure user")
		return err
	}
	logger.SetStatus(codes.Ok, "CSI login credentials applied")
	return nil
}

func selectActiveContainer(containers []*wekav1alpha1.WekaContainer) *wekav1alpha1.WekaContainer {
	for _, container := range containers {
		if container.Status.ClusterContainerID != nil {
			return container
		}
	}
	return nil
}
