package wekacluster

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) EnsureNfs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNfs")
	defer end()

	// nfsContainers := r.SelectNfsContainers(r.containers)

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		return errors.New("No active container found")
	}
	wekaService := services.NewWekaService(r.ExecService, execInContainer)
	// containerIds := []int{}
	// for _, c := range nfsContainers {
	// 	containerIds = append(containerIds, *c.Status.ClusterContainerID)
	// }

	err := wekaService.ConfigureNfs(ctx, services.NFSParams{
		ConfigFilesystem: ".config_fs",
	})

	if err != nil {
		var nfsIgExists *services.NfsInterfaceGroupExists
		if !errors.As(err, &nfsIgExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "NFS ensured")
	return nil
}
