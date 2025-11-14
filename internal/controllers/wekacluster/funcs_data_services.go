package wekacluster

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/otel/codes"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) EnsureDataServicesGlobalConfig(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDataServicesGlobalConfig")
	defer end()

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err := wekaService.ConfigureDataServicesGlobalConfig(ctx)
	if err != nil {
		return err
	}

	logger.SetStatus(codes.Ok, "Data services global config ensured")

	return nil
}
