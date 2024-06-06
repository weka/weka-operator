package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type CondStartIoError struct {
	lifecycle.ConditionExecutionError
	Cluster *wekav1alpha1.WekaCluster
}

func (e CondStartIoError) Error() string {
	return fmt.Sprintf("error starting IO for cluster %s: %v", e.Cluster.Name, e.Err)
}

func (state *ClusterState) StartIo() lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "StartIo")
		defer end()

		if state.Subject == nil {
			return &errors.ArgumentError{ArgName: "Cluster", Message: "Cluster is nil"}
		}
		wekaCluster := state.Subject

		if state.Containers == nil {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is nil"}
		}
		if len(state.Containers) == 0 {
			return &errors.ArgumentError{ArgName: "Containers", Message: "Containers is empty"}
		}

		containers := state.Containers
		execService := state.ExecService
		wekaService := services.NewWekaService(execService, containers[0])
		if err := wekaService.StartIo(ctx); err != nil {
			return &CondStartIoError{
				ConditionExecutionError: lifecycle.ConditionExecutionError{
					Err:       err,
					Condition: condition.CondIoStarted,
				},
				Cluster: wekaCluster,
			}
		}
		logger.Info("IO Started, time since create:" + time.Since(wekaCluster.CreationTimestamp.Time).String())
		logger.SetPhase("IO_IS_STARTED")
		return nil
	}
}
