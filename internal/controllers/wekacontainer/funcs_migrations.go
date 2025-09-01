package wekacontainer

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/operations/tempops"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *containerReconcilerLoop) migrateEnsurePorts(ctx context.Context) error {
	if r.container.Spec.ExposePorts == nil {
		return nil
	}

	if !r.container.IsEnvoy() {
		return nil // we never set old format for anything but envoy
	}

	if len(r.container.Spec.ExposePorts) == 2 {
		r.container.Spec.ExposedPorts = []v1.ContainerPort{{
			Name:          "envoy",
			ContainerPort: int32(r.container.Spec.ExposePorts[0]),
			HostPort:      int32(r.container.Spec.ExposePorts[0]),
		},
			{
				Name:          "envoy-admin",
				ContainerPort: int32(r.container.Spec.ExposePorts[1]),
				HostPort:      int32(r.container.Spec.ExposePorts[1]),
			},
		}

		r.container.Spec.ExposePorts = nil
		return r.Update(ctx, r.container)
	}

	return nil
}

func (r *containerReconcilerLoop) MigratePVC(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "MigratePVC")
	defer end()

	logger.Info("Starting PVC migration operation")

	op := tempops.NewPvcMigrateOperation(r.Manager, r.container)
	err := operations.ExecuteOperation(ctx, op)
	if err != nil {
		logger.Error(err, "PVC migration operation failed")
		return err // Keep retrying until job succeeds or fails definitively
	}

	logger.Info("PVC migration job completed successfully")

	// After successful migration, update the container spec to remove PVC reference
	// and potentially set a flag indicating migration is done.
	// This prevents re-running the migration on subsequent reconciles.
	// This will update also containers like dist service, which is yaml controlled
	// While this is not desired, for cases we plan to use it this should be fine
	patch := client.MergeFrom(r.container.DeepCopy())
	r.container.Spec.PVC = nil
	// Optionally add an annotation or status field to mark migration complete
	// if r.container.Annotations == nil {
	// 	r.container.Annotations = make(map[string]string)
	// }
	// r.container.Annotations["weka.io/pvc-migrated"] = "true"

	err = r.Patch(ctx, r.container, patch)
	if err != nil {
		logger.Error(err, "Failed to patch container spec after PVC migration")
		return errors.Wrap(err, "failed to patch container spec after PVC migration")
	}

	logger.Info("Container spec updated to remove PVC reference")
	// Returning an error here forces a requeue, allowing the reconciler to
	// proceed with pod creation using the updated spec (without PVC).
	return lifecycle.NewExpectedError(errors.New("requeue after successful PVC migration and spec update"))
}
