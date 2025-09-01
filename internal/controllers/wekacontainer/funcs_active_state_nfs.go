package wekacontainer

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/services"
)

func (r *containerReconcilerLoop) JoinNfsInterfaceGroups(ctx context.Context) error {
	wekaService := services.NewWekaService(r.ExecService, r.container)
	err := wekaService.JoinNfsInterfaceGroups(ctx, *r.container.Status.ClusterContainerID)
	var nfsErr *services.NfsInterfaceGroupAlreadyJoined
	if !errors.As(err, &nfsErr) {
		return err
	}
	return nil
}
